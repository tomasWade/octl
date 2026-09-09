// Package daemon provides integration tests for the Unix socket event pipeline:
// forwarder connection → event processing → snapshotCh output.
package daemon

import (
	"database/sql"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// waitForSocket polls the Unix socket until it accepts connections or the
// deadline expires.
func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		conn, err := net.DialTimeout("unix", path, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		select {
		case <-deadline:
			t.Fatalf("socket %s not ready within %v", path, timeout)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// writeEvent marshals a rawEvent and writes it as a JSON line to conn.
func writeEvent(t *testing.T, conn net.Conn, typ string, props map[string]interface{}) {
	t.Helper()
	evt := rawEvent{Type: typ, Properties: props}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write event: %v", err)
	}
}

// waitState 循环读取 snapshot 直到目标 session 出现且满足 match 条件。
// listen 前置到初次同步之前后，snapshot 通道的首帧可能是不含事件 session
// 的同步视图，"读一帧即断言"的写法会被首帧抢先命中，必须循环等待。
// （首帧还可能与事件帧同帧到达，跨 session 断言需用帧级谓词一次等齐。）
func waitState(t *testing.T, ch <-chan []SessionState, id string, timeout time.Duration, match func(*SessionState) bool) *SessionState {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case snap := <-ch:
			if st := findState(snap, id); st != nil && match(st) {
				return st
			}
		case <-deadline:
			t.Fatalf("timed out waiting for session %s to satisfy condition (%v)", id, timeout)
			return nil
		}
	}
}

// waitStateGone 循环读取 snapshot 直到目标 session 从帧中消失。
func waitStateGone(t *testing.T, ch <-chan []SessionState, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case snap := <-ch:
			if findState(snap, id) == nil {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for session %s to disappear (%v)", id, timeout)
			return
		}
	}
}

// drainSnapshots drains all currently-buffered snapshots without blocking.
func drainSnapshots(ch <-chan []SessionState) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// findState looks up a session by ID in a snapshot slice. Returns nil when
// the session is absent (e.g. deleted or not yet created).
func findState(snapshot []SessionState, id string) *SessionState {
	for i := range snapshot {
		if snapshot[i].SessionID == id {
			return &snapshot[i]
		}
	}
	return nil
}

// newIntegrationSM creates a StateManager for integration tests, backed by
// an empty temporary database and a Unix socket in a temp directory. It
// returns the manager, the socket path, and a cleanup function.
func newIntegrationSM(t *testing.T) (*StateManager, string, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "octl-int.sock")
	database := setupDBWithData(t, nil)

	sm := NewStateManagerWithSocket(database, sockPath)
	cleanup := func() {
		sm.Close()
		database.Close()
	}
	return sm, sockPath, cleanup
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

// TestIntegration_SocketSessionStatusBusy verifies that writing a
// session.status event with status.type=busy over the Unix socket results
// in a snapshot containing a session with StatusBusy.
func TestIntegration_SocketSessionStatusBusy(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writeEvent(t, conn, "session.status", map[string]interface{}{
		"sessionID": "test1",
		"status":    map[string]interface{}{"type": "busy"},
	})

	state := waitState(t, sm.SnapshotCh(), "test1", time.Second, func(*SessionState) bool { return true })
	if state.Status != StatusBusy {
		t.Errorf("Status = %q, want %q", state.Status, StatusBusy)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
}

// TestIntegration_SocketSessionIdle verifies that a session.idle event
// produces a snapshot entry with StatusIdle.
func TestIntegration_SocketSessionIdle(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writeEvent(t, conn, "session.idle", map[string]interface{}{
		"sessionID": "idle-session",
	})

	state := waitState(t, sm.SnapshotCh(), "idle-session", time.Second, func(*SessionState) bool { return true })
	if state.Status != StatusIdle {
		t.Errorf("Status = %q, want %q", state.Status, StatusIdle)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
}

// TestIntegration_SocketPermissionUpdated verifies that a permission.updated
// event produces StatusPermission with the correct permType and permTitle.
func TestIntegration_SocketPermissionUpdated(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writeEvent(t, conn, "permission.updated", map[string]interface{}{
		"sessionID": "perm-session",
		"permType":  "bash",
		"permTitle": "Run bash: npm test",
	})

	state := waitState(t, sm.SnapshotCh(), "perm-session", time.Second, func(*SessionState) bool { return true })
	if state.Status != StatusPermission {
		t.Errorf("Status = %q, want %q", state.Status, StatusPermission)
	}
	if state.PermType != "bash" {
		t.Errorf("PermType = %q, want %q", state.PermType, "bash")
	}
	if state.PermTitle != "Run bash: npm test" {
		t.Errorf("PermTitle = %q, want %q", state.PermTitle, "Run bash: npm test")
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
}

// TestIntegration_SocketSessionError verifies that a session.error event
// produces StatusError with the correct error message.
func TestIntegration_SocketSessionError(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writeEvent(t, conn, "session.error", map[string]interface{}{
		"sessionID": "err-session",
		"error":     "connection timeout",
	})

	state := waitState(t, sm.SnapshotCh(), "err-session", time.Second, func(*SessionState) bool { return true })
	if state.Status != StatusError {
		t.Errorf("Status = %q, want %q", state.Status, StatusError)
	}
	if state.ErrorMsg != "connection timeout" {
		t.Errorf("ErrorMsg = %q, want %q", state.ErrorMsg, "connection timeout")
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
}

// TestIntegration_SocketSessionDeleted verifies that sending a
// session.created event followed by a session.deleted event first creates
// an entry in the snapshot and then removes it.
func TestIntegration_SocketSessionDeleted(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Create the session.
	writeEvent(t, conn, "session.created", map[string]interface{}{
		"sessionID": "del-session",
		"title":     "To Be Deleted",
		"projectID": "proj-d",
	})

	state := waitState(t, sm.SnapshotCh(), "del-session", time.Second, func(*SessionState) bool { return true })
	if state.Title != "To Be Deleted" {
		t.Errorf("Title = %q, want %q", state.Title, "To Be Deleted")
	}

	// Delete the session.
	writeEvent(t, conn, "session.deleted", map[string]interface{}{
		"sessionID": "del-session",
	})

	waitStateGone(t, sm.SnapshotCh(), "del-session", time.Second)
}

// TestIntegration_SocketMultipleEventsSameSession verifies that sending
// multiple events for the same session applies transitions correctly and
// the final snapshot reflects the last status.
func TestIntegration_SocketMultipleEventsSameSession(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send idle first.
	writeEvent(t, conn, "session.idle", map[string]interface{}{
		"sessionID": "multi",
	})

	waitState(t, sm.SnapshotCh(), "multi", time.Second, func(s *SessionState) bool {
		return s.Status == StatusIdle
	})

	// Then send busy — final state must be BUSY.
	writeEvent(t, conn, "session.status", map[string]interface{}{
		"sessionID": "multi",
		"status":    map[string]interface{}{"type": "busy"},
	})

	waitState(t, sm.SnapshotCh(), "multi", time.Second, func(s *SessionState) bool {
		return s.Status == StatusBusy
	})
}

// TestIntegration_SocketMultipleSessions verifies that events from
// different sessions are all tracked in the same snapshot.
func TestIntegration_SocketMultipleSessions(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send events for session A and B.
	writeEvent(t, conn, "session.status", map[string]interface{}{
		"sessionID": "session-a",
		"status":    map[string]interface{}{"type": "busy"},
	})
	writeEvent(t, conn, "session.idle", map[string]interface{}{
		"sessionID": "session-b",
	})

	// Events may straddle multiple snapshots (first-frame broadcast can
	// preempt the event frames), and both sessions can land in the SAME
	// frame — so wait with a per-frame predicate instead of consuming
	// frames one session at a time.
	deadline := time.After(2 * time.Second)
	for {
		var stateA, stateB *SessionState
		select {
		case snap := <-sm.SnapshotCh():
			stateA = findState(snap, "session-a")
			stateB = findState(snap, "session-b")
		case <-deadline:
			t.Fatalf("timed out waiting for session-a(BUSY) and session-b(IDLE)")
		}
		if stateA != nil && stateA.Status == StatusBusy && stateB != nil && stateB.Status == StatusIdle {
			return
		}
	}
}

// TestDaemon_ListSessionsOverSocket verifies that a client can request the
// sidebar session list over the Unix socket and receive a populated response.
func TestDaemon_ListSessionsOverSocket(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('ses_root', 'global', '', 'root', '/home/user', 'Root Session', '1.0',
			1, 2000, 0)`)
	})
	defer database.Close()

	sockPath := filepath.Join(t.TempDir(), "octl.sock")
	sm := NewStateManagerWithSocket(database, sockPath)

	errCh := make(chan error, 1)
	go func() {
		errCh <- sm.Run()
	}()

	// Wait for listener to start.
	time.Sleep(100 * time.Millisecond)

	client := NewSocketClientWithSocket(sockPath)
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	if err := client.Subscribe(); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := client.RequestListSessions("req-1"); err != nil {
		t.Fatalf("request listSessions: %v", err)
	}

	var gotResponse bool
	for msg := range client.Msgs() {
		resp, ok := msg.(ResponseMsg)
		if !ok {
			continue
		}
		if resp.ID == "req-1" {
			gotResponse = true
			if !resp.Ok {
				t.Fatalf("listSessions failed: %s", resp.Error)
			}
			if resp.Projects == nil {
				t.Fatal("expected Projects in response")
			}
			if len(resp.Projects) != 1 {
				t.Fatalf("expected 1 project, got %d", len(resp.Projects))
			}
			if len(resp.Projects[0].Sessions) != 1 {
				t.Fatalf("expected 1 session, got %d", len(resp.Projects[0].Sessions))
			}
			break
		}
	}
	if !gotResponse {
		t.Fatal("did not receive listSessions response")
	}

	sm.Close()
	<-errCh
}

// TestIntegration_DBSyncOverwritesStaleEvents verifies that once an
// event-derived state exceeds the stale threshold, the periodic DB sync
// takes over and the status reverts to what the database indicates.
func TestIntegration_DBSyncOverwritesStaleEvents(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "octl-stale.sock")

	// Create a DB with a session that has a completed assistant message, so
	// deriveFromDB returns StatusIdle.
	database := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) VALUES ('db-test', 'global', 'test', '/tmp', 'DB Session', '1.0', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 'db-test', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})

	sm := NewStateManagerWithSocket(database, sockPath)
	// Override intervals so the test completes quickly.
	sm.eventStaleAfter = 100 * time.Millisecond
	sm.dbSyncInterval = 50 * time.Millisecond

	cleanup := func() {
		sm.Close()
		database.Close()
	}
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send a busy event — state should immediately become BUSY.
	writeEvent(t, conn, "session.status", map[string]interface{}{
		"sessionID": "db-test",
		"status":    map[string]interface{}{"type": "busy"},
	})

	state := waitState(t, sm.SnapshotCh(), "db-test", time.Second, func(s *SessionState) bool {
		return s.Status == StatusBusy
	})
	if state.Source != SourceEvent {
		t.Errorf("expected Source=EVENT after event, got %q", state.Source)
	}

	// Wait long enough for the event to become stale and at least one DB
	// sync cycle to take over.
	time.Sleep(400 * time.Millisecond)

	// Drain any intermediate snapshots from intermediate sync ticks.
	drainSnapshots(sm.SnapshotCh())

	// Wait for one more sync cycle to guarantee a fresh snapshot.
	time.Sleep(100 * time.Millisecond)

	state = waitState(t, sm.SnapshotCh(), "db-test", 2*time.Second, func(s *SessionState) bool {
		return s.Status == StatusIdle
	})
	if state.Source != SourceDB {
		t.Errorf("expected Source=DB after stale takeover, got %q", state.Source)
	}
}

// readView reads one ViewMsg from the client message channel with a timeout.
func readView(t *testing.T, msgs <-chan interface{}, timeout time.Duration) *ViewMsg {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg := <-msgs:
			if view, ok := msg.(ViewMsg); ok {
				return &view
			}
		case <-deadline:
			t.Fatalf("timed out waiting for view message (%v)", timeout)
		}
	}
}

// TestIntegration_ProcessInfoInSnapshot verifies that pid/tmuxPane/tmuxSession
// sent by an event source are reflected in the broadcast snapshot.
func TestIntegration_ProcessInfoInSnapshot(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writeEvent(t, conn, "session.status", map[string]interface{}{
		"sessionID":   "proc-session",
		"status":      map[string]interface{}{"type": "busy"},
		"pid":         12345,
		"tmuxPane":    "%7",
		"tmuxSession": "$3",
	})

	state := waitState(t, sm.SnapshotCh(), "proc-session", time.Second, func(s *SessionState) bool {
		return s.ProcessInfo != nil
	})
	if state.ProcessInfo == nil {
		t.Fatal("expected ProcessInfo in snapshot")
	}
	if state.ProcessInfo.PID != 12345 {
		t.Errorf("PID = %d, want 12345", state.ProcessInfo.PID)
	}
	if state.ProcessInfo.TMUXPane != "%7" {
		t.Errorf("TMUXPane = %q, want %%7", state.ProcessInfo.TMUXPane)
	}
	if state.ProcessInfo.TMUXSession != "$3" {
		t.Errorf("TMUXSession = %q, want $3", state.ProcessInfo.TMUXSession)
	}
}

// TestIntegration_ProcessInfoClearedOnDisconnect verifies that when the
// event-source connection closes, the process info for its sessions is removed
// from subsequent snapshots.
func TestIntegration_ProcessInfoClearedOnDisconnect(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	writeEvent(t, conn, "session.status", map[string]interface{}{
		"sessionID":   "proc-session",
		"status":      map[string]interface{}{"type": "busy"},
		"pid":         12345,
		"tmuxPane":    "%7",
		"tmuxSession": "$3",
	})

	state := waitState(t, sm.SnapshotCh(), "proc-session", time.Second, func(s *SessionState) bool {
		return s.ProcessInfo != nil
	})
	if state == nil || state.ProcessInfo == nil {
		t.Fatal("expected ProcessInfo before disconnect")
	}

	// Close the event-source connection and wait for the daemon to notice.
	conn.Close()
	time.Sleep(200 * time.Millisecond)

	// Trigger another event for an unrelated session to force a snapshot push.
	conn2, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial second source: %v", err)
	}
	defer conn2.Close()

	writeEvent(t, conn2, "session.idle", map[string]interface{}{
		"sessionID": "other-session",
	})

	waitState(t, sm.SnapshotCh(), "proc-session", time.Second, func(s *SessionState) bool {
		return s.ProcessInfo == nil
	})
}

// TestIntegration_ViewIncludesProcessInfo verifies that a subscriber connected
// to the "view" channel receives ViewSession entries with tmux context.
func TestIntegration_ViewIncludesProcessInfo(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('view-proc', 'global', '', 'view-proc', '/home/user', 'View Proc', '1.0',
			1, 2000, 0)`)
	})
	defer database.Close()

	sockPath := filepath.Join(t.TempDir(), "octl-view.sock")
	sm := NewStateManagerWithSocket(database, sockPath)

	errCh := make(chan error, 1)
	go func() {
		errCh <- sm.Run()
	}()
	time.Sleep(100 * time.Millisecond)

	// Event source connection.
	src, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial source: %v", err)
	}
	defer src.Close()

	writeEvent(t, src, "session.status", map[string]interface{}{
		"sessionID":   "view-proc",
		"status":      map[string]interface{}{"type": "busy"},
		"pid":         777,
		"tmuxPane":    "%9",
		"tmuxSession": "$4",
	})

	// Subscriber connection.
	client := NewSocketClientWithSocket(sockPath)
	if err := client.Connect(); err != nil {
		t.Fatalf("connect subscriber: %v", err)
	}
	defer client.Close()
	if err := client.SubscribeView(); err != nil {
		t.Fatalf("subscribe to view: %v", err)
	}

	view := readView(t, client.Msgs(), 2*time.Second)
	if view == nil {
		t.Fatal("expected view message")
	}
	if len(view.Projects) != 1 || len(view.Projects[0].Sessions) != 1 {
		t.Fatalf("expected 1 project with 1 session, got %+v", view.Projects)
	}
	s := view.Projects[0].Sessions[0]
	if s.SessionID != "view-proc" {
		t.Errorf("session id = %q, want view-proc", s.SessionID)
	}
	if s.PID != 777 {
		t.Errorf("PID = %d, want 777", s.PID)
	}
	if s.TMUXPane != "%9" {
		t.Errorf("TMUXPane = %q, want %%9", s.TMUXPane)
	}
	if s.TMUXSession != "$4" {
		t.Errorf("TMUXSession = %q, want $4", s.TMUXSession)
	}

	sm.Close()
	<-errCh
}
