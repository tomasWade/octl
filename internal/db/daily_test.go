// daily_test.go 覆盖时间窗口聚合查询：GetMessageActivity 的窗口边界、
// GetSessionExcerpts 的角色定位与 SQL 层预截断。
package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetMessageActivity_Window(t *testing.T) {
	d, dir := setupTestDB(t)
	defer d.Close()
	// setupTestDB 固定数据：session-1 的 msg-1/msg-2 在 1700000001000/1700000002000，
	// session-2 的 msg-3 在 1700000201000。

	tests := []struct {
		name      string
		from, to  int64
		wantCount map[string]int
	}{
		{"full window covers all", 1700000000000, 1700000300000,
			map[string]int{"session-1": 2, "session-2": 1}},
		{"half-open lower bound includes msg-1", 1700000001000, 1700000002000,
			map[string]int{"session-1": 1}},
		{"upper bound exclusive drops msg-1", 1700000000000, 1700000001000,
			map[string]int{}},
		{"empty window", 1800000000000, 1800001000000,
			map[string]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.GetMessageActivity(tt.from, tt.to)
			if err != nil {
				t.Fatalf("GetMessageActivity: %v", err)
			}
			if len(got) != len(tt.wantCount) {
				t.Fatalf("got %d sessions, want %d: %+v", len(got), len(tt.wantCount), got)
			}
			for _, a := range got {
				if tt.wantCount[a.SessionID] != a.Count {
					t.Errorf("%s count = %d, want %d", a.SessionID, a.Count, tt.wantCount[a.SessionID])
				}
				if a.FirstAt <= 0 || a.LastAt < a.FirstAt {
					t.Errorf("%s invalid activity range: %d..%d", a.SessionID, a.FirstAt, a.LastAt)
				}
			}
		})
	}
	_ = dir
}

func TestGetSessionExcerpts_Roles(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// session-1：user "Hello, write some code" → assistant "Here is the code you requested"。
	first, last, err := d.GetSessionExcerpts("session-1", 0, 1700000100000)
	if err != nil {
		t.Fatalf("GetSessionExcerpts: %v", err)
	}
	if first != "Hello, write some code" {
		t.Errorf("firstUser = %q", first)
	}
	if last != "Here is the code you requested" {
		t.Errorf("lastAssistant = %q", last)
	}

	// session-2 只有 user 消息：assistant 摘录为空而非错误。
	first2, last2, err := d.GetSessionExcerpts("session-2", 0, 1700000300000)
	if err != nil {
		t.Fatalf("GetSessionExcerpts: %v", err)
	}
	if first2 != "Review this code please" {
		t.Errorf("firstUser = %q", first2)
	}
	if last2 != "" {
		t.Errorf("lastAssistant = %q, want empty", last2)
	}

	// 不存在的 session：两段为空，不是错误。
	f, l, err := d.GetSessionExcerpts("no-such", 0, 9999999999999)
	if err != nil || f != "" || l != "" {
		t.Errorf("missing session: (%q, %q, %v), want empty/nil", f, l, err)
	}
}

func TestGetSessionExcerpts_WindowBounds(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// session-1：msg-1 user @1700000001000、msg-2 assistant @1700000002000。
	// 窗口 [1700000001000, 1700000001500)：用户摘录取到 msg-1；
	// 末条 assistant 只要求早于 to —— msg-2 晚于 to，摘录落空。
	first, last, err := d.GetSessionExcerpts("session-1", 1700000001000, 1700000001500)
	if err != nil {
		t.Fatalf("GetSessionExcerpts: %v", err)
	}
	if first != "Hello, write some code" {
		t.Errorf("firstUser = %q", first)
	}
	if last != "" {
		t.Errorf("lastAssistant = %q, want empty (assistant msg is after to)", last)
	}

	// to 放宽到覆盖 msg-2：assistant 摘录取到，且不受窗口起点限制。
	first, last, err = d.GetSessionExcerpts("session-1", 1700000001500, 1700000003000)
	if err != nil {
		t.Fatalf("GetSessionExcerpts: %v", err)
	}
	if first != "" {
		t.Errorf("firstUser = %q, want empty", first)
	}
	if last != "Here is the code you requested" {
		t.Errorf("lastAssistant = %q", last)
	}
}

func TestGetSessionExcerpts_PartTruncation(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// 追加一条超长 assistant 消息，验证 SQL 层 substr 预截断到 excerptPartLimit。
	wdb, err := d.NewWritable()
	if err != nil {
		t.Fatalf("NewWritable: %v", err)
	}
	long := strings.Repeat("长", 5000)
	if _, err := wdb.Exec(`INSERT INTO message (id, session_id, data, time_created)
		VALUES ('msg-long', 'session-1', '{"role":"assistant"}', 1700000003000)`); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if _, err := wdb.Exec(`INSERT INTO part (id, message_id, session_id, data, time_created)
		VALUES ('part-long', 'msg-long', 'session-1', ?, 1700000003000)`,
		`{"type":"text","text":"`+long+`"}`); err != nil {
		t.Fatalf("insert part: %v", err)
	}
	wdb.Close()

	_, last, err := d.GetSessionExcerpts("session-1", 0, 1700000010000)
	if err != nil {
		t.Fatalf("GetSessionExcerpts: %v", err)
	}
	if got := len([]rune(last)); got != excerptPartLimit {
		t.Errorf("part excerpt runes = %d, want %d", got, excerptPartLimit)
	}
}

// TestGetUserSkeleton 骨架查询：role 过滤、窗口边界（半开区间）、
// 全史模式（from=0）、多 part 拼接、无 text part 消息跳过、单条截断。
func TestGetUserSkeleton(t *testing.T) {
	d, dir := setupTestDB(t)
	defer d.Close()

	// 基础固定数据：session-1 的 msg-1 是 user（part-1 文本）、msg-2 是
	// assistant；session-2 的 msg-3 是 user。补充：多 part 用户消息、
	// 无 text part 的用户消息、超长文本截断。
	wdb, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open writable: %v", err)
	}
	defer wdb.Close()
	wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES
		('msg-u2', 'session-1', '{"role": "user"}', 1700000003000),
		('msg-u3', 'session-1', '{"role": "user"}', 1700000004000),
		('msg-u4', 'session-1', '{"role": "user"}', 1700000005000)`)
	wdb.Exec(`INSERT INTO part (id, message_id, session_id, data, time_created) VALUES
		('part-a', 'msg-u2', 'session-1', '{"type": "text", "text": "第一段"}', 1700000003100),
		('part-b', 'msg-u2', 'session-1', '{"type": "text", "text": "第二段"}', 1700000003200),
		('part-c', 'msg-u3', 'session-1', '{"type": "tool", "text": "不该出现"}', 1700000004100),
		('part-d', 'msg-u4', 'session-1', '{"type": "text", "text": "超长"}', 1700000005100),
		('part-e', 'msg-u4', 'session-1', '{"type": "text", "text": "也超长"}', 1700000005200),
		('part-f', 'msg-u4', 'session-1', '{"type": "text", "text": "还是超长"}', 1700000005300)`)
	// 三个 2500 字 part 各被 SQL 层截到 1000，拼接 3000 超过 Go 层消息
	// 上限（2000 rune）→ 截到 2000 + 省略号。
	long := strings.Repeat("长", 2500)
	wdb.Exec(`UPDATE part SET data = json_set(data, '$.text', ?) WHERE id IN ('part-d', 'part-e', 'part-f')`, long)

	t.Run("role filter and multip part join", func(t *testing.T) {
		got, err := d.GetUserSkeleton("session-1", 0, 1700000007000)
		if err != nil {
			t.Fatalf("GetUserSkeleton: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d entries, want 3（msg-1, msg-u2, msg-u4）: %+v", len(got), got)
		}
		if got[0].Text != "Hello, write some code" {
			t.Errorf("first entry = %q", got[0].Text)
		}
		if got[1].Text != "第一段\n第二段" {
			t.Errorf("multip part join = %q", got[1].Text)
		}
		if r := []rune(got[2].Text); len(r) != 2001 {
			t.Errorf("go truncation: %d runes, want 2001（2000+…）", len(r))
		}
	})

	t.Run("window bounds half-open", func(t *testing.T) {
		got, err := d.GetUserSkeleton("session-1", 1700000001000, 1700000004000)
		if err != nil {
			t.Fatalf("GetUserSkeleton: %v", err)
		}
		// [1700000001000, 1700000004000)：msg-1、msg-u2；msg-u3 恰在 to 上被排。
		if len(got) != 2 {
			t.Fatalf("got %d, want 2: %+v", len(got), got)
		}
	})

	t.Run("empty session", func(t *testing.T) {
		got, err := d.GetUserSkeleton("no-such", 0, 9999999999999)
		if err != nil {
			t.Fatalf("GetUserSkeleton: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d, want 0", len(got))
		}
	})
}
