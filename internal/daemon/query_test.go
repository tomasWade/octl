package daemon

import (
	"database/sql"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// startQueryDaemon 起一个真实 StateManager + 临时 socket 供 QueryOnce 测试。
func startQueryDaemon(t *testing.T, sockPath string, seed func(wdb *sql.DB)) {
	t.Helper()
	database := setupDBWithData(t, seed)
	t.Cleanup(func() { database.Close() })

	sm := NewStateManagerWithSocket(database, sockPath)
	t.Cleanup(func() { sm.Close() })

	go func() {
		if err := sm.Run(); err != nil {
			t.Logf("Run returned: %v", err)
		}
	}()
	waitForSocket(t, sockPath, time.Second)
}

// seedQuerySessions 构造两个 session（s1/s2）与 s1 的三条消息：
// m1(user, 1 part)、m2(assistant, 2 parts)，part 按时间升序落库。
func seedQuerySessions(wdb *sql.DB) {
	stmts := []string{
		`INSERT INTO project (id, worktree, name, time_created, time_updated) VALUES ('p1', '/tmp/proj-one', 'proj-one', 900, 900)`,
		`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) VALUES ('s1', 'p1', 'a', '/tmp/proj-one', 'Fix daemon sync', '1.0', 1000, 2000)`,
		`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) VALUES ('s2', 'p1', 'b', '/tmp/proj-one', 'Add query CLI', '1.0', 1000, 2500)`,
		`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"user"}', 1000)`,
		`INSERT INTO message (id, session_id, data, time_created) VALUES ('m2', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`,
		`INSERT INTO part (id, message_id, session_id, data, time_created) VALUES ('p1', 'm1', 's1', '{"type":"text","text":"hello"}', 1100)`,
		`INSERT INTO part (id, message_id, session_id, data, time_created) VALUES ('p2', 'm2', 's1', '{"type":"text","text":"hi"}', 1600)`,
		`INSERT INTO part (id, message_id, session_id, data, time_created) VALUES ('p3', 'm2', 's1', '{"type":"text","text":"there"}', 1700)`,
	}
	for _, s := range stmts {
		if _, err := wdb.Exec(s); err != nil {
			panic(err)
		}
	}
}

// TestQueryOnce_Snapshot 验证 snapshot 请求返回 daemon 启动时从 DB 同步的
// 全部 session 状态。
func TestQueryOnce_Snapshot(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "q-snap.sock")
	startQueryDaemon(t, sockPath, seedQuerySessions)

	resp, err := QueryOnce(sockPath, "snapshot", "", 2*time.Second)
	if err != nil {
		t.Fatalf("QueryOnce: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("resp.Ok=false, error=%q", resp.Error)
	}
	if len(resp.States) != 2 {
		t.Fatalf("expected 2 states, got %d", len(resp.States))
	}
	found := map[string]bool{}
	for _, st := range resp.States {
		found[st.SessionID] = true
		if st.Title != "" && st.SessionID == "s1" && st.Title != "Fix daemon sync" {
			t.Errorf("s1 title = %q, want %q", st.Title, "Fix daemon sync")
		}
	}
	if !found["s1"] || !found["s2"] {
		t.Errorf("missing sessions in snapshot: %v", found)
	}
}

// TestQueryOnce_ListSessions 验证 listSessions 请求返回按 project 分组的
// root session 列表。
func TestQueryOnce_ListSessions(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "q-list.sock")
	startQueryDaemon(t, sockPath, seedQuerySessions)

	resp, err := QueryOnce(sockPath, "listSessions", "", 2*time.Second)
	if err != nil {
		t.Fatalf("QueryOnce: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("resp.Ok=false, error=%q", resp.Error)
	}
	if len(resp.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(resp.Projects))
	}
	p := resp.Projects[0]
	if p.ProjectID != "p1" || p.Name != "proj-one" {
		t.Errorf("project = %+v", p)
	}
	if len(p.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(p.Sessions))
	}
}

// TestQueryOnce_Messages 验证 messages 请求按时间升序返回全部 text part。
func TestQueryOnce_Messages(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "q-msg.sock")
	startQueryDaemon(t, sockPath, seedQuerySessions)

	resp, err := QueryOnce(sockPath, "messages", "s1", 2*time.Second)
	if err != nil {
		t.Fatalf("QueryOnce: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("resp.Ok=false, error=%q", resp.Error)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("expected 3 message parts, got %d", len(resp.Messages))
	}
	wantTexts := []string{"hello", "hi", "there"}
	wantRoles := []string{"user", "assistant", "assistant"}
	for i, m := range resp.Messages {
		if m.Text != wantTexts[i] {
			t.Errorf("part %d text = %q, want %q", i, m.Text, wantTexts[i])
		}
		if m.Role != wantRoles[i] {
			t.Errorf("part %d role = %q, want %q", i, m.Role, wantRoles[i])
		}
	}
}

// TestQueryOnce_MessagesUnknownSession 未知 session 返回 Ok=true 与空消息
// 列表（DB 层对无行结果不报错），由 CLI 层负责呈现"无消息"。
func TestQueryOnce_MessagesUnknownSession(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "q-unknown.sock")
	startQueryDaemon(t, sockPath, seedQuerySessions)

	resp, err := QueryOnce(sockPath, "messages", "no-such-session", 2*time.Second)
	if err != nil {
		t.Fatalf("QueryOnce: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("expected Ok=true, got error=%q", resp.Error)
	}
	if len(resp.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(resp.Messages))
	}
}

// TestQueryOnce_DaemonNotRunning socket 不存在时应返回 ErrDaemonConnect。
func TestQueryOnce_DaemonNotRunning(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "q-missing.sock")
	_, err := QueryOnce(sockPath, "snapshot", "", time.Second)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrDaemonConnect) {
		t.Errorf("err = %v, want ErrDaemonConnect", err)
	}
}

// TestQueryOnce_Timeout daemon 接受连接但不应答时应返回 ErrQueryTimeout。
func TestQueryOnce_Timeout(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "q-silent.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// 读取客户端发送的内容但从不应答。
			go io.Copy(io.Discard, conn)
		}
	}()

	_, err = QueryOnce(sockPath, "snapshot", "", 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrQueryTimeout) {
		t.Errorf("err = %v, want ErrQueryTimeout", err)
	}
}
