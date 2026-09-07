// shadow_test.go 覆盖影子库在 daemon 内的集成行为：全量回填与增量对账、
// 定向对账、tombstone 确认后标记删除、日报纳入被删线、messages 影子库
// 优先与回落、purge action、保留期清理。
package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tomasWade/octl/internal/db"
	"github.com/tomasWade/octl/internal/manage"
	"github.com/tomasWade/octl/internal/shadow"
)

// setupShadowEnv 搭建完整的影子库测试环境：可写的 opencode 模拟库
// （夹具 schema + seed）、只读 db.DB、独立 shadow.DB、接入影子库的
// StateManager。wdb 保持打开以便测试中途变更数据。
func setupShadowEnv(t *testing.T, seed func(wdb *sql.DB)) (*StateManager, *sql.DB, *shadow.DB) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "oc.db")

	wdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open write DB: %v", err)
	}
	if _, err := wdb.Exec(daemonTestSchema); err != nil {
		wdb.Close()
		t.Fatalf("schema: %v", err)
	}
	if seed != nil {
		seed(wdb)
	}

	d, err := db.New(dbPath)
	if err != nil {
		wdb.Close()
		t.Fatalf("db.New: %v", err)
	}
	sdb, err := shadow.Open(filepath.Join(dir, "shadow.db"))
	if err != nil {
		wdb.Close()
		d.Close()
		t.Fatalf("shadow.Open: %v", err)
	}
	sm := NewStateManager(d)
	sm.SetShadow(sdb, 0)

	t.Cleanup(func() {
		_ = wdb.Close()
		_ = d.Close()
		_ = sdb.Close()
	})
	return sm, wdb, sdb
}

// insertMessageFull 插入带 time_updated 的 message + 可选部件。
// 部件自身 time_updated 默认从 updated 起步；要模拟"部件晚于消息行更新"
// 时用 latePartMs（>0 则覆盖部件时间戳）。
func insertMessageFull(wdb *sql.DB, id, sessionID, role string, created, updated int64, parts map[string]string) {
	insertMessageFullLate(wdb, id, sessionID, role, created, updated, parts, 0)
}

func insertMessageFullLate(wdb *sql.DB, id, sessionID, role string, created, updated int64, parts map[string]string, latePartMs int64) {
	wdb.Exec(`INSERT INTO message (id, session_id, data, time_created, time_updated)
		VALUES (?, ?, ?, ?, ?)`, id, sessionID, `{"role":"`+role+`"}`, created, updated)
	i := 0
	for kind, text := range parts {
		partUpdated := updated + int64(i)
		if latePartMs > 0 {
			partUpdated = latePartMs + int64(i)
		}
		wdb.Exec(`INSERT INTO part (id, message_id, session_id, data, time_created, time_updated)
			VALUES (?, ?, ?, ?, ?, ?)`,
			id+"-p"+string(rune('a'+i)), id, sessionID,
			`{"type":"`+kind+`","text":`+mustJSONString(text)+`}`, created+int64(i), partUpdated)
		i++
	}
}

// TestShadowReconcile_BackfillAndIncremental 首轮对账即全量回填（水位 0）；
// tool 部件不入库；第二轮对账按水位只拉增量。
func TestShadowReconcile_BackfillAndIncremental(t *testing.T) {
	sm, wdb, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('p', '/w', '', 'P', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "p", "主线", 1000, 5000, 0)
		insertMessageFull(wdb, "m1", "s1", "user", 1100, 1200, map[string]string{"text": "用户提问"})
		insertMessageFull(wdb, "m2", "s1", "assistant", 1300, 1400, map[string]string{
			"reasoning": "思考过程",
			"text":      "回答正文",
			"tool":      "{\"command\":\"ls -la\"}",
		})
	})

	if err := sm.shadowReconcile(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	sessions, err := sdb.ListSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("shadow sessions = %v %v", sessions, err)
	}
	if sessions[0].Title != "主线" || sessions[0].DeletedAtMs != 0 || sessions[0].MsgCount != 2 {
		t.Errorf("session row = %+v", sessions[0])
	}

	parts, err := sdb.SessionMessages("s1")
	if err != nil {
		t.Fatal(err)
	}
	// text + reasoning 共 3 个部件；tool 部件不进影子库。
	if len(parts) != 3 {
		t.Fatalf("shadow parts = %d, want 3（tool 不入档）", len(parts))
	}
	joined := parts[0].Text + parts[1].Text + parts[2].Text
	for _, want := range []string{"用户提问", "思考过程", "回答正文"} {
		if !bytes.Contains([]byte(joined), []byte(want)) {
			t.Errorf("影子库缺 %q", want)
		}
	}
	if bytes.Contains([]byte(joined), []byte("ls -la")) {
		t.Errorf("tool 输出不应入档")
	}
	wm := sdb.GetWatermark("message")
	if wm < 1400 {
		t.Errorf("message watermark = %d, want >= 1400", wm)
	}

	// 增量：新消息 + 一个仅更新过的旧消息。
	insertMessageFull(wdb, "m3", "s1", "user", 6000, 6100, map[string]string{"text": "追加问题"})
	wdb.Exec(`UPDATE message SET time_updated = 6200 WHERE id = 'm1'`)
	// 迟到部件（review #2 的回归用例）：消息行 time_updated 停在低位
	// （消息水位越过它 → 消息扫描路径不可见），但部件自身更新在高位——
	// 部件水位路径必须把它捞回来。
	insertMessageFullLate(wdb, "m4", "s1", "assistant", 1000, 1000, map[string]string{"text": "迟到部件正文"}, 6300)
	if err := sm.shadowReconcile(); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	sessions, _ = sdb.ListSessions()
	if sessions[0].MsgCount != 4 {
		t.Errorf("msg_count = %d, want 4", sessions[0].MsgCount)
	}
	parts, _ = sdb.SessionMessages("s1")
	if len(parts) != 5 {
		t.Errorf("parts = %d, want 5（含迟到部件）", len(parts))
	}
	found := false
	for _, p := range parts {
		if p.Text == "迟到部件正文" {
			found = true
		}
	}
	if !found {
		t.Errorf("迟到部件未被部件水位路径捕获")
	}
	if sdb.GetWatermark("message") < 6200 {
		t.Errorf("watermark should advance to >= 6200, got %d", sdb.GetWatermark("message"))
	}
	if sdb.GetWatermark("part") < 6300 {
		t.Errorf("part watermark should advance to >= 6300, got %d", sdb.GetWatermark("part"))
	}
}

// TestShadowReconcileSessions_Targeted 定向对账只动目标 session。
func TestShadowReconcileSessions_Targeted(t *testing.T) {
	sm, _, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		dailyInsertSession(wdb, "s1", "p", "一", 1000, 2000, 0)
		dailyInsertSession(wdb, "s2", "p", "二", 1000, 2000, 0)
		insertMessageFull(wdb, "m1", "s1", "user", 1100, 1200, map[string]string{"text": "s1 消息"})
	})

	sm.shadowReconcileSessions([]string{"s1"})
	if has, _ := sdb.HasSession("s1"); !has {
		t.Fatalf("s1 应已入档")
	}
	if has, _ := sdb.HasSession("s2"); has {
		t.Errorf("s2 不应被定向对账")
	}
	parts, _ := sdb.SessionMessages("s1")
	if len(parts) != 1 || parts[0].Text != "s1 消息" {
		t.Errorf("s1 parts = %+v", parts)
	}
}

// TestSyncFromDB_TombstoneMarksShadow 外部删除（opencode DB 行消失）经
// 两个 sync 周期确认后标记影子库 deleted_at。
func TestSyncFromDB_TombstoneMarksShadow(t *testing.T) {
	sm, wdb, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		dailyInsertSession(wdb, "s1", "p", "将被外部删除", 1000, 2000, 0)
	})

	// 初始同步：stateMap 认识 s1；影子库回填。
	if err := sm.syncFromDB(); err != nil {
		t.Fatal(err)
	}
	sm.shadowReconcileNow()
	if has, _ := sdb.HasSession("s1"); !has {
		t.Fatalf("s1 应回填入影子库")
	}

	// 外部删除：opencode DB 行消失。
	wdb.Exec(`DELETE FROM session WHERE id = 's1'`)

	// 第一周期：tombstone 标记；第二周期：确认并删除 + 标记影子库。
	_ = sm.syncFromDB()
	_ = sm.syncFromDB()

	// shadowMarkDeleted 在 goroutine 里跑，轮询等待。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := sdb.ListSessions()
		if len(rows) == 1 && rows[0].DeletedAtMs != 0 {
			return // pass
		}
		time.Sleep(20 * time.Millisecond)
	}
	rows, _ := sdb.ListSessions()
	if len(rows) != 1 {
		t.Fatalf("影子库应保留行，got %d", len(rows))
	}
	t.Errorf("tombstone 后影子库未标记删除: %+v", rows[0])
}

// TestDaily_IncludesDeletedSessions 日报口径：被删线出现在活跃列表（带
// 标记）与 DeletedSessions 引用里；僵尸排除已删线。
func TestDaily_IncludesDeletedSessions(t *testing.T) {
	const from, to = int64(10000), int64(20000)
	sm, _, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('p', '/w', '', 'P', 1, 1000)`)
	})
	del := int64(18000)
	seedShadow := func() {
		// 活着的活跃线。
		_ = sdb.UpsertSessions([]shadow.SessionRow{
			{ID: "live", Title: "活线", ProjectID: "p", CreatedMs: 11000, UpdatedMs: 15000},
			// 已删线：窗口内有消息 + 窗口内被删。
			{ID: "gone", Title: "删线", ProjectID: "p", CreatedMs: 9000, UpdatedMs: 17000},
			// 已删但闲置很久的线：不应成为僵尸（已删排除）。
			{ID: "gone-old", Title: "旧删线", ProjectID: "p", CreatedMs: 100, UpdatedMs: 200},
		})
		_ = sdb.UpsertMessages([]shadow.MessageRow{
			{ID: "lm", SessionID: "live", Role: "user", CreatedMs: 12000, UpdatedMs: 12000},
			{ID: "gm1", SessionID: "gone", Role: "user", CreatedMs: 12000, UpdatedMs: 12000},
			{ID: "gm2", SessionID: "gone", Role: "assistant", CreatedMs: 16000, UpdatedMs: 16000},
		})
		_ = sdb.UpsertParts([]shadow.PartRow{
			{ID: "lm-p", MessageID: "lm", SessionID: "live", Kind: "text", Text: "活线提问", CreatedMs: 12000},
			{ID: "gm1-p", MessageID: "gm1", SessionID: "gone", Kind: "text", Text: "删线提问", CreatedMs: 12000},
			{ID: "gm2-p", MessageID: "gm2", SessionID: "gone", Kind: "text", Text: "删线回复", CreatedMs: 16000},
		})
		_ = sdb.MarkDeleted([]string{"gone"}, del)
		_ = sdb.MarkDeleted([]string{"gone-old"}, 300)
	}
	seedShadow()

	digest, err := sm.buildDailyDigest(from, to)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if len(digest.Projects) != 1 {
		t.Fatalf("projects = %d", len(digest.Projects))
	}
	p := digest.Projects[0]

	var goneView *DailySession
	for i := range p.ActiveSessions {
		if p.ActiveSessions[i].SessionID == "gone" {
			goneView = &p.ActiveSessions[i]
		}
	}
	if goneView == nil {
		t.Fatalf("被删线应出现在活跃列表:\n%+v", digest)
	}
	if !goneView.Deleted || goneView.DeletedAtMs != del {
		t.Errorf("gone view = %+v", goneView)
	}
	if !goneView.HasExcerpt || goneView.FirstUserExcerpt != "删线提问" {
		t.Errorf("被删线摘录 = %+v", goneView)
	}

	if len(p.DeletedSessions) != 1 || p.DeletedSessions[0].SessionID != "gone" {
		t.Errorf("DeletedSessions = %+v（gone-old 删除时刻在窗口外，不应列入）", p.DeletedSessions)
	}
	if p.DeletedSessions[0].LastActivity != del {
		t.Errorf("DeletedSessions[0].LastActivity = %d, want 删除时刻 %d", p.DeletedSessions[0].LastActivity, del)
	}

	for _, z := range digest.Zombies {
		if z.SessionID == "gone-old" {
			t.Errorf("已删线不应成为僵尸: %+v", digest.Zombies)
		}
	}
}

// TestDaily_LegacyWithoutShadow 未接影子库时走旧路径（行为不变烟囱测试）。
func TestDaily_LegacyWithoutShadow(t *testing.T) {
	const from, to = int64(10000), int64(20000)
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('p', '/w', '', 'P', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "p", "旧路径", 12000, 15000, 0)
		dailyInsertMessage(wdb, "m1", "s1", "user", "旧路径提问", 13000)
	})
	defer database.Close()
	sm := NewStateManager(database) // 无影子库

	digest, err := sm.buildDailyDigest(from, to)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if digest.TotalActive != 1 || len(digest.Projects) != 1 {
		t.Fatalf("legacy digest = %+v", digest)
	}
	if len(digest.Projects[0].DeletedSessions) != 0 {
		t.Errorf("旧路径不应有 DeletedSessions")
	}
}

// TestMessages_LiveFirstArchiveForDead 消息查询的优先级语义：活线看活库
// （新鲜 + 反映 revert/compaction 后的现行视图，即使影子库里有不同的旧
// 内容）；死线（已从 opencode 消失）走影子库全史；两边都没有的老尸体
// 回落活库返回空。
func TestMessages_LiveFirstArchiveForDead(t *testing.T) {
	sm, _, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('p', '/w', '', 'P', 1, 1000)`)
		dailyInsertSession(wdb, "s-live", "p", "活线", 1000, 2000, 0)
		dailyInsertMessage(wdb, "lm1", "s-live", "user", "活库现行内容", 1100)
	})
	// 活线在影子库里也有一份"陈旧镜像"（内容不同）——必须返回活库版。
	_ = sdb.UpsertSessions([]shadow.SessionRow{
		{ID: "s-live", Title: "活线", ProjectID: "p", CreatedMs: 1000, UpdatedMs: 2000},
		{ID: "s-dead", Title: "死线", ProjectID: "p", CreatedMs: 1000, UpdatedMs: 2000},
	})
	_ = sdb.UpsertMessages([]shadow.MessageRow{
		{ID: "lm1-s", SessionID: "s-live", Role: "user", CreatedMs: 1100, UpdatedMs: 1100},
		{ID: "dm1", SessionID: "s-dead", Role: "user", CreatedMs: 1100, UpdatedMs: 1100},
	})
	_ = sdb.UpsertParts([]shadow.PartRow{
		{ID: "lm1-s-p", MessageID: "lm1-s", SessionID: "s-live", Kind: "text", Text: "影子库陈旧内容", CreatedMs: 1100},
		{ID: "dm1-p", MessageID: "dm1", SessionID: "s-dead", Kind: "text", Text: "死线全史", CreatedMs: 1100},
	})

	fetch := func(sid string) []MessagePart {
		t.Helper()
		buf := &bytes.Buffer{}
		cl := &clientConn{enc: json.NewEncoder(buf)}
		sm.handleRequestMsg(cl, RequestMsg{Type: "request", Method: "messages", ID: "q", SessionID: sid})
		var resp ResponseMsg
		if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, buf.String())
		}
		if !resp.Ok {
			t.Fatalf("messages %s failed: %s", sid, resp.Error)
		}
		return resp.Messages
	}

	// 活线：活库内容，不是影子库的陈旧镜像。
	if parts := fetch("s-live"); len(parts) != 1 || parts[0].Text != "活库现行内容" {
		t.Errorf("live parts = %+v", parts)
	}
	// 死线（不在 opencode，只在影子库）：返回档案全史。
	if parts := fetch("s-dead"); len(parts) != 1 || parts[0].Text != "死线全史" {
		t.Errorf("dead parts = %+v", parts)
	}
	// 两边都没有：空结果，不报错（与旧行为一致）。
	if parts := fetch("s-ghost"); len(parts) != 0 {
		t.Errorf("ghost parts = %+v", parts)
	}
}

// TestShadowPurgeAction purge 只允许清理已从 opencode 消失的 session。
func TestShadowPurgeAction(t *testing.T) {
	sm, _, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('p', '/w', '', 'P', 1, 1000)`)
		dailyInsertSession(wdb, "s-alive", "p", "活着", 1000, 2000, 0)
	})
	_ = sdb.UpsertSessions([]shadow.SessionRow{
		{ID: "s-alive", Title: "活着", ProjectID: "p", CreatedMs: 1000, UpdatedMs: 2000},
		{ID: "s-dead", Title: "已删", ProjectID: "p", CreatedMs: 1000, UpdatedMs: 2000},
	})
	_ = sdb.MarkDeleted([]string{"s-dead"}, 5000)

	summary := sm.shadowPurgeAction(ActionMsg{Action: "purge", SessionIDs: []string{"s-alive"}})
	if summary.Succeeded != 0 || summary.Failed != 1 {
		t.Errorf("alive purge = %+v, want refused", summary)
	}
	if has, _ := sdb.HasSession("s-alive"); !has {
		t.Errorf("s-alive 不应被 purge")
	}

	summary = sm.shadowPurgeAction(ActionMsg{Action: "purge", SessionIDs: []string{"s-dead"}})
	if summary.Succeeded != 1 || summary.Failed != 0 {
		t.Errorf("dead purge = %+v", summary)
	}
	if has, _ := sdb.HasSession("s-dead"); has {
		t.Errorf("s-dead 应已彻底移除")
	}
}

// TestShadowRetentionSweep 保留期：过期清理、24h 节流、0=永不过期、
// 活线排除（review #3：仍在 opencode 里的线即使闲置超期也不清）。
func TestShadowRetentionSweep(t *testing.T) {
	sm, wdb, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		// 闲置超期但仍活在 opencode 里的线：保留期不得碰它。
		dailyInsertSession(wdb, "s-old-alive", "p", "老活线", 1, 2, 0)
	})
	now := time.UnixMilli(10_000_000_000)
	cutoff := now.AddDate(0, 0, -1).UnixMilli()

	_ = sdb.UpsertSessions([]shadow.SessionRow{
		{ID: "stale", Title: "过期线", ProjectID: "p", CreatedMs: 1, UpdatedMs: cutoff - 1000},
		{ID: "fresh", Title: "新鲜线", ProjectID: "p", CreatedMs: cutoff, UpdatedMs: now.UnixMilli()},
		{ID: "s-old-alive", Title: "老活线", ProjectID: "p", CreatedMs: 1, UpdatedMs: 2},
	})
	_ = sdb.UpsertMessages([]shadow.MessageRow{{ID: "stale-m", SessionID: "stale", CreatedMs: 1, UpdatedMs: cutoff - 1000}})

	sm.SetShadow(sdb, 1) // 保留 1 天
	sm.shadowRetentionSweep(now)
	if has, _ := sdb.HasSession("stale"); has {
		t.Errorf("stale 应被清理")
	}
	if has, _ := sdb.HasSession("fresh"); !has {
		t.Errorf("fresh 应保留")
	}
	if has, _ := sdb.HasSession("s-old-alive"); !has {
		t.Errorf("仍活在 opencode 的老线不应被保留期清理")
	}
	_ = wdb // seed 用，无直接断言

	// 节流：上次运行时间即 now → 立刻再扫不应再动库（无过期项可验，
	// 验证 meta 节流行为：插入新的过期项后 sweep 不清理）。
	_ = sdb.UpsertSessions([]shadow.SessionRow{{ID: "stale2", Title: "又过期", ProjectID: "p", CreatedMs: 1, UpdatedMs: cutoff - 1000}})
	sm.shadowRetentionSweep(now.Add(time.Hour))
	if has, _ := sdb.HasSession("stale2"); !has {
		t.Errorf("24h 节流内不应清理 stale2")
	}
	// 解除节流后清理生效。
	_ = sdb.SetMeta("retention_last_run", "0")
	sm.shadowRetentionSweep(now.Add(25 * time.Hour))
	if has, _ := sdb.HasSession("stale2"); has {
		t.Errorf("解除节流后 stale2 应被清理")
	}

	// 0 = 永不过期。
	sm.SetShadow(sdb, 0)
	_ = sdb.SetMeta("retention_last_run", "0")
	_ = sdb.UpsertSessions([]shadow.SessionRow{{ID: "forever", Title: "永存", ProjectID: "p", CreatedMs: 1, UpdatedMs: 1}})
	sm.shadowRetentionSweep(now.Add(48 * time.Hour))
	if has, _ := sdb.HasSession("forever"); !has {
		t.Errorf("retention=0 不应清理")
	}
}

// TestSyncFromDB_PropagatesArchiveAndTitle review 后续回归：opencode 的
// setArchived 只写 time_archived 不 bump time_updated，setTitle 同理——
// 这类字段变化对水位扫描不可见，必须经 30s DB 同步路径（全量新鲜行）
// 收敛进影子库，否则 daily 的归档分类会瞎、标题会陈旧。
func TestSyncFromDB_PropagatesArchiveAndTitle(t *testing.T) {
	sm, wdb, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('p', '/w', '', 'P', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "p", "原名", 1000, 2000, 0)
	})

	// 第一轮同步：镜像入档。
	if err := sm.syncFromDB(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	waitFor := func(cond func() bool) bool {
		for time.Now().Before(deadline) {
			if cond() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return cond()
	}
	if !waitFor(func() bool {
		rows, _ := sdb.ListSessions()
		return len(rows) == 1
	}) {
		t.Fatalf("初始镜像未入档")
	}

	// 归档 + 改标题，都不 bump time_updated（复刻 opencode setArchived/setTitle 行为）。
	wdb.Exec(`UPDATE session SET time_archived = 5000, title = '归档后标题' WHERE id = 's1'`)

	if err := sm.syncFromDB(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		rows, _ := sdb.ListSessions()
		return len(rows) == 1 && rows[0].ArchivedMs == 5000 && rows[0].Title == "归档后标题"
	}) {
		rows, _ := sdb.ListSessions()
		t.Fatalf("归档/标题未传播到影子库: %+v", rows[0])
	}
}

// TestHandleDeleteAction_SalvagesBeforeDelete review #1 回归：删除 action
// 在 BatchDelete 之前同步抢救镜像。测试库里 opencode CLI 不可见（删除必然
// 失败），正好验证"抢救先于删除执行"——失败也挡不住内容进影子库。
func TestHandleDeleteAction_SalvagesBeforeDelete(t *testing.T) {
	sm, _, sdb := setupShadowEnv(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('p', '/w', '', 'P', 1, 1000)`)
		dailyInsertSession(wdb, "s-doom", "p", "待删线", 1000, 2000, 0)
		insertMessageFull(wdb, "dm1", "s-doom", "user", 1100, 1200, map[string]string{"text": "删除前的最后消息"})
	})

	sm.SetManager(manage.New(sm.db))
	_ = sm.handleDeleteAction(ActionMsg{Action: "delete", SessionIDs: []string{"s-doom"}})

	parts, err := sdb.SessionMessages("s-doom")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range parts {
		if p.Text == "删除前的最后消息" {
			found = true
		}
	}
	if !found {
		t.Fatalf("删除前抢救失败：影子库没有 s-doom 的内容（parts=%d）", len(parts))
	}
}

// TestHandleAction_UnknownStillRejected 删除 action 分发路径包含
// "purge" case（编译期之外的烟囱验证：unknown action 仍被拒绝）。
func TestHandleAction_UnknownStillRejected(t *testing.T) {
	sm, _, _ := setupShadowEnv(t, nil)
	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}
	sm.handleActionMsg(cl, ActionMsg{Type: "action", Action: "nope"})
	var resp ResultMsg
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == "" {
		t.Errorf("unknown action 应报错")
	}
}
