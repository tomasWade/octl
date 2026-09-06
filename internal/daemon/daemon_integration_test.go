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

// readSnapshot reads one snapshot from the snapshot channel with a timeout.
func readSnapshot(t *testing.T, ch <-chan []SessionState, timeout time.Duration) []SessionState {
	t.Helper()
	select {
	case snap := <-ch:
		return snap
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for snapshot (%v)", timeout)
		return nil
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "test1")
	if state == nil {
		t.Fatal("session test1 not found in snapshot")
	}
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "idle-session")
	if state == nil {
		t.Fatal("session idle-session not found in snapshot")
	}
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "perm-session")
	if state == nil {
		t.Fatal("session perm-session not found in snapshot")
	}
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "err-session")
	if state == nil {
		t.Fatal("session err-session not found in snapshot")
	}
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "del-session")
	if state == nil {
		t.Fatal("del-session should exist after session.created")
	}
	if state.Title != "To Be Deleted" {
		t.Errorf("Title = %q, want %q", state.Title, "To Be Deleted")
	}

	// Delete the session.
	writeEvent(t, conn, "session.deleted", map[string]interface{}{
		"sessionID": "del-session",
	})

	snap = readSnapshot(t, sm.SnapshotCh(), time.Second)
	if state := findState(snap, "del-session"); state != nil {
		t.Error("del-session should be removed from snapshot after session.deleted")
	}
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "multi")
	if state == nil {
		t.Fatal("session multi not found after idle event")
	}
	if state.Status != StatusIdle {
		t.Errorf("after idle: Status = %q, want %q", state.Status, StatusIdle)
	}

	// Then send busy — final state must be BUSY.
	writeEvent(t, conn, "session.status", map[string]interface{}{
		"sessionID": "multi",
		"status":    map[string]interface{}{"type": "busy"},
	})

	snap = readSnapshot(t, sm.SnapshotCh(), time.Second)
	state = findState(snap, "multi")
	if state == nil {
		t.Fatal("session multi not found after busy event")
	}
	if state.Status != StatusBusy {
		t.Errorf("after busy: Status = %q, want %q", state.Status, StatusBusy)
	}
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

	// The event loop processes events in order and pushes one snapshot
	// after draining all pending events, so we only need to read once.
	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)

	stateA := findState(snap, "session-a")
	if stateA == nil {
		t.Fatal("session-a not found in snapshot")
	}
	if stateA.Status != StatusBusy {
		t.Errorf("session-a Status = %q, want %q", stateA.Status, StatusBusy)
	}

	stateB := findState(snap, "session-b")
	if stateB == nil {
		t.Fatal("session-b not found in snapshot")
	}
	if stateB.Status != StatusIdle {
		t.Errorf("session-b Status = %q, want %q", stateB.Status, StatusIdle)
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "db-test")
	if state == nil {
		t.Fatal("db-test not found after busy event")
	}
	if state.Status != StatusBusy {
		t.Fatalf("expected BUSY after event, got %q", state.Status)
	}
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

	snap = readSnapshot(t, sm.SnapshotCh(), 2*time.Second)
	state = findState(snap, "db-test")
	if state == nil {
		t.Fatal("db-test should still exist after DB sync")
	}
	if state.Status != StatusIdle {
		t.Errorf("expected IDLE after stale DB takeover, got %q", state.Status)
	}
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "proc-session")
	if state == nil {
		t.Fatal("proc-session not found in snapshot")
	}
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

	snap := readSnapshot(t, sm.SnapshotCh(), time.Second)
	if state := findState(snap, "proc-session"); state == nil || state.ProcessInfo == nil {
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

	snap = readSnapshot(t, sm.SnapshotCh(), time.Second)
	state := findState(snap, "proc-session")
	if state == nil {
		t.Fatal("proc-session should still exist after disconnect")
	}
	if state.ProcessInfo != nil {
		t.Errorf("ProcessInfo should be cleared after disconnect, got %+v", state.ProcessInfo)
	}
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
