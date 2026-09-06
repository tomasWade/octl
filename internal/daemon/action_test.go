package daemon

import (
	"database/sql"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasWade/octl/internal/manage"
)

// startActionDaemon 起一个带 Manager 的真实 StateManager + 临时 socket，
// 供 ActionOnce 测试使用（favorite/unknown action 不触发外部 exec）。
func startActionDaemon(t *testing.T, sockPath string, seed func(wdb *sql.DB)) *StateManager {
	t.Helper()
	database := setupDBWithData(t, seed)
	t.Cleanup(func() { database.Close() })

	sm := NewStateManagerWithSocket(database, sockPath)
	sm.SetManager(manage.New(database))
	t.Cleanup(func() { sm.Close() })

	go func() {
		if err := sm.Run(); err != nil {
			t.Logf("Run returned: %v", err)
		}
	}()
	waitForSocket(t, sockPath, time.Second)
	return sm
}

// TestActionOnce_Favorite 验证 favorite action（无外部 exec）经 ActionOnce
// 全链路返回带成功 Summary 的 result。
func TestActionOnce_Favorite(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "act-fav.sock")
	startActionDaemon(t, sockPath, seedQuerySessions)

	res, err := ActionOnce(sockPath, ActionMsg{Action: "favorite", SessionIDs: []string{"s1", "s2"}}, 2*time.Second)
	if err != nil {
		t.Fatalf("ActionOnce: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("result.Error = %q", res.Error)
	}
	if res.Action != "favorite" {
		t.Errorf("result.Action = %q, want favorite", res.Action)
	}
	if res.Summary.Total != 2 || res.Summary.Succeeded != 2 || res.Summary.Failed != 0 {
		t.Errorf("summary = %+v, want 2 succeeded", res.Summary)
	}
}

// TestActionOnce_UnknownAction 验证未知 action 返回带错误的 result（而非
// 协议错误）。
func TestActionOnce_UnknownAction(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "act-unknown.sock")
	startActionDaemon(t, sockPath, seedQuerySessions)

	res, err := ActionOnce(sockPath, ActionMsg{Action: "nope"}, 2*time.Second)
	if err != nil {
		t.Fatalf("ActionOnce: %v", err)
	}
	if res.Error != "unknown action: nope" {
		t.Errorf("result.Error = %q, want unknown action error", res.Error)
	}
}

// TestActionOnce_DaemonNotRunning socket 不存在时应返回 ErrDaemonConnect。
func TestActionOnce_DaemonNotRunning(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "act-missing.sock")
	_, err := ActionOnce(sockPath, ActionMsg{Action: "delete"}, time.Second)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "daemon unreachable") {
		t.Errorf("err = %v, want ErrDaemonConnect", err)
	}
}

// TestActionOnce_Timeout daemon 接受连接但不应答时应返回 ErrQueryTimeout。
func TestActionOnce_Timeout(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "act-silent.sock")
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
			go func() {
				buf := make([]byte, 4096)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	waitForSocket(t, sockPath, time.Second)

	_, err = ActionOnce(sockPath, ActionMsg{Action: "favorite"}, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "daemon response timeout") {
		t.Errorf("err = %v, want ErrQueryTimeout", err)
	}
}

// TestResolveActionDirectory 表驱动验证 fork/send 的工作目录补全：显式
// 指定时透传不查库；为空时从 DB 补全；session 不存在或无 directory 时报错。
func TestResolveActionDirectory(t *testing.T) {
	seed := func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, name, time_created, time_updated) VALUES ('p1', '/tmp/p1', 'p1', 1, 1)`)
		wdb.Exec(`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
			VALUES ('dir-1', 'p1', 'a', '/tmp/proj-one', 'Has Dir', '1.0', 1, 1)`)
		wdb.Exec(`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
			VALUES ('dir-2', 'p1', 'b', '', 'No Dir', '1.0', 1, 1)`)
	}

	tests := []struct {
		name      string
		sessionID string
		directory string
		wantDir   string
		wantErr   string
	}{
		{name: "explicit directory passes through without DB lookup", sessionID: "no-such", directory: "/tmp/x", wantDir: "/tmp/x"},
		{name: "empty directory resolved from DB", sessionID: "dir-1", directory: "", wantDir: "/tmp/proj-one"},
		{name: "unknown session fails", sessionID: "no-such", directory: "", wantErr: "resolve directory"},
		{name: "session without directory fails", sessionID: "dir-2", directory: "", wantErr: "no directory on record"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := setupDBWithData(t, seed)
			defer database.Close()
			sm := NewStateManager(database)

			got, err := sm.resolveActionDirectory(tt.sessionID, tt.directory)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantDir {
				t.Errorf("dir = %q, want %q", got, tt.wantDir)
			}
		})
	}
}

// seedSessionTree 构造一棵删除级联测试用的 session 树：
// tree-1 → (tree-1a → tree-1a1, tree-1b)，独立 root tree-2，
// 互相成环的 loop-x/loop-y，自引用 self-1。
func seedSessionTree(wdb *sql.DB) {
	wdb.Exec(`INSERT INTO project (id, worktree, name, time_created, time_updated) VALUES ('p1', '/tmp/p1', 'p1', 1, 1)`)
	insert := func(id, parent string) {
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version, time_created, time_updated)
			VALUES (?, 'p1', ?, ?, '/tmp/p1', ?, '1.0', 1, 1)`, id, parent, id, id)
	}
	insert("tree-1", "")
	insert("tree-1a", "tree-1")
	insert("tree-1a1", "tree-1a")
	insert("tree-1b", "tree-1")
	insert("tree-2", "")
	insert("loop-x", "loop-y")
	insert("loop-y", "loop-x")
	insert("self-1", "self-1")
}

// TestExpandSessionTree 表驱动验证删除级联的子树展开：多级后代、多输入
// 去重、环不死循环、自引用按叶子、DB 不存在的 ID 原样保留。
func TestExpandSessionTree(t *testing.T) {
	newSM := func(t *testing.T) *StateManager {
		database := setupDBWithData(t, seedSessionTree)
		t.Cleanup(func() { database.Close() })
		return NewStateManager(database)
	}

	toSet := func(ids []string) map[string]bool {
		set := make(map[string]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		return set
	}

	tests := []struct {
		name string
		in   []string
		want map[string]bool
	}{
		{name: "root expands to full subtree", in: []string{"tree-1"}, want: map[string]bool{"tree-1": true, "tree-1a": true, "tree-1a1": true, "tree-1b": true}},
		{name: "leaf has no descendants", in: []string{"tree-2"}, want: map[string]bool{"tree-2": true}},
		{name: "overlapping inputs deduplicated", in: []string{"tree-1", "tree-1a"}, want: map[string]bool{"tree-1": true, "tree-1a": true, "tree-1a1": true, "tree-1b": true}},
		{name: "parent cycle terminates", in: []string{"loop-x"}, want: map[string]bool{"loop-x": true, "loop-y": true}},
		{name: "self reference treated as leaf", in: []string{"self-1"}, want: map[string]bool{"self-1": true}},
		{name: "unknown id kept as-is", in: []string{"no-such"}, want: map[string]bool{"no-such": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := newSM(t)
			got := toSet(sm.expandSessionTree(tt.in))
			if len(got) != len(tt.want) {
				t.Fatalf("expanded = %v, want %v", got, tt.want)
			}
			for id := range tt.want {
				if !got[id] {
					t.Errorf("expanded set missing %q: %v", id, got)
				}
			}
		})
	}
}

// TestExpandSessionTree_Empty 空输入直接返回，不查 DB。
func TestExpandSessionTree_Empty(t *testing.T) {
	sm := NewStateManager(nil)
	if got := sm.expandSessionTree(nil); len(got) != 0 {
		t.Errorf("expandSessionTree(nil) = %v, want empty", got)
	}
}

// TestHandleAction_DeleteCascade 集成验证 Cascade=true 时 handleDeleteAction
// 把展开后的全子树集合传给了 BatchDelete：无论单条删除成败，summary.Results
// 的 ID 集合都应覆盖 root 及其全部后代（ID 使用 zz-tree- 前缀，不会命中真实
// opencode 数据库中的 session）。
func TestHandleAction_DeleteCascade(t *testing.T) {
	seed := func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, name, time_created, time_updated) VALUES ('p1', '/tmp/p1', 'p1', 1, 1)`)
		insert := func(id, parent string) {
			wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version, time_created, time_updated)
				VALUES (?, 'p1', ?, ?, '/tmp/p1', ?, '1.0', 1, 1)`, id, parent, id, id)
		}
		insert("zz-tree-1", "")
		insert("zz-tree-1a", "zz-tree-1")
		insert("zz-tree-1a1", "zz-tree-1a")
		insert("zz-tree-1b", "zz-tree-1")
	}
	database := setupDBWithData(t, seed)
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	sum := sm.handleDeleteAction(ActionMsg{Action: "delete", SessionIDs: []string{"zz-tree-1"}, Cascade: true})

	got := map[string]bool{}
	for _, r := range sum.Results {
		got[r.SessionID] = true
	}
	want := map[string]bool{"zz-tree-1": true, "zz-tree-1a": true, "zz-tree-1a1": true, "zz-tree-1b": true}
	if len(got) != len(want) {
		t.Fatalf("deleted id set = %v (summary %+v), want %v", got, sum, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("delete set missing %q: %v", id, got)
		}
	}
}

// TestHandleAction_DeleteNoCascadeByDefault 验证缺省（Cascade=false）时
// 只删除请求的 ID 本身、不做子树展开——这是 TUI/sidebar/Favorites 等
// 既有客户端的原始语义，必须保持不变。
func TestHandleAction_DeleteNoCascadeByDefault(t *testing.T) {
	seed := func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, name, time_created, time_updated) VALUES ('p1', '/tmp/p1', 'p1', 1, 1)`)
		insert := func(id, parent string) {
			wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version, time_created, time_updated)
				VALUES (?, 'p1', ?, ?, '/tmp/p1', ?, '1.0', 1, 1)`, id, parent, id, id)
		}
		insert("zz-flat-1", "")
		insert("zz-flat-1a", "zz-flat-1")
	}
	database := setupDBWithData(t, seed)
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	sum := sm.handleDeleteAction(ActionMsg{Action: "delete", SessionIDs: []string{"zz-flat-1"}})

	got := map[string]bool{}
	for _, r := range sum.Results {
		got[r.SessionID] = true
	}
	if len(got) != 1 || !got["zz-flat-1"] {
		t.Errorf("deleted id set = %v (summary %+v), want only [zz-flat-1] without expansion", got, sum)
	}
}

// TestHandleAction_ForkAutoDirectoryMissing 验证 fork 在未传 Directory 且
// session 不存在时，在 exec 之前就以 resolve 错误失败（不会向 opencode
// 传递空 --dir）。
func TestHandleAction_ForkAutoDirectoryMissing(t *testing.T) {
	sm := startActionDaemon(t, filepath.Join(t.TempDir(), "act-forkdir.sock"), seedQuerySessions)

	sum := sm.handleForkAction(ActionMsg{Action: "fork", SessionID: "no-such", Message: "hi"})
	if sum.Failed != 1 || len(sum.Results) != 1 {
		t.Fatalf("summary = %+v, want 1 failed", sum)
	}
	if !strings.Contains(sum.Results[0].Error, "resolve directory") {
		t.Errorf("error = %q, want resolve directory failure", sum.Results[0].Error)
	}
}
