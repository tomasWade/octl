// Package db provides database access for the opencode session manager.
package db

import (
	"database/sql"
	"errors"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/tomasWade/octl/internal/types"
)

// ErrSessionNotFound 在数据库中未找到会话时返回。
var ErrSessionNotFound = errors.New("session not found")

// DB 表示用于会话数据的数据库连接。
type DB struct {
	db     *sql.DB
	dbPath string
}

// New 创建一个新的数据库连接。
// dbPath 应为 SQLite 数据库文件的路径。
// 以只读模式打开数据库以确保安全查询。
func New(dbPath string) (*DB, error) {
	// 以只读模式打开以确保安全的并发访问。
	// 注意：不要显式指定 _journal_mode=wal。opencode 可能以不同模式/锁
	// 持有数据库，WAL 读者会与之冲突并产生 disk I/O error (522)。
	// 使用最简单的 mode=ro 让 SQLite 自行决定如何读取。
	dsn := dbPath + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	return &DB{db: db, dbPath: dbPath}, nil
}

// Close 关闭数据库连接。
func (d *DB) Close() error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// ListSessions 从数据库返回所有会话，按创建时间降序排列。
func (d *DB) ListSessions() ([]types.Session, error) {
	query := `
SELECT s.id, COALESCE(s.project_id, '') as project_id, COALESCE(s.parent_id, '') as parent_id,
       COALESCE(s.slug, '') as slug, COALESCE(s.directory, '') as directory,
       COALESCE(s.title, '') as title, COALESCE(s.version, '') as version,
       COALESCE(s.agent, '') as agent, COALESCE(s.model, '') as model,
       s.cost, s.tokens_input, s.tokens_output, s.tokens_reasoning,
       s.tokens_cache_read, s.tokens_cache_write,
       COALESCE(s.time_created, 0) as time_created,
       COALESCE(s.time_updated, 0) as time_updated,
       COALESCE(s.time_compacting, 0) as time_compacting,
       COALESCE(s.time_archived, 0) as time_archived,
       COALESCE(s.path, '') as path, COALESCE(s.workspace_id, '') as workspace_id,
       COALESCE(s.summary_additions, 0), COALESCE(s.summary_deletions, 0),
       COALESCE(s.summary_files, 0), COALESCE(s.summary_diffs, ''),
        (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id) as msg_count
FROM session s
ORDER BY s.time_created DESC`

	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var sessions []types.Session
	for rows.Next() {
		var s types.Session
		err := rows.Scan(
			&s.ID, &s.ProjectID, &s.ParentID, &s.Slug, &s.Directory, &s.Title, &s.Version,
			&s.Agent, &s.Model,
			&s.Cost, &s.TokensInput, &s.TokensOutput, &s.TokensReasoning,
			&s.TokensCacheRead, &s.TokensCacheWrite,
			&s.TimeCreated, &s.TimeUpdated, &s.TimeCompacting, &s.TimeArchived,
			&s.Path, &s.WorkspaceID,
			&s.SummaryAdditions, &s.SummaryDeletions, &s.SummaryFiles, &s.SummaryDiffs,
			&s.MessageCount,
		)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return sessions, nil
}

// GetSession 根据 ID 返回单个会话。
func (d *DB) GetSession(id string) (*types.Session, error) {
	query := `
SELECT s.id, COALESCE(s.project_id, '') as project_id, COALESCE(s.parent_id, '') as parent_id,
       COALESCE(s.slug, '') as slug, COALESCE(s.directory, '') as directory,
       COALESCE(s.title, '') as title, COALESCE(s.version, '') as version,
       COALESCE(s.agent, '') as agent, COALESCE(s.model, '') as model,
       s.cost, s.tokens_input, s.tokens_output, s.tokens_reasoning,
       s.tokens_cache_read, s.tokens_cache_write,
       COALESCE(s.time_created, 0) as time_created,
       COALESCE(s.time_updated, 0) as time_updated,
       COALESCE(s.time_compacting, 0) as time_compacting,
       COALESCE(s.time_archived, 0) as time_archived,
       COALESCE(s.path, '') as path, COALESCE(s.workspace_id, '') as workspace_id,
       COALESCE(s.summary_additions, 0), COALESCE(s.summary_deletions, 0),
       COALESCE(s.summary_files, 0), COALESCE(s.summary_diffs, ''),
       (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id) as msg_count
FROM session s
WHERE s.id = ?`

	var s types.Session
	err := d.db.QueryRow(query, id).Scan(
		&s.ID, &s.ProjectID, &s.ParentID, &s.Slug, &s.Directory, &s.Title, &s.Version,
		&s.Agent, &s.Model,
		&s.Cost, &s.TokensInput, &s.TokensOutput, &s.TokensReasoning,
		&s.TokensCacheRead, &s.TokensCacheWrite,
		&s.TimeCreated, &s.TimeUpdated, &s.TimeCompacting, &s.TimeArchived,
		&s.Path, &s.WorkspaceID,
		&s.SummaryAdditions, &s.SummaryDeletions, &s.SummaryFiles, &s.SummaryDiffs,
		&s.MessageCount,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}

	return &s, nil
}

// GetSessionStats 返回关于会话的聚合统计信息。
func (d *DB) GetSessionStats() (*types.SessionStats, error) {
	query := `
SELECT
  COUNT(*) as total_sessions,
  COALESCE(SUM(CASE WHEN COALESCE(time_archived, 0) = 0 THEN 1 ELSE 0 END), 0) as active_sessions,
  COALESCE(SUM(cost), 0) as total_cost,
  COALESCE(SUM(tokens_input), 0) as total_tokens_input,
  COALESCE(SUM(tokens_output), 0) as total_tokens_output,
  COALESCE(SUM(tokens_reasoning), 0) as total_tokens_reasoning,
  COALESCE(SUM(tokens_cache_read), 0) as total_tokens_cache_read
FROM session`

	var stats types.SessionStats
	err := d.db.QueryRow(query).Scan(
		&stats.TotalSessions,
		&stats.ActiveSessions,
		&stats.TotalCost,
		&stats.TotalTokensInput,
		&stats.TotalTokensOutput,
		&stats.TotalTokensReasoning,
		&stats.TotalTokensCacheRead,
	)
	if err != nil {
		return nil, err
	}

	return &stats, nil
}

// GetModelUsage 返回按模型分组的用量统计信息。
func (d *DB) GetModelUsage() ([]types.ModelUsage, error) {
	query := `
SELECT
  COALESCE(json_extract(model, '$.id'), 'unknown') as model_id,
  COALESCE(json_extract(model, '$.providerID'), 'unknown') as provider_id,
  COALESCE(SUM(tokens_input + tokens_output), 0) as token_count,
  COALESCE(SUM(cost), 0) as cost
FROM session
WHERE model IS NOT NULL AND model != ''
GROUP BY json_extract(model, '$.id'), json_extract(model, '$.providerID')
ORDER BY cost DESC`

	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var usages []types.ModelUsage
	for rows.Next() {
		var u types.ModelUsage
		err := rows.Scan(
			&u.ModelID,
			&u.ProviderID,
			&u.TokenCount,
			&u.Cost,
		)
		if err != nil {
			return nil, err
		}
		usages = append(usages, u)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return usages, nil
}

// GetAllProjects 从数据库返回所有项目。
func (d *DB) GetAllProjects() ([]types.Project, error) {
	query := `SELECT id, worktree, COALESCE(vcs, '') as vcs, COALESCE(name, '') as name,
		time_created, time_updated FROM project ORDER BY 
		CASE WHEN id = 'global' THEN 0 ELSE 1 END, time_created DESC`

	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var projects []types.Project
	for rows.Next() {
		var p types.Project
		err := rows.Scan(&p.ID, &p.Worktree, &p.Vcs, &p.Name, &p.TimeCreated, &p.TimeUpdated)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return projects, nil
}

// NewWritable 打开一个用于管理操作的读写连接。
func (d *DB) NewWritable() (*sql.DB, error) {
	return sql.Open("sqlite", d.dbPath)
}

// GetSessionMessages 返回给定会话的所有消息部分，
// 按创建时间升序排列。
func (d *DB) GetSessionMessages(sessionID string) ([]types.MessagePart, error) {
	query := `
SELECT p.id, p.message_id, p.session_id,
       COALESCE(json_extract(m.data, '$.role'), '') as role,
       COALESCE(json_extract(m.data, '$.agent'), '') as agent,
       COALESCE(json_extract(m.data, '$.model'), '{}') as model_info,
       COALESCE(json_extract(p.data, '$.text'), '') as text,
       p.time_created
FROM part p
JOIN message m ON m.id = p.message_id
WHERE p.session_id = ?
ORDER BY p.time_created ASC`

	rows, err := d.db.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var parts []types.MessagePart
	for rows.Next() {
		var p types.MessagePart
		err := rows.Scan(
			&p.PartID,
			&p.MessageID,
			&p.SessionID,
			&p.Role,
			&p.Agent,
			&p.ModelInfo,
			&p.Text,
			&p.TimeCreated,
		)
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return parts, nil
}

// GetLastMessageRoleCompleted 返回给定会话中最新消息的角色，
// 以及该消息的完成时间戳（data JSON 中 time.completed，unix 毫秒）。
// completedMs 为 0 表示消息尚未完成（例如 assistant 消息仍在生成中）。
// 如果会话中没有消息，返回空角色、0 和 nil。
func (d *DB) GetLastMessageRoleCompleted(sessionID string) (role string, completedMs int64, err error) {
	query := `
SELECT COALESCE(json_extract(m.data, '$.role'), ''),
       COALESCE(json_extract(m.data, '$.time.completed'), 0)
FROM message m
WHERE m.session_id = ?
ORDER BY m.time_created DESC
LIMIT 1`

	err = d.db.QueryRow(query, sessionID).Scan(&role, &completedMs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, nil
		}
		return "", 0, err
	}
	return role, completedMs, nil
}

// GetMessageActivity 返回时间窗口 [from, to) 内按 session 聚合的消息活动：
// 每个有消息的 session 的消息数与首末消息时间（unix 毫秒）。
// 结果无特定顺序，调用方自行排序。
func (d *DB) GetMessageActivity(from, to int64) ([]types.MessageActivity, error) {
	query := `
SELECT m.session_id, COUNT(*), COALESCE(MIN(m.time_created), 0), COALESCE(MAX(m.time_created), 0)
FROM message m
WHERE m.time_created >= ? AND m.time_created < ?
GROUP BY m.session_id`

	rows, err := d.db.Query(query, from, to)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetSessionExcerpts 返回日报摘要素材：
//   - firstUser：窗口 [from, to) 内首条用户消息的文本开头（各 text part
//     顺序拼接，每个 part 在 SQL 层截到 excerptPartLimit 字符）
//   - lastAssistant：time_created < to 的最新一条 assistant 消息的文本开头
//
// 找不到对应消息时对应返回值为空字符串，不是错误。截断到最终长度由
// 调用方（daemon 层）按 rune 上限完成。
func (d *DB) GetSessionExcerpts(sessionID string, from, to int64) (firstUser, lastAssistant string, err error) {
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

// excerptPartLimit 是单个 text part 在 SQL 层预截断的字符数（sqlite substr
// 按 codepoint 计数），略高于 daemon 层的 rune 上限，留出拼接余量。
const excerptPartLimit = 620

// excerptOfRole 取指定 role 的一条消息（首条用户消息按窗口正序、末条
// assistant 消息按截止时间倒序）的全部 text part 并按时间顺序拼接。
// wantFirst=true 时消息必须在 [from, to) 内（取最早一条）；
// wantFirst=false 时消息只需早于 to（取最新一条）。
func (d *DB) excerptOfRole(sessionID, role string, from, to int64, wantFirst bool) (string, error) {
	order := "DESC"
	timeCond := "m.time_created < ?"
	args := []interface{}{sessionID, role, to}
	if wantFirst {
		order = "ASC"
		timeCond = "m.time_created >= ? AND m.time_created < ?"
		args = []interface{}{sessionID, role, from, to}
	}

	var msgID string
	findMsg := `
SELECT m.id
FROM message m
WHERE m.session_id = ? AND COALESCE(json_extract(m.data, '$.role'), '') = ? AND ` + timeCond + `
ORDER BY m.time_created ` + order + ` LIMIT 1`
	err := d.db.QueryRow(findMsg, args...).Scan(&msgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}

	partQuery := `
SELECT COALESCE(substr(COALESCE(json_extract(p.data, '$.text'), ''), 1, ?), '')
FROM part p
WHERE p.message_id = ? AND COALESCE(json_extract(p.data, '$.type'), '') = 'text'
  AND COALESCE(json_extract(p.data, '$.text'), '') != ''
ORDER BY p.time_created ASC
LIMIT 8`
	rows, err := d.db.Query(partQuery, excerptPartLimit, msgID)
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

// skeletonPartLimit 是骨架查询中单个 text part 在 SQL 层预截断的字符数
// （sqlite substr 按 codepoint 计数）。骨架是底片/讣告的存储层，上限比
// 摘录宽松：用户消息罕见超过千字符，超长粘贴截断即可。
const skeletonPartLimit = 1000

// skeletonEntryLimit 单条消息拼接后的 rune 上限（多 text part 防御）。
const skeletonEntryLimit = 2000

// GetUserSkeleton 取 session 在 [from, to) 窗口内全部用户消息的骨架
// （每条消息的全部 text part 按时间序拼接）。from=0 表示全史（to 传
// math.MaxInt64 或未来时刻）。无 text part 的消息（纯附件等）跳过。
// 排序按消息时间升序；同消息的 part 按 part 时间升序拼接。
func (d *DB) GetUserSkeleton(sessionID string, from, to int64) ([]types.SkeletonEntry, error) {
	query := `
SELECT m.id, m.time_created,
       COALESCE(substr(COALESCE(json_extract(p.data, '$.text'), ''), 1, ?), '')
FROM message m
LEFT JOIN part p ON p.message_id = m.id
  AND COALESCE(json_extract(p.data, '$.type'), '') = 'text'
  AND COALESCE(json_extract(p.data, '$.text'), '') != ''
WHERE m.session_id = ? AND COALESCE(json_extract(m.data, '$.role'), '') = 'user'
  AND m.time_created >= ? AND m.time_created < ?
ORDER BY m.time_created ASC, p.time_created ASC`
	rows, err := d.db.Query(query, skeletonPartLimit, sessionID, from, to)
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
		if runes := []rune(text); len(runes) > skeletonEntryLimit {
			text = string(runes[:skeletonEntryLimit]) + "…"
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

// ---------------------------------------------------------------------------
// 影子库增量对账查询（watermark 增量拉取；配合 internal/shadow 使用）
// ---------------------------------------------------------------------------

// ShadowSessionRow 是对账用的 session 投影：影子库需要的字段子集。
type ShadowSessionRow struct {
	ID          string
	Title       string
	ProjectID   string
	Directory   string
	CreatedMs   int64
	UpdatedMs   int64
	ArchivedMs  int64
	MsgCount    int64
	Cost        float64
}

// ShadowMessageRow 是对账用的 message 投影：消息元数据（正文部件另取）。
type ShadowMessageRow struct {
	ID        string
	SessionID string
	Role      string
	Agent     string
	ModelJSON string
	CreatedMs int64
	UpdatedMs int64
}

// ShadowPartRow 是对账用的 part 投影：只含 text / reasoning 部件。
type ShadowPartRow struct {
	ID        string
	MessageID string
	SessionID string
	Kind      string
	Text      string
	CreatedMs int64
	UpdatedMs int64
}

// sessionsUpdatedSince 按 time_updated 水位增量读取 session 投影。
// watermark=0 时全量（首次即回填）。time_updated 可能同毫秒多行，
// 用 >= 配合幂等 upsert，重叠重放无害。
func (d *DB) sessionsUpdatedSince(watermark int64) ([]ShadowSessionRow, error) {
	rows, err := d.db.Query(`
SELECT s.id, COALESCE(s.title, ''), COALESCE(s.project_id, ''), COALESCE(s.directory, ''),
       COALESCE(s.time_created, 0), COALESCE(s.time_updated, 0), COALESCE(s.time_archived, 0),
       s.cost,
       (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id)
FROM session s WHERE COALESCE(s.time_updated, 0) >= ?
ORDER BY s.time_updated ASC`, watermark)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ShadowSessionRow
	for rows.Next() {
		var r ShadowSessionRow
		if err := rows.Scan(&r.ID, &r.Title, &r.ProjectID, &r.Directory, &r.CreatedMs, &r.UpdatedMs, &r.ArchivedMs, &r.Cost, &r.MsgCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionsUpdatedSince 导出 sessionsUpdatedSince（daemon 对账入口）。
func (d *DB) SessionsUpdatedSince(watermark int64) ([]ShadowSessionRow, error) {
	return d.sessionsUpdatedSince(watermark)
}

// messagesUpdatedSince 按 message.time_updated 水位增量读取消息元数据。
// 流式生成中消息行会持续更新 time_updated，完成态在下一轮对账自然覆盖。
func (d *DB) messagesUpdatedSince(watermark int64) ([]ShadowMessageRow, error) {
	rows, err := d.db.Query(`
SELECT m.id, m.session_id,
       COALESCE(json_extract(m.data, '$.role'), ''),
       COALESCE(json_extract(m.data, '$.agent'), ''),
       COALESCE(json_extract(m.data, '$.model'), '{}'),
       COALESCE(m.time_created, 0), COALESCE(m.time_updated, 0)
FROM message m WHERE COALESCE(m.time_updated, 0) >= ?
ORDER BY m.time_updated ASC`, watermark)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ShadowMessageRow
	for rows.Next() {
		var r ShadowMessageRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Role, &r.Agent, &r.ModelJSON, &r.CreatedMs, &r.UpdatedMs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MessagesUpdatedSince 导出 messagesUpdatedSince。
func (d *DB) MessagesUpdatedSince(watermark int64) ([]ShadowMessageRow, error) {
	return d.messagesUpdatedSince(watermark)
}

// ShadowPartWithMessage 是部件水位扫描的结果：部件行 + 其父消息元数据
// （opencode 里 part.time_updated 可能晚于 message.time_updated——流式
// 收尾时部件还会更新而消息行不再 bump；部件必须按自己的水位扫，
// 否则消息水位越过后迟到的部件永远不可见）。
type ShadowPartWithMessage struct {
	Part    ShadowPartRow
	Message ShadowMessageRow
}

// PartsUpdatedSince 按 part.time_updated 水位增量读取 text / reasoning
// 部件及其父消息元数据。opencode 的 part 表无 time_updated 列时返回
// 空结果与 nil（旧 schema 兼容：该扫描退化为不生效，部件仍由消息水位
// 路径覆盖）。
func (d *DB) PartsUpdatedSince(watermark int64) ([]ShadowPartWithMessage, error) {
	hasCol, err := d.partHasTimeUpdated()
	if err != nil || !hasCol {
		return nil, err
	}
	rows, err := d.db.Query(`
SELECT p.id, p.message_id, COALESCE(p.session_id, ''),
       COALESCE(json_extract(p.data, '$.type'), ''),
       COALESCE(json_extract(p.data, '$.text'), ''),
       COALESCE(p.time_created, 0), COALESCE(p.time_updated, 0),
       m.id, m.session_id,
       COALESCE(json_extract(m.data, '$.role'), ''),
       COALESCE(json_extract(m.data, '$.agent'), ''),
       COALESCE(json_extract(m.data, '$.model'), '{}'),
       COALESCE(m.time_created, 0), COALESCE(m.time_updated, 0)
FROM part p JOIN message m ON m.id = p.message_id
WHERE COALESCE(p.time_updated, 0) >= ?
  AND COALESCE(json_extract(p.data, '$.type'), '') IN ('text', 'reasoning')
  AND COALESCE(json_extract(p.data, '$.text'), '') != ''
ORDER BY COALESCE(p.time_updated, 0) ASC`, watermark)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ShadowPartWithMessage
	for rows.Next() {
		var r ShadowPartWithMessage
		if err := rows.Scan(
			&r.Part.ID, &r.Part.MessageID, &r.Part.SessionID, &r.Part.Kind, &r.Part.Text, &r.Part.CreatedMs, &r.Part.UpdatedMs,
			&r.Message.ID, &r.Message.SessionID, &r.Message.Role, &r.Message.Agent, &r.Message.ModelJSON, &r.Message.CreatedMs, &r.Message.UpdatedMs,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// partHasTimeUpdated 检查 part 表是否有 time_updated 列（真实库有；
// 极旧 schema 没有——该扫描退化为不生效，部件仍由消息水位路径覆盖）。
// 每次实查 PRAGMA：成本可忽略，且避免跨库实例的缓存污染。
func (d *DB) partHasTimeUpdated() (bool, error) {
	rows, err := d.db.Query(`PRAGMA table_info(part)`)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	has := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == "time_updated" {
			has = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return has, nil
}

// chunkSize 控制 IN (...) 列表长度，避开 SQLite 变量数上限。
const chunkSize = 500

// TextPartsOfMessages 读取给定消息的 text / reasoning 部件（按部件时间升序）。
// tool / step-start 等部件不进影子库。
func (d *DB) TextPartsOfMessages(messageIDs []string) ([]ShadowPartRow, error) {
	var out []ShadowPartRow
	for len(messageIDs) > 0 {
		chunk := messageIDs
		if len(chunk) > chunkSize {
			chunk = chunk[:chunkSize]
		}
		messageIDs = messageIDs[len(chunk):]

		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := d.db.Query(`
SELECT p.id, p.message_id, COALESCE(p.session_id, ''),
       COALESCE(json_extract(p.data, '$.type'), ''),
       COALESCE(json_extract(p.data, '$.text'), ''),
       COALESCE(p.time_created, 0), COALESCE(p.time_updated, 0)
FROM part p
WHERE p.message_id IN (`+placeholders+`)
  AND COALESCE(json_extract(p.data, '$.type'), '') IN ('text', 'reasoning')
  AND COALESCE(json_extract(p.data, '$.text'), '') != ''
ORDER BY p.time_created ASC`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r ShadowPartRow
			if err := rows.Scan(&r.ID, &r.MessageID, &r.SessionID, &r.Kind, &r.Text, &r.CreatedMs, &r.UpdatedMs); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// SessionsByIDs 读取指定 session 投影（定向对账用；顺序不定）。
// ids 分块查询；不存在的 id 静默跳过。
func (d *DB) SessionsByIDs(ids []string) ([]ShadowSessionRow, error) {
	var out []ShadowSessionRow
	for len(ids) > 0 {
		chunk := ids
		if len(chunk) > chunkSize {
			chunk = chunk[:chunkSize]
		}
		ids = ids[len(chunk):]

		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := d.db.Query(`
SELECT s.id, COALESCE(s.title, ''), COALESCE(s.project_id, ''), COALESCE(s.directory, ''),
       COALESCE(s.time_created, 0), COALESCE(s.time_updated, 0), COALESCE(s.time_archived, 0),
       s.cost,
       (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id)
FROM session s WHERE s.id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r ShadowSessionRow
			if err := rows.Scan(&r.ID, &r.Title, &r.ProjectID, &r.Directory, &r.CreatedMs, &r.UpdatedMs, &r.ArchivedMs, &r.Cost, &r.MsgCount); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// MessagesOfSessions 读取指定 session 的全部消息元数据（定向对账用，
// 用于删除前最后一搏：session 行可能已消失，但若还在则连消息一起抢救）。
func (d *DB) MessagesOfSessions(sessionIDs []string) ([]ShadowMessageRow, error) {
	var out []ShadowMessageRow
	for len(sessionIDs) > 0 {
		chunk := sessionIDs
		if len(chunk) > chunkSize {
			chunk = chunk[:chunkSize]
		}
		sessionIDs = sessionIDs[len(chunk):]

		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := d.db.Query(`
SELECT m.id, m.session_id,
       COALESCE(json_extract(m.data, '$.role'), ''),
       COALESCE(json_extract(m.data, '$.agent'), ''),
       COALESCE(json_extract(m.data, '$.model'), '{}'),
       COALESCE(m.time_created, 0), COALESCE(m.time_updated, 0)
FROM message m WHERE m.session_id IN (`+placeholders+`)
ORDER BY m.time_created ASC`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r ShadowMessageRow
			if err := rows.Scan(&r.ID, &r.SessionID, &r.Role, &r.Agent, &r.ModelJSON, &r.CreatedMs, &r.UpdatedMs); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}
