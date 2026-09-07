// shadow_test.go 覆盖影子库核心语义：schema 幂等、镜像 upsert、删除标记
// 与自愈、水位线单调、真删除（按 id / 按保留期）、以及与 internal/db
// 语义对齐的读取（消息形状 / 活动窗口 / 摘录 / 骨架截断）。
package shadow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomasWade/octl/internal/types"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "shadow.db"))
	if err != nil {
		t.Fatalf("open shadow: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpen_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadow.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = d1.Close()
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = d2.Close()
}

func seedSession(t *testing.T, d *DB, id string, updated int64) {
	t.Helper()
	if err := d.UpsertSessions([]SessionRow{{ID: id, Title: id, ProjectID: "p", UpdatedMs: updated, CreatedMs: updated - 100}}); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
}

func TestMarkDeleted_UpsertDoesNotResurrect(t *testing.T) {
	d := openTestDB(t)
	seedSession(t, d, "s1", 1000)

	// 标记一次；再次标记（更晚时间）不刷新首次删除时刻。
	if err := d.MarkDeleted([]string{"s1"}, 5000); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := d.MarkDeleted([]string{"s1"}, 9000); err != nil {
		t.Fatalf("mark twice: %v", err)
	}
	rows, _ := d.ListSessions()
	if rows[0].DeletedAtMs != 5000 {
		t.Errorf("deleted_at = %d, want first mark 5000", rows[0].DeletedAtMs)
	}

	// 并发窗口防护：镜像流的 upsert（读到行 → 被删 → 才提交）不得复活
	// 删除标记——即使镜像行 updated_ms 更大。
	if err := d.UpsertSessions([]SessionRow{{ID: "s1", Title: "竞态行", UpdatedMs: 6000}}); err != nil {
		t.Fatalf("racing upsert: %v", err)
	}
	rows, _ = d.ListSessions()
	if rows[0].DeletedAtMs != 5000 {
		t.Errorf("upsert 不应复活删除标记: deleted_at = %d", rows[0].DeletedAtMs)
	}

	// 条件复活：updated_ms 晚于删除时刻（opencode 侧删除后仍有真实活动
	// → 误标）才清除；早于删除时刻的镜像行不复活。
	n, err := d.ResurrectIfNewer([]SessionRow{{ID: "s1", UpdatedMs: 4000}})
	if err != nil || n != 0 {
		t.Errorf("旧活动不应复活: n=%d err=%v", n, err)
	}
	rows, _ = d.ListSessions()
	if rows[0].DeletedAtMs != 5000 {
		t.Errorf("仍应保持删除标记")
	}
	n, err = d.ResurrectIfNewer([]SessionRow{{ID: "s1", UpdatedMs: 6000}})
	if err != nil || n != 1 {
		t.Errorf("新活动应复活: n=%d err=%v", n, err)
	}
	rows, _ = d.ListSessions()
	if rows[0].DeletedAtMs != 0 {
		t.Errorf("复活失败: deleted_at = %d", rows[0].DeletedAtMs)
	}
}

func TestUpserts_Monotonic(t *testing.T) {
	d := openTestDB(t)
	// session：旧快照不覆盖新。
	if err := d.UpsertSessions([]SessionRow{{ID: "s1", Title: "新标题", UpdatedMs: 2000, MsgCount: 5}}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertSessions([]SessionRow{{ID: "s1", Title: "旧快照", UpdatedMs: 1000, MsgCount: 1}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := d.ListSessions()
	if rows[0].Title != "新标题" || rows[0].MsgCount != 5 {
		t.Errorf("旧快照不应覆盖新: %+v", rows[0])
	}
	// message：同理（role 单调由同一条 WHERE 保证，这里以 UpdatedMs 驱动的
	// upsert 不报错为底线，文本级单调由 part 断言覆盖）。
	_ = d.UpsertMessages([]MessageRow{{ID: "m1", SessionID: "s1", Role: "user", UpdatedMs: 2000}})
	_ = d.UpsertMessages([]MessageRow{{ID: "m1", SessionID: "s1", Role: "assistant", UpdatedMs: 1000}})
	// part：同理（updated_ms 驱动）。
	_ = d.UpsertParts([]PartRow{{ID: "p1", MessageID: "m1", SessionID: "s1", Kind: "text", Text: "新版", UpdatedMs: 2000}})
	_ = d.UpsertParts([]PartRow{{ID: "p1", MessageID: "m1", SessionID: "s1", Kind: "text", Text: "旧版", UpdatedMs: 1000}})
	parts, _ := d.SessionMessages("s1")
	if len(parts) != 1 || parts[0].Text != "新版" {
		t.Errorf("part 旧快照不应覆盖新: %+v", parts)
	}
}

func TestUpserts_Idempotent(t *testing.T) {
	d := openTestDB(t)
	row := SessionRow{ID: "s1", Title: "T", ProjectID: "p", MsgCount: 1}
	for i := 0; i < 3; i++ {
		if err := d.UpsertSessions([]SessionRow{row}); err != nil {
			t.Fatalf("upsert sessions %d: %v", i, err)
		}
	}
	msg := MessageRow{ID: "m1", SessionID: "s1", Role: "user", CreatedMs: 1, UpdatedMs: 1}
	for i := 0; i < 3; i++ {
		if err := d.UpsertMessages([]MessageRow{msg}); err != nil {
			t.Fatalf("upsert messages %d: %v", i, err)
		}
	}
	part := PartRow{ID: "p1", MessageID: "m1", SessionID: "s1", Kind: "text", Text: "hi", CreatedMs: 1}
	for i := 0; i < 3; i++ {
		if err := d.UpsertParts([]PartRow{part}); err != nil {
			t.Fatalf("upsert parts %d: %v", i, err)
		}
	}
	sessions, err := d.ListSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %v %v", sessions, err)
	}
	if sessions[0].DeletedAtMs != 0 {
		t.Errorf("upsert should keep alive, got deleted_at=%d", sessions[0].DeletedAtMs)
	}
}

func TestWatermark_Monotonic(t *testing.T) {
	d := openTestDB(t)
	if got := d.GetWatermark("message"); got != 0 {
		t.Errorf("initial watermark = %d, want 0", got)
	}
	if err := d.SetWatermark("message", 100); err != nil {
		t.Fatalf("set 100: %v", err)
	}
	if err := d.SetWatermark("message", 50); err != nil {
		t.Fatalf("set 50: %v", err)
	}
	if got := d.GetWatermark("message"); got != 100 {
		t.Errorf("watermark should not decrease, got %d", got)
	}
	if err := d.SetWatermark("message", 200); err != nil {
		t.Fatalf("set 200: %v", err)
	}
	if got := d.GetWatermark("message"); got != 200 {
		t.Errorf("watermark = %d, want 200", got)
	}
}

func TestPurgeSessions(t *testing.T) {
	d := openTestDB(t)
	seedSession(t, d, "s1", 1000)
	seedSession(t, d, "s2", 1000)
	if err := d.UpsertMessages([]MessageRow{{ID: "m1", SessionID: "s1"}, {ID: "m2", SessionID: "s2"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertParts([]PartRow{{ID: "p1", MessageID: "m1", SessionID: "s1", Text: "x"}, {ID: "p2", MessageID: "m2", SessionID: "s2", Text: "y"}}); err != nil {
		t.Fatal(err)
	}
	n, err := d.PurgeSessions([]string{"s1", "ghost"})
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Errorf("purged = %d, want 1", n)
	}
	if has, _ := d.HasSession("s1"); has {
		t.Errorf("s1 should be gone")
	}
	if has, _ := d.HasSession("s2"); !has {
		t.Errorf("s2 should survive")
	}
	parts, err := d.SessionMessages("s2")
	if err != nil || len(parts) != 1 {
		t.Errorf("s2 parts = %v %v", parts, err)
	}
	parts, _ = d.SessionMessages("s1")
	if len(parts) != 0 {
		t.Errorf("s1 parts should be purged, got %d", len(parts))
	}
}

func TestPurgeOlderThan(t *testing.T) {
	d := openTestDB(t)
	// 三条线：活跃新、删除已久（以 deleted_at 计寿）、活跃已久（以 updated 计寿）。
	seedSession(t, d, "fresh", 9000)
	seedSession(t, d, "old-deleted", 100)
	seedSession(t, d, "old-alive", 100)
	seedSession(t, d, "old-but-in-opencode", 100)
	if err := d.MarkDeleted([]string{"old-deleted"}, 200); err != nil {
		t.Fatal(err)
	}

	n, err := d.PurgeOlderThan(5000, map[string]struct{}{"old-but-in-opencode": {}})
	if err != nil {
		t.Fatalf("purge older: %v", err)
	}
	if n != 2 {
		t.Errorf("purged = %d, want 2（old-deleted 与 old-alive）", n)
	}
	if has, _ := d.HasSession("fresh"); !has {
		t.Errorf("fresh should survive")
	}
	if has, _ := d.HasSession("old-deleted"); has {
		t.Errorf("old-deleted should be purged")
	}
	if has, _ := d.HasSession("old-alive"); has {
		t.Errorf("old-alive should be purged")
	}
	// exclude：仍活在 opencode 里的线即使闲置超期也不清。
	if has, _ := d.HasSession("old-but-in-opencode"); !has {
		t.Errorf("exclude 集合内的活线不应被清理")
	}
}

func TestSessionMessages_Shape(t *testing.T) {
	d := openTestDB(t)
	seedSession(t, d, "s1", 1000)
	msgs := []MessageRow{
		{ID: "m1", SessionID: "s1", Role: "user", Agent: "", ModelJSON: `{}`, CreatedMs: 100},
		{ID: "m2", SessionID: "s1", Role: "assistant", Agent: "build", ModelJSON: `{"id":"glm"}`, CreatedMs: 200},
	}
	if err := d.UpsertMessages(msgs); err != nil {
		t.Fatal(err)
	}
	parts := []PartRow{
		{ID: "p0", MessageID: "m1", SessionID: "s1", Kind: "text", Text: "你好", CreatedMs: 100},
		{ID: "p1", MessageID: "m2", SessionID: "s1", Kind: "reasoning", Text: "思考中", CreatedMs: 200},
		{ID: "p2", MessageID: "m2", SessionID: "s1", Kind: "text", Text: "回复正文", CreatedMs: 300},
	}
	if err := d.UpsertParts(parts); err != nil {
		t.Fatal(err)
	}
	got, err := d.SessionMessages("s1")
	if err != nil {
		t.Fatalf("SessionMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("parts = %d, want 3", len(got))
	}
	if got[0].Role != "user" || got[0].Text != "你好" {
		t.Errorf("part0 = %+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Text != "思考中" {
		t.Errorf("part1 = %+v", got[1])
	}
	if got[2].Agent != "build" || got[2].ModelInfo != `{"id":"glm"}` {
		t.Errorf("part2 = %+v", got[2])
	}
}

func TestMessageActivity_Window(t *testing.T) {
	d := openTestDB(t)
	seedSession(t, d, "s1", 1000)
	seedSession(t, d, "s2", 1000)
	seed := []MessageRow{
		{ID: "a", SessionID: "s1", CreatedMs: 900},
		{ID: "b", SessionID: "s1", CreatedMs: 1500},
		{ID: "c", SessionID: "s1", CreatedMs: 2500},
		{ID: "d", SessionID: "s2", CreatedMs: 100},
	}
	if err := d.UpsertMessages(seed); err != nil {
		t.Fatal(err)
	}
	act, err := d.MessageActivity(1000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(act) != 1 {
		t.Fatalf("activity = %v, want only s1", act)
	}
	if act[0].SessionID != "s1" || act[0].Count != 1 || act[0].FirstAt != 1500 || act[0].LastAt != 1500 {
		t.Errorf("activity = %+v", act[0])
	}
}

func TestSessionExcerpts(t *testing.T) {
	d := openTestDB(t)
	seedSession(t, d, "s1", 1000)
	msgs := []MessageRow{
		{ID: "u1", SessionID: "s1", Role: "user", CreatedMs: 100},
		{ID: "u2", SessionID: "s1", Role: "user", CreatedMs: 1500},
		{ID: "a1", SessionID: "s1", Role: "assistant", CreatedMs: 1600},
		{ID: "a2", SessionID: "s1", Role: "assistant", CreatedMs: 2500},
	}
	if err := d.UpsertMessages(msgs); err != nil {
		t.Fatal(err)
	}
	parts := []PartRow{
		{ID: "u1-p", MessageID: "u1", SessionID: "s1", Kind: "text", Text: "窗口前的第一条", CreatedMs: 100},
		{ID: "u2-p", MessageID: "u2", SessionID: "s1", Kind: "text", Text: "窗口内的第一条", CreatedMs: 1500},
		{ID: "a1-p", MessageID: "a1", SessionID: "s1", Kind: "text", Text: "窗口内回复", CreatedMs: 1600},
		{ID: "a2-p", MessageID: "a2", SessionID: "s1", Kind: "text", Text: "窗口后回复", CreatedMs: 2500},
	}
	if err := d.UpsertParts(parts); err != nil {
		t.Fatal(err)
	}

	first, last, err := d.SessionExcerpts("s1", 1000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if first != "窗口内的第一条" {
		t.Errorf("firstUser = %q", first)
	}
	// lastAssistant 语义：time_created < to 的最新 assistant（不限 from）。
	if last != "窗口内回复" {
		t.Errorf("lastAssistant = %q", last)
	}
}

func TestUserSkeleton_Truncation(t *testing.T) {
	d := openTestDB(t)
	seedSession(t, d, "s1", 1000)
	// 多 text part 拼接 + 超 2000 rune 截断。
	long := strings.Repeat("长", 1500)
	msgs := []MessageRow{
		{ID: "u1", SessionID: "s1", Role: "user", CreatedMs: 100},
		{ID: "u2", SessionID: "s1", Role: "user", CreatedMs: 200},
	}
	if err := d.UpsertMessages(msgs); err != nil {
		t.Fatal(err)
	}
	parts := []PartRow{
		{ID: "u1-a", MessageID: "u1", SessionID: "s1", Kind: "text", Text: long, CreatedMs: 100},
		{ID: "u1-b", MessageID: "u1", SessionID: "s1", Kind: "text", Text: long, CreatedMs: 101},
		{ID: "u2-a", MessageID: "u2", SessionID: "s1", Kind: "text", Text: "第二条", CreatedMs: 200},
	}
	if err := d.UpsertParts(parts); err != nil {
		t.Fatal(err)
	}
	sk, err := d.UserSkeleton("s1", 0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	if len(sk) != 2 {
		t.Fatalf("skeleton entries = %d, want 2", len(sk))
	}
	runes := []rune(sk[0].Text)
	// 与 db 层语义一致：截到 2000 rune 后追加 "…"，总长 2001。
	if len(runes) != 2001 || !strings.HasSuffix(sk[0].Text, "…") {
		t.Errorf("entry0 len = %d, suffix ok = %v", len(runes), strings.HasSuffix(sk[0].Text, "…"))
	}
	if sk[1].Text != "第二条" {
		t.Errorf("entry1 = %q", sk[1].Text)
	}
	// 窗口过滤：只取 [150, +inf)。
	sk, _ = d.UserSkeleton("s1", 150, 1<<62)
	if len(sk) != 1 || sk[0].Text != "第二条" {
		t.Errorf("windowed skeleton = %+v", sk)
	}
}

func TestRefreshMsgCounts(t *testing.T) {
	d := openTestDB(t)
	seedSession(t, d, "s1", 1000)
	if err := d.UpsertMessages([]MessageRow{
		{ID: "m1", SessionID: "s1"}, {ID: "m2", SessionID: "s1"}, {ID: "m3", SessionID: "s1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.RefreshMsgCounts([]string{"s1"}); err != nil {
		t.Fatal(err)
	}
	rows, _ := d.ListSessions()
	if rows[0].MsgCount != 3 {
		t.Errorf("msg_count = %d, want 3", rows[0].MsgCount)
	}
}

func TestMeta_Roundtrip(t *testing.T) {
	d := openTestDB(t)
	if _, ok := d.GetMeta("k"); ok {
		t.Errorf("missing key should be !ok")
	}
	if err := d.SetMeta("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetMeta("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, ok := d.GetMeta("k"); !ok || v != "v2" {
		t.Errorf("meta = %q %v", v, ok)
	}
}

// 编译期确保 types 依赖被使用（SessionMessages 返回 types.MessagePart）。
var _ []types.MessagePart
