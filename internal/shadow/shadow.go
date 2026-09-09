// Package shadow 实现 octl 的影子库：把 opencode DB 的 session/message 元数据
// 与用户/assistant 文本（text、reasoning 部件，不含 tool 输出）持续镜像到
// octl 自己的 SQLite 档案库。镜像永不删除——opencode 侧的删除在这里只是
// deleted_at_ms 标记，日报/周报因此能把"窗口内被删的线"当一等公民查出。
// 真正的删除只有一个出口：Purge*（含保留期策略与 octl purge）。
package shadow

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/tomasWade/octl/internal/types"
)

// DB 是影子库连接。所有方法并发安全（database/sql 内部连接池 + 单文件
// SQLite；写操作均短事务）。
type DB struct {
	db     *sql.DB
	dbPath string
}

// SessionRow 是影子库 sessions 表的行：镜像自 opencode session 表的元数据
// 加影子库自身的生命周期标记。
type SessionRow struct {
	ID          string
	Title       string
	ProjectID   string
	Directory   string
	CreatedMs   int64
	UpdatedMs   int64
	ArchivedMs  int64
	DeletedAtMs int64 // 0 = 未删；非 0 = 删除时刻（unix ms）
	MsgCount    int64
	Cost        float64
}

// MessageRow 是影子库 messages 表的行（消息级元数据；正文在 parts 表）。
type MessageRow struct {
	ID        string
	SessionID string
	Role      string
	Agent     string
	ModelJSON string
	CreatedMs int64
	UpdatedMs int64
}

// PartRow 是影子库 parts 表的行。只镜像 text / reasoning 两种部件
// （用户输入与 AI 输出），tool 输出不存。
type PartRow struct {
	ID        string
	MessageID string
	SessionID string
	Kind      string // "text" | "reasoning"
	Text      string
	CreatedMs int64
	UpdatedMs int64 // 部件自身的水位时间（opencode part.time_updated）
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  project_id TEXT NOT NULL DEFAULT '',
  directory TEXT NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0,
  archived_ms INTEGER NOT NULL DEFAULT 0,
  deleted_at_ms INTEGER NOT NULL DEFAULT 0,
  msg_count INTEGER NOT NULL DEFAULT 0,
  cost REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_shadow_sessions_updated ON sessions(updated_ms);
CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT '',
  agent TEXT NOT NULL DEFAULT '',
  model_json TEXT NOT NULL DEFAULT '{}',
  created_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_shadow_messages_session ON messages(session_id, created_ms);
CREATE TABLE IF NOT EXISTS parts (
  id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'text',
  text TEXT NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_shadow_parts_message ON parts(message_id, created_ms);
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`

// Open 打开（必要时创建）影子库。父目录自动创建；启用 WAL 与
// busy_timeout，写负载极小（低频增量对账），同步策略 NORMAL 足够。
// 默认路径由 internal/paths.ShadowDBPath 提供（~/.local/share/octl/shadow.db）：
// 影子库住在 octl 自己的目录而不是 opencode 的数据目录，它的职责是对抗
// opencode 世界的删除，不能跟被镜像对象住在同一间屋里。
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("shadow: mkdir: %w", err)
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("shadow: open: %w", err)
	}
	if _, err := handle.Exec(schema); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("shadow: schema: %w", err)
	}
	if err := migrate(handle); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("shadow: migrate: %w", err)
	}
	return &DB{db: handle, dbPath: path}, nil
}

// migrate 处理既有库的结构升级（CREATE TABLE IF NOT EXISTS 不会给已存在的
// 表补列）。v1 的 parts 表没有 updated_ms（部件自身水位），v2 起有。
func migrate(handle *sql.DB) error {
	rows, err := handle.Query(`PRAGMA table_info(parts)`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	hasUpdated := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == "updated_ms" {
			hasUpdated = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasUpdated {
		if _, err := handle.Exec(`ALTER TABLE parts ADD COLUMN updated_ms INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

// Close 关闭影子库。
func (d *DB) Close() error { return d.db.Close() }

// Path 返回库文件路径。
func (d *DB) Path() string { return d.dbPath }

// ---------------------------------------------------------------------------
// 写入
// ---------------------------------------------------------------------------

// UpsertSessions 批量镜像 session 行（单调：旧快照不覆盖新）。
// DO UPDATE 不碰 deleted_at_ms——并发窗口里（对账读到行 → opencode 删除 →
// 标记生效 → 对账才提交）upsert 不得复活删除标记；复活走 ResurrectIfNewer，
// 以"opencode 侧确有更新于删除时刻的活动"为准。已删除的 session 不会出现
// 在镜像结果里，其删除标记自然保留。
func (d *DB) UpsertSessions(rows []SessionRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`
INSERT INTO sessions (id, title, project_id, directory, created_ms, updated_ms, archived_ms, deleted_at_ms, msg_count, cost)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  title = excluded.title,
  project_id = excluded.project_id,
  directory = excluded.directory,
  created_ms = excluded.created_ms,
  updated_ms = excluded.updated_ms,
  archived_ms = excluded.archived_ms,
  msg_count = excluded.msg_count,
  cost = excluded.cost
WHERE excluded.updated_ms >= sessions.updated_ms`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		if _, err := stmt.Exec(r.ID, r.Title, r.ProjectID, r.Directory, r.CreatedMs, r.UpdatedMs, r.ArchivedMs, r.MsgCount, r.Cost); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResurrectIfNewer 条件复活：仅当 session 被标过删除、且镜像行的
// updated_ms 晚于删除时刻（opencode 侧在"删除"之后仍有真实活动——说明
// 当初的删除标记是误标）时清除标记。批量执行，返回复活条数。
func (d *DB) ResurrectIfNewer(rows []SessionRow) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`UPDATE sessions SET deleted_at_ms = 0 WHERE id = ? AND deleted_at_ms != 0 AND deleted_at_ms < ?`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()
	var n int64
	for _, r := range rows {
		res, err := stmt.Exec(r.ID, r.UpdatedMs)
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		n += affected
	}
	return n, tx.Commit()
}

// UpsertMessages 批量镜像消息元数据行。
func (d *DB) UpsertMessages(rows []MessageRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`
INSERT INTO messages (id, session_id, role, agent, model_json, created_ms, updated_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  session_id = excluded.session_id,
  role = excluded.role,
  agent = excluded.agent,
  model_json = excluded.model_json,
  created_ms = excluded.created_ms,
  updated_ms = excluded.updated_ms
WHERE excluded.updated_ms >= messages.updated_ms`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		if _, err := stmt.Exec(r.ID, r.SessionID, r.Role, r.Agent, r.ModelJSON, r.CreatedMs, r.UpdatedMs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertParts 批量镜像正文部件行。
func (d *DB) UpsertParts(rows []PartRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`
INSERT INTO parts (id, message_id, session_id, kind, text, created_ms, updated_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  message_id = excluded.message_id,
  session_id = excluded.session_id,
  kind = excluded.kind,
  text = excluded.text,
  created_ms = excluded.created_ms,
  updated_ms = excluded.updated_ms
WHERE excluded.updated_ms >= parts.updated_ms`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		if _, err := stmt.Exec(r.ID, r.MessageID, r.SessionID, r.Kind, r.Text, r.CreatedMs, r.UpdatedMs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RefreshMsgCounts 为给定 session 重算 msg_count（消息表行数）。
// reconcile 在消息落库后对受影响 session 调用。
func (d *DB) RefreshMsgCounts(sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`
UPDATE sessions SET msg_count = (SELECT COUNT(*) FROM messages m WHERE m.session_id = sessions.id)
WHERE sessions.id = ?`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, id := range sessionIDs {
		if _, err := stmt.Exec(id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkDeleted 把一组 session 标记为已删除（幂等：重复标记刷新时间戳，
// 以最早一次为准更符合"删除时刻"语义，故仅当当前未标记时写入）。
func (d *DB) MarkDeleted(ids []string, atMs int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`UPDATE sessions SET deleted_at_ms = ? WHERE id = ? AND deleted_at_ms = 0`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, id := range ids {
		if _, err := stmt.Exec(atMs, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PurgeSessions 真删除：从影子库彻底移除指定 session 及其消息/部件。
// 返回删除的 session 数。不存在的 id 静默跳过。
func (d *DB) PurgeSessions(ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var n int64
	for _, id := range ids {
		res, err := tx.Exec(`DELETE FROM parts WHERE session_id = ?`, id)
		if err != nil {
			return 0, err
		}
		_ = res
		if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, id); err != nil {
			return 0, err
		}
		res2, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, id)
		if err != nil {
			return 0, err
		}
		affected, _ := res2.RowsAffected()
		n += affected
	}
	return n, tx.Commit()
}

// PurgeOlderThan 保留期清理：删除最后寿命（max(updated_ms, deleted_at_ms)）
// 早于 cutoff 的 session 及其消息/部件，返回清理的 session 数。
// exclude 是"仍活在 opencode 里的 session"集合——活线即使闲置超期也不清
// （清了会让恢复使用后的镜像历史残缺）；保留期只约束档案，不管活库。
// 不留墓碑——行彻底消失（需求如此）。
func (d *DB) PurgeOlderThan(cutoffMs int64, exclude map[string]struct{}) (int64, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stale := `SELECT id FROM sessions WHERE max(updated_ms, deleted_at_ms) < ?`
	rows, err := tx.Query(stale, cutoffMs)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if _, alive := exclude[id]; alive {
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	var n int64
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM parts WHERE session_id = ?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, id); err != nil {
			return 0, err
		}
		res, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, id)
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		n += affected
	}
	return n, tx.Commit()
}

// ---------------------------------------------------------------------------
// 水位线
// ---------------------------------------------------------------------------

// GetWatermark 读取命名水位线；不存在返回 0（首次对账即全量回填）。
func (d *DB) GetWatermark(name string) int64 {
	var v string
	err := d.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, "wm:"+name).Scan(&v)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// SetWatermark 写入水位线，只增不减（防止对账部分失败后水位回退导致漏读）。
func (d *DB) SetWatermark(name string, ms int64) error {
	_, err := d.db.Exec(`
INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
WHERE CAST(meta.value AS INTEGER) < CAST(excluded.value AS INTEGER)`,
		"wm:"+name, strconv.FormatInt(ms, 10))
	return err
}

// GetMeta/SetMeta 通用元数据读写（保留期上次执行时间等）。
func (d *DB) GetMeta(key string) (string, bool) {
	var v string
	err := d.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	return v, err == nil
}

func (d *DB) SetMeta(key, value string) error {
	_, err := d.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---------------------------------------------------------------------------
// 读取（语义与 internal/db 对应查询保持一致，保证消费方无感切换）
// ---------------------------------------------------------------------------

// ListSessions 返回全部 session（含已删除），按创建时间降序。
func (d *DB) ListSessions() ([]SessionRow, error) {
	rows, err := d.db.Query(`
SELECT id, title, project_id, directory, created_ms, updated_ms, archived_ms, deleted_at_ms, msg_count, cost
FROM sessions ORDER BY created_ms DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.ID, &r.Title, &r.ProjectID, &r.Directory, &r.CreatedMs, &r.UpdatedMs, &r.ArchivedMs, &r.DeletedAtMs, &r.MsgCount, &r.Cost); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HasSession 报告 session 行是否存在（无论是否已删）。
func (d *DB) HasSession(id string) (bool, error) {
	var one int
	err := d.db.QueryRow(`SELECT 1 FROM sessions WHERE id = ?`, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SessionMessages 返回 session 的全部部件，形状与 db.GetSessionMessages
// 一致（MessagePart 列表，按部件时间升序）。影子库不含 tool 部件，
// 因此比原查询少这些空文本行，分组渲染结果不变。
func (d *DB) SessionMessages(sessionID string) ([]types.MessagePart, error) {
	rows, err := d.db.Query(`
SELECT p.id, p.message_id, p.session_id, m.role, m.agent, m.model_json, p.text, p.created_ms
FROM parts p JOIN messages m ON m.id = p.message_id
WHERE p.session_id = ?
ORDER BY p.created_ms ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []types.MessagePart
	for rows.Next() {
		var p types.MessagePart
		if err := rows.Scan(&p.PartID, &p.MessageID, &p.SessionID, &p.Role, &p.Agent, &p.ModelInfo, &p.Text, &p.TimeCreated); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MessageActivity 与 db.GetMessageActivity 同义：窗口内消息行数与首末时刻。
func (d *DB) MessageActivity(from, to int64) ([]types.MessageActivity, error) {
	rows, err := d.db.Query(`
SELECT session_id, COUNT(*), COALESCE(MIN(created_ms), 0), COALESCE(MAX(created_ms), 0)
FROM messages WHERE created_ms >= ? AND created_ms < ?
GROUP BY session_id`, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []types.MessageActivity
	for rows.Next() {
		var a types.MessageActivity
		if err := rows.Scan(&a.SessionID, &a.Count, &a.FirstAt, &a.LastAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// 摘录与骨架的截断参数：与 internal/db 完全一致，保证日报/底片输出
// 逐字节兼容（存储层存全文，截断只发生在读取层）。
const (
	excerptPartLimit  = 620
	skeletonPartLimit = 1000
	skeletonEntryRune = 2000
)

// SessionExcerpts 与 db.GetSessionExcerpts 同义：
// firstUser = 窗口 [from,to) 内首条用户消息的 text 部件拼接；
// lastAssistant = time_created < to 的最新 assistant 消息（不限 from）。
func (d *DB) SessionExcerpts(sessionID string, from, to int64) (firstUser, lastAssistant string, err error) {
	firstUser, err = d.excerptOfRole(sessionID, "user", from, to, true)
	if err != nil {
		return "", "", err
	}
	lastAssistant, err = d.excerptOfRole(sessionID, "assistant", 0, to, false)
	if err != nil {
		return "", "", err
	}
	return firstUser, lastAssistant, nil
}

func (d *DB) excerptOfRole(sessionID, role string, from, to int64, wantFirst bool) (string, error) {
	order := "DESC"
	timeCond := `created_ms < ?`
	args := []interface{}{sessionID, role, to}
	if wantFirst {
		order = "ASC"
		timeCond = `created_ms >= ? AND created_ms < ?`
		args = []interface{}{sessionID, role, from, to}
	}
	var msgID string
	findMsg := `
SELECT id FROM messages WHERE session_id = ? AND role = ? AND ` + timeCond + `
ORDER BY created_ms ` + order + ` LIMIT 1`
	err := d.db.QueryRow(findMsg, args...).Scan(&msgID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	rows, err := d.db.Query(`
SELECT COALESCE(substr(text, 1, ?), '')
FROM parts WHERE message_id = ? AND kind = 'text' AND text != ''
ORDER BY created_ms ASC LIMIT 8`, excerptPartLimit, msgID)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var texts []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return "", err
		}
		texts = append(texts, t)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(texts, "\n"), nil
}

// UserSkeleton 与 db.GetUserSkeleton 同义：窗口内全部用户消息骨架，
// 每条消息的 text 部件按时间序拼接；单部件截 1000 字符（codepoint），
// 单条消息拼接后截 2000 rune 加省略号；无 text 部件的消息跳过。
func (d *DB) UserSkeleton(sessionID string, from, to int64) ([]types.SkeletonEntry, error) {
	rows, err := d.db.Query(`
SELECT m.id, m.created_ms, COALESCE(substr(p.text, 1, ?), '')
FROM messages m
LEFT JOIN parts p ON p.message_id = m.id AND p.kind = 'text' AND p.text != ''
WHERE m.session_id = ? AND m.role = 'user' AND m.created_ms >= ? AND m.created_ms < ?
ORDER BY m.created_ms ASC, p.created_ms ASC`, skeletonPartLimit, sessionID, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []types.SkeletonEntry
	var curID string
	var curTime int64
	var curText []string
	flush := func() {
		if curID == "" || len(curText) == 0 {
			return
		}
		text := strings.Join(curText, "\n")
		if runes := []rune(text); len(runes) > skeletonEntryRune {
			text = string(runes[:skeletonEntryRune]) + "…"
		}
		out = append(out, types.SkeletonEntry{TimeMs: curTime, Text: text})
	}
	for rows.Next() {
		var id string
		var t int64
		var text sql.NullString
		if err := rows.Scan(&id, &t, &text); err != nil {
			return nil, err
		}
		if id != curID {
			flush()
			curID, curTime, curText = id, t, nil
		}
		if text.Valid && text.String != "" {
			curText = append(curText, text.String)
		}
	}
	flush()
	return out, rows.Err()
}
