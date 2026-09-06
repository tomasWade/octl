// daily_test.go 覆盖 "daily" 请求的时间窗口聚合：分类（新增/活跃/归档/
// 僵尸/卡住）、摘录名额与截断、参数校验与未知 method 错误。
package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// dailySeed 提供常用插入语句的快捷方式（表结构由 setupDBWithData 创建）。
func dailyInsertSession(wdb *sql.DB, id, projectID, title string, created, updated, archived int64) {
	wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
		time_created, time_updated, time_archived)
		VALUES (?, ?, '', ?, '/w', ?, '1.0', ?, ?, ?)`,
		id, projectID, id, title, created, updated, archived)
}

func dailyInsertMessage(wdb *sql.DB, id, sessionID, role, text string, created int64) {
	wdb.Exec(`INSERT INTO message (id, session_id, data, time_created)
		VALUES (?, ?, ?, ?)`, id, sessionID, `{"role":"`+role+`"}`, created)
	wdb.Exec(`INSERT INTO part (id, message_id, session_id, data, time_created)
		VALUES (?, ?, ?, ?, ?)`,
		id+"-p1", id, sessionID, `{"type":"text","text":`+mustJSONString(text)+`}`, created)
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestBuildDailyDigest_Classification 验证窗口内的新增/活跃/归档分类、
// project 分组费用、活跃排序与摘录标记。
func TestBuildDailyDigest_Classification(t *testing.T) {
	const from, to = int64(10000), int64(20000)
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('proj-a', '/a', 'git', 'Alpha', 1, 1000),
			       ('proj-b', '/b', 'git', 'Beta', 1, 1000)`)
		// s-new：窗口内新建 + 活跃（2 条消息，应排活跃首位）。
		dailyInsertSession(wdb, "s-new", "proj-a", "New session", 12000, 13000, 0)
		dailyInsertMessage(wdb, "m1", "s-new", "user", "帮我修 sidebar", 12000)
		dailyInsertMessage(wdb, "m2", "s-new", "assistant", "已修复删除确认浮层", 13000)
		// s-old：窗口前创建、窗口内活跃（1 条消息）。
		dailyInsertSession(wdb, "s-old", "proj-a", "Old session", 500, 15000, 0)
		dailyInsertMessage(wdb, "m3", "s-old", "assistant", "继续推进文档", 15000)
		// s-archived：窗口内归档（不活跃）。
		dailyInsertSession(wdb, "s-arch", "proj-b", "Done session", 500, 600, 18000)
		// s-quite：窗口外活动，不出现在任何分类里。
		dailyInsertSession(wdb, "s-quiet", "proj-b", "Quiet session", 500, 600, 0)
		dailyInsertMessage(wdb, "m4", "s-quiet", "user", "旧消息", 600)
	})
	defer database.Close()

	sm := NewStateManager(database)
	d, err := sm.buildDailyDigest(from, to)
	if err != nil {
		t.Fatalf("buildDailyDigest: %v", err)
	}

	if d.TotalActive != 2 || d.Excerpted != 2 {
		t.Errorf("TotalActive=%d Excerpted=%d, want 2/2", d.TotalActive, d.Excerpted)
	}
	if len(d.Projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(d.Projects))
	}

	var projA *DailyProject
	for i := range d.Projects {
		if d.Projects[i].ProjectID == "proj-a" {
			projA = &d.Projects[i]
		}
	}
	if projA == nil {
		t.Fatal("proj-a missing")
	}
	if projA.Name != "Alpha" || projA.Worktree != "/a" {
		t.Errorf("project meta = %q/%q, want Alpha//a", projA.Name, projA.Worktree)
	}
	if len(projA.NewSessions) != 1 || projA.NewSessions[0].SessionID != "s-new" {
		t.Errorf("NewSessions = %+v, want [s-new]", projA.NewSessions)
	}
	if len(projA.ActiveSessions) != 2 {
		t.Fatalf("ActiveSessions = %d, want 2", len(projA.ActiveSessions))
	}
	// 活跃按窗口内消息数降序：s-new(2) 在 s-old(1) 前。
	if projA.ActiveSessions[0].SessionID != "s-new" || projA.ActiveSessions[1].SessionID != "s-old" {
		t.Errorf("active order = %s,%s, want s-new,s-old",
			projA.ActiveSessions[0].SessionID, projA.ActiveSessions[1].SessionID)
	}
	first := projA.ActiveSessions[0]
	if !first.HasExcerpt || first.FirstUserExcerpt != "帮我修 sidebar" || first.LastAssistantExcerpt != "已修复删除确认浮层" {
		t.Errorf("s-new excerpt = %q/%q (has=%v)", first.FirstUserExcerpt, first.LastAssistantExcerpt, first.HasExcerpt)
	}
	second := projA.ActiveSessions[1]
	if !second.HasExcerpt || second.FirstUserExcerpt != "" {
		// s-old 窗口内只有 assistant 消息，用户摘录应为空。
		t.Errorf("s-old excerpt = %q/%q (has=%v)", second.FirstUserExcerpt, second.LastAssistantExcerpt, second.HasExcerpt)
	}
	if second.LastAssistantExcerpt != "继续推进文档" {
		t.Errorf("s-old lastAssistant = %q", second.LastAssistantExcerpt)
	}

	var projB *DailyProject
	for i := range d.Projects {
		if d.Projects[i].ProjectID == "proj-b" {
			projB = &d.Projects[i]
		}
	}
	if projB == nil {
		t.Fatal("proj-b missing")
	}
	if len(projB.ActiveSessions) != 0 || len(projB.NewSessions) != 0 {
		t.Errorf("proj-b active/new = %d/%d, want 0/0", len(projB.ActiveSessions), len(projB.NewSessions))
	}
	if len(projB.ArchivedSessions) != 1 || projB.ArchivedSessions[0].SessionID != "s-arch" {
		t.Errorf("ArchivedSessions = %+v, want [s-arch]", projB.ArchivedSessions)
	}
}

// TestBuildDailyDigest_ExcerptTruncation 验证摘录按 rune 上限截断：
// 用户 120、assistant 500（省略号占一位）。
func TestBuildDailyDigest_ExcerptTruncation(t *testing.T) {
	const from, to = int64(10000), int64(20000)
	longUser := strings.Repeat("问", 300)
	longAssistant := strings.Repeat("答", 800)
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "global", "S1", 500, 15000, 0)
		dailyInsertMessage(wdb, "m1", "s1", "user", longUser, 11000)
		dailyInsertMessage(wdb, "m2", "s1", "assistant", longAssistant, 15000)
	})
	defer database.Close()

	sm := NewStateManager(database)
	d, err := sm.buildDailyDigest(from, to)
	if err != nil {
		t.Fatalf("buildDailyDigest: %v", err)
	}
	s := d.Projects[0].ActiveSessions[0]
	if got := len([]rune(s.FirstUserExcerpt)); got != 120 {
		t.Errorf("user excerpt runes = %d, want 120", got)
	}
	if got := len([]rune(s.LastAssistantExcerpt)); got != 500 {
		t.Errorf("assistant excerpt runes = %d, want 500", got)
	}
	if !strings.HasSuffix(s.LastAssistantExcerpt, "…") {
		t.Error("truncated excerpt should end with ellipsis")
	}
}

// TestBuildDailyDigest_ExcerptLimit 验证全局摘录名额：超出上限的活跃
// session 只有计数（HasExcerpt=false），名额按窗口内消息数分配。
func TestBuildDailyDigest_ExcerptLimit(t *testing.T) {
	const from, to = int64(10000), int64(20000)
	// 临时下调名额，避免真的种 100+ 个 session。
	oldLimit := dailyExcerptSessionLimit
	dailyExcerptSessionLimit = 3
	defer func() { dailyExcerptSessionLimit = oldLimit }()

	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		// 5 个活跃 session，消息数 1..5；名额 3 应给消息最多的 s5/s4/s3。
		for i := 1; i <= 5; i++ {
			id := "s" + string(rune('0'+i))
			dailyInsertSession(wdb, id, "global", id, 500, 15000, 0)
			for j := 0; j < i; j++ {
				dailyInsertMessage(wdb, id+"-m"+string(rune('0'+j)), id, "user", "msg", 11000+int64(j))
			}
		}
	})
	defer database.Close()

	sm := NewStateManager(database)
	d, err := sm.buildDailyDigest(from, to)
	if err != nil {
		t.Fatalf("buildDailyDigest: %v", err)
	}
	if d.TotalActive != 5 || d.Excerpted != 3 {
		t.Fatalf("TotalActive=%d Excerpted=%d, want 5/3", d.TotalActive, d.Excerpted)
	}
	for _, s := range d.Projects[0].ActiveSessions {
		wantExcerpt := s.MsgCount >= 3
		if s.HasExcerpt != wantExcerpt {
			t.Errorf("%s msgCount=%d HasExcerpt=%v, want %v", s.SessionID, s.MsgCount, s.HasExcerpt, wantExcerpt)
		}
	}
}

// TestBuildDailyDigest_Zombies 验证僵尸规则：闲置超 48h 且 30d 内有活动、
// 未归档；归档与近期活动的不算。
func TestBuildDailyDigest_Zombies(t *testing.T) {
	now := time.Now()
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		// 僵尸：3 天前最后活动。
		dailyInsertSession(wdb, "s-zombie", "global", "Z", 500, now.Add(-72*time.Hour).UnixMilli(), 0)
		// 非僵尸：10 小时前活动（闲置不足 48h）。
		dailyInsertSession(wdb, "s-fresh", "global", "F", 500, now.Add(-10*time.Hour).UnixMilli(), 0)
		// 非僵尸：40 天前活动（超出 30d 观察窗）。
		dailyInsertSession(wdb, "s-ancient", "global", "A", 500, now.Add(-40*24*time.Hour).UnixMilli(), 0)
		// 非僵尸：已归档。
		dailyInsertSession(wdb, "s-arch", "global", "R", 500, now.Add(-72*time.Hour).UnixMilli(),
			now.Add(-70*time.Hour).UnixMilli())
	})
	defer database.Close()

	sm := NewStateManager(database)
	d, err := sm.buildDailyDigest(10000, 20000)
	if err != nil {
		t.Fatalf("buildDailyDigest: %v", err)
	}
	if len(d.Zombies) != 1 || d.Zombies[0].SessionID != "s-zombie" {
		t.Errorf("Zombies = %+v, want [s-zombie]", d.Zombies)
	}
}

// TestBuildDailyDigest_StuckStates 验证 daemon 内存态中 PERMISSION / ERROR
// 的 session 进入 StuckStates。
func TestBuildDailyDigest_StuckStates(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()

	sm := NewStateManager(database)
	sm.processEvent(marshalEvent(t, "permission.asked", map[string]interface{}{"sessionID": "s-perm"}))
	sm.processEvent(marshalEvent(t, "session.error", map[string]interface{}{
		"sessionID": "s-err",
		"error":     map[string]interface{}{"name": "e"},
	}))
	sm.processEvent(marshalEvent(t, "session.status", map[string]interface{}{
		"sessionID": "s-busy",
		"status":    map[string]interface{}{"type": "busy"},
	}))

	d, err := sm.buildDailyDigest(10000, 20000)
	if err != nil {
		t.Fatalf("buildDailyDigest: %v", err)
	}
	if len(d.StuckStates) != 2 {
		t.Fatalf("StuckStates = %d, want 2", len(d.StuckStates))
	}
	for _, st := range d.StuckStates {
		if st.SessionID != "s-perm" && st.SessionID != "s-err" {
			t.Errorf("unexpected stuck session %s", st.SessionID)
		}
	}
}

// TestBuildDailyDigest_ListsNeverNull wire 契约：无 omitempty 的列表字段
// （projects/newSessions/activeSessions/archivedSessions）空时序列化为
// [] 而非 null——消费方（skill/jq）len() 不设防。
func TestBuildDailyDigest_ListsNeverNull(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()

	sm := NewStateManager(database)

	// 空窗口：无任何 project 活动。
	d, err := sm.buildDailyDigest(10000, 20000)
	if err != nil {
		t.Fatalf("buildDailyDigest: %v", err)
	}
	if d.Projects == nil {
		t.Fatal("Projects should be empty slice, not nil")
	}

	// 有 project 但无新增/归档的分类字段也不得为 null。
	database2 := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "global", "S1", 500, 15000, 0)
		dailyInsertMessage(wdb, "m1", "s1", "user", "hi", 15000)
	})
	defer database2.Close()
	sm2 := NewStateManager(database2)
	d2, err := sm2.buildDailyDigest(10000, 20000)
	if err != nil {
		t.Fatalf("buildDailyDigest: %v", err)
	}
	b, err := json.Marshal(d2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"newSessions":[]`, `"archivedSessions":[]`, `"activeSessions":[{`, `"projects":[{`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("digest JSON missing %s: %s", want, b)
		}
	}
}

// TestHandleRequest_Daily 验证参数校验与正常应答走 wire 通道。
func TestHandleRequest_Daily(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "global", "S1", 12000, 13000, 0)
		dailyInsertMessage(wdb, "m1", "s1", "user", "hello", 12000)
	})
	defer database.Close()

	sm := NewStateManager(database)

	t.Run("invalid window", func(t *testing.T) {
		buf := &bytes.Buffer{}
		cl := &clientConn{enc: json.NewEncoder(buf)}
		sm.handleRequestMsg(cl, RequestMsg{Type: "request", Method: "daily", ID: "r1", From: 20000, To: 10000})
		if !bytes.Contains(buf.Bytes(), []byte(`"ok":false`)) {
			t.Errorf("expected ok=false for from>=to, got %q", buf.String())
		}
	})
	t.Run("zero from", func(t *testing.T) {
		buf := &bytes.Buffer{}
		cl := &clientConn{enc: json.NewEncoder(buf)}
		sm.handleRequestMsg(cl, RequestMsg{Type: "request", Method: "daily", ID: "r2", From: 0, To: 10000})
		if !bytes.Contains(buf.Bytes(), []byte(`"ok":false`)) {
			t.Errorf("expected ok=false for from=0, got %q", buf.String())
		}
	})
	t.Run("valid", func(t *testing.T) {
		buf := &bytes.Buffer{}
		cl := &clientConn{enc: json.NewEncoder(buf)}
		sm.handleRequestMsg(cl, RequestMsg{Type: "request", Method: "daily", ID: "r3", From: 10000, To: 20000})
		out := buf.String()
		if !strings.Contains(out, `"ok":true`) {
			t.Errorf("expected ok=true, got %q", out)
		}
		if !strings.Contains(out, `"totalActive":1`) {
			t.Errorf("expected totalActive=1 in daily payload, got %q", out)
		}
	})
	t.Run("unknown method", func(t *testing.T) {
		buf := &bytes.Buffer{}
		cl := &clientConn{enc: json.NewEncoder(buf)}
		sm.handleRequestMsg(cl, RequestMsg{Type: "request", Method: "nope", ID: "r4"})
		if !bytes.Contains(buf.Bytes(), []byte(`unknown method`)) {
			t.Errorf("expected unknown method error, got %q", buf.String())
		}
	})
}
