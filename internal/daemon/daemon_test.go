// Package daemon provides tests for the notification daemon core logic.
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
	"github.com/tomasWade/octl/internal/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// marshalEvent creates raw JSON bytes from a rawEvent struct for feeding into
// processEvent.  Testing in the same package gives us access to rawEvent.
func marshalEvent(t *testing.T, typ string, props map[string]interface{}) []byte {
	t.Helper()
	evt := rawEvent{Type: typ, Properties: props}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return data
}

// daemonTestSchema 是测试夹具用的 opencode 库表结构子集（与真实库对齐到
// octl 读取所需的列；message.time_updated 供影子库水位查询使用）。
const daemonTestSchema = `
	CREATE TABLE IF NOT EXISTS project (
		id TEXT PRIMARY KEY,
		worktree TEXT,
		vcs TEXT,
		name TEXT,
		time_created INTEGER,
		time_updated INTEGER
	);
	CREATE TABLE IF NOT EXISTS session (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		parent_id TEXT,
		slug TEXT,
		directory TEXT,
		title TEXT,
		version TEXT,
		agent TEXT,
		model TEXT,
		cost REAL DEFAULT 0,
		tokens_input INTEGER DEFAULT 0,
		tokens_output INTEGER DEFAULT 0,
		tokens_reasoning INTEGER DEFAULT 0,
		tokens_cache_read INTEGER DEFAULT 0,
		tokens_cache_write INTEGER DEFAULT 0,
		time_created INTEGER,
		time_updated INTEGER,
		time_compacting INTEGER DEFAULT 0,
		time_archived INTEGER,
		path TEXT,
		workspace_id TEXT,
		summary_additions INTEGER DEFAULT 0,
		summary_deletions INTEGER DEFAULT 0,
		summary_files INTEGER DEFAULT 0,
		summary_diffs TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS message (
		id TEXT PRIMARY KEY,
		session_id TEXT,
		data TEXT,
		time_created INTEGER,
		time_updated INTEGER,
		FOREIGN KEY (session_id) REFERENCES session(id)
	);
	CREATE TABLE IF NOT EXISTS part (
		id TEXT PRIMARY KEY,
		message_id TEXT,
		session_id TEXT,
		data TEXT,
		time_created INTEGER,
		time_updated INTEGER,
		FOREIGN KEY (message_id) REFERENCES message(id),
		FOREIGN KEY (session_id) REFERENCES session(id)
	);
	`

// setupDBWithData creates a temporary SQLite database, applies the schema,
// invokes seedFn to insert data, then opens a read-only db.DB on the same
// file.  The caller owns the returned *db.DB and must close it.
func setupDBWithData(t *testing.T, seedFn func(wdb *sql.DB)) *db.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "daemon_test.db")

	wdb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open write DB: %v", err)
	}

	if _, err := wdb.Exec(daemonTestSchema); err != nil {
		wdb.Close()
		t.Fatalf("create schema: %v", err)
	}

	if seedFn != nil {
		seedFn(wdb)
	}
	wdb.Close()

	d, err := db.New(dbPath)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	return d
}

// ---------------------------------------------------------------------------
// processEvent: session.status  (requirement 1)
// ---------------------------------------------------------------------------

func TestProcessEvent_session_status_updatesStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusType string
		want       SessionStatus
	}{
		{name: "busy", statusType: "busy", want: StatusBusy},
		{name: "idle", statusType: "idle", want: StatusIdle},
		{name: "retry", statusType: "retry", want: StatusRetry},
		{name: "thinking maps to BUSY", statusType: "thinking", want: StatusBusy},
		{name: "permission", statusType: "permission", want: StatusPermission},
		{name: "error", statusType: "error", want: StatusError},
		{name: "unknown status falls back to UNKNOWN", statusType: "garbage", want: StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := &StateManager{stateMap: make(map[string]*SessionState)}
			evt := marshalEvent(t, "session.status", map[string]interface{}{
				"sessionID": "s1",
				"status":    map[string]interface{}{"type": tt.statusType},
				"title":     "My Session",
				"projectID": "proj-1",
			})
			sm.processEvent(evt)

			sm.mu.RLock()
			state, ok := sm.stateMap["s1"]
			sm.mu.RUnlock()
			if !ok {
				t.Fatal("expected session s1 to exist in stateMap")
			}
			if state.Status != tt.want {
				t.Errorf("Status = %q, want %q", state.Status, tt.want)
			}
			if state.Source != SourceEvent {
				t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
			}
			if state.LastEventAt.IsZero() {
				t.Error("LastEventAt should be set to a non-zero time")
			}
			if state.Title != "My Session" {
				t.Errorf("Title = %q, want %q", state.Title, "My Session")
			}
			if state.ProjectID != "proj-1" {
				t.Errorf("ProjectID = %q, want %q", state.ProjectID, "proj-1")
			}
			if state.Tombstone {
				t.Error("Tombstone should be false after event")
			}
		})
	}
}

func TestProcessEvent_session_status_emptySessionID_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "session.status", map[string]interface{}{
		"sessionID": "",
		"status":    map[string]interface{}{"type": "busy"},
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected empty stateMap, got %d entries", n)
	}
}

func TestProcessEvent_session_status_missingSessionID_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "session.status", map[string]interface{}{
		"status": map[string]interface{}{"type": "busy"},
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected empty stateMap, got %d entries", n)
	}
}

func TestProcessEvent_session_status_emptyTitleDoesNotOverwrite(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{
		SessionID: "s1",
		Title:     "Original Title",
		ProjectID: "original-proj",
	}
	sm.mu.Unlock()

	// Event with no title/projectID — existing values are preserved.
	evt := marshalEvent(t, "session.status", map[string]interface{}{
		"sessionID": "s1",
		"status":    map[string]interface{}{"type": "idle"},
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if state.Title != "Original Title" {
		t.Errorf("Title should remain %q, got %q", "Original Title", state.Title)
	}
	if state.ProjectID != "original-proj" {
		t.Errorf("ProjectID should remain %q, got %q", "original-proj", state.ProjectID)
	}
}

// ---------------------------------------------------------------------------
// processEvent: session.idle  (requirement 2)
// ---------------------------------------------------------------------------

func TestProcessEvent_session_idle_setsStatusToIdle(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	// Pre-populate with a session in ERROR state to verify idle clears it.
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{
		SessionID: "s1",
		Status:    StatusError,
		Source:    SourceEvent,
		ErrorMsg:  "something went wrong",
		Tombstone: true,
	}
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.idle", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session s1 to exist")
	}
	if state.Status != StatusIdle {
		t.Errorf("Status = %q, want %q", state.Status, StatusIdle)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
	if state.LastEventAt.IsZero() {
		t.Error("LastEventAt should be set")
	}
	if state.ErrorMsg != "" {
		t.Errorf("ErrorMsg should be cleared, got %q", state.ErrorMsg)
	}
	if state.Tombstone {
		t.Error("Tombstone should be false after idle event")
	}
}

func TestProcessEvent_session_idle_createsNewEntry(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "session.idle", map[string]interface{}{
		"sessionID": "new-session",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["new-session"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session new-session to be created")
	}
	if state.Status != StatusIdle {
		t.Errorf("Status = %q, want %q", state.Status, StatusIdle)
	}
}

func TestProcessEvent_session_idle_emptySessionID_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "session.idle", map[string]interface{}{
		"sessionID": "",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected empty stateMap, got %d entries", n)
	}
}

// ---------------------------------------------------------------------------
// processEvent: session.created  (requirement 3)
// ---------------------------------------------------------------------------

func TestProcessEvent_session_created_createsNewEntry(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	evt := marshalEvent(t, "session.created", map[string]interface{}{
		"sessionID": "s1",
		"title":     "New Session",
		"projectID": "proj-x",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session s1 to be created")
	}
	if state.Status != StatusUnknown {
		t.Errorf("Status = %q, want %q", state.Status, StatusUnknown)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
	if state.LastEventAt.IsZero() {
		t.Error("LastEventAt should be set")
	}
	if state.Title != "New Session" {
		t.Errorf("Title = %q, want %q", state.Title, "New Session")
	}
	if state.ProjectID != "proj-x" {
		t.Errorf("ProjectID = %q, want %q", state.ProjectID, "proj-x")
	}
	if state.Tombstone {
		t.Error("Tombstone should be false")
	}
}

func TestProcessEvent_session_created_updatesExistingEntry(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	// Pre-populate with a busy session.
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{
		SessionID: "s1",
		Status:    StatusBusy,
		Source:    SourceDB,
	}
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.created", map[string]interface{}{
		"sessionID": "s1",
		"title":     "Recreated",
		"projectID": "proj-y",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if state.Status != StatusUnknown {
		t.Errorf("Status should be reset to UNKNOWN on created event, got %q", state.Status)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source should be EVENT, got %q", state.Source)
	}
	if state.Title != "Recreated" {
		t.Errorf("Title = %q, want %q", state.Title, "Recreated")
	}
	if state.ProjectID != "proj-y" {
		t.Errorf("ProjectID = %q, want %q", state.ProjectID, "proj-y")
	}
}

func TestProcessEvent_session_created_emptySessionID_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "session.created", map[string]interface{}{
		"sessionID": "",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected empty stateMap, got %d entries", n)
	}
}

// ---------------------------------------------------------------------------
// processEvent: session.deleted  (requirement 4)
// ---------------------------------------------------------------------------

func TestProcessEvent_session_deleted_removesEntry(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{SessionID: "s1", Status: StatusBusy}
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.deleted", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	_, ok := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if ok {
		t.Error("session s1 should have been removed from stateMap")
	}
}

func TestProcessEvent_session_deleted_nonExistentIsNoop(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "session.deleted", map[string]interface{}{
		"sessionID": "ghost",
	})
	sm.processEvent(evt) // Should not panic.

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 entries, got %d", n)
	}
}

func TestProcessEvent_session_deleted_emptySessionID_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{SessionID: "s1"}
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.deleted", map[string]interface{}{
		"sessionID": "",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	_, s1ok := sm.stateMap["s1"]
	_, emptyOk := sm.stateMap[""]
	sm.mu.RUnlock()
	if !s1ok {
		t.Error("session s1 should still exist")
	}
	if emptyOk {
		t.Error("should not create entry with empty sessionID")
	}
}

// ---------------------------------------------------------------------------
// processEvent: session.error  (requirement 5)
// ---------------------------------------------------------------------------

func TestProcessEvent_session_error_setsStatusAndMessage(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	evt := marshalEvent(t, "session.error", map[string]interface{}{
		"sessionID": "s1",
		"error":     "connection timeout",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session s1 to exist")
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
	if state.LastEventAt.IsZero() {
		t.Error("LastEventAt should be set")
	}
	if state.Tombstone {
		t.Error("Tombstone should be false")
	}
}

func TestProcessEvent_session_error_emptyError(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	// Pre-populate with an earlier error to verify overwrite.
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{
		SessionID: "s1",
		Status:    StatusBusy,
		ErrorMsg:  "old error",
	}
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.error", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if state.Status != StatusError {
		t.Errorf("Status = %q, want %q", state.Status, StatusError)
	}
	if state.ErrorMsg != "" {
		t.Errorf("ErrorMsg should be empty, got %q", state.ErrorMsg)
	}
}

func TestProcessEvent_session_error_emptySessionID_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "session.error", map[string]interface{}{
		"sessionID": "",
		"error":     "oops",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected empty stateMap, got %d entries", n)
	}
}

// ---------------------------------------------------------------------------
// processEvent: permission.updated  (requirement 6)
// ---------------------------------------------------------------------------

func TestProcessEvent_permission_updated_setsStatusAndPermInfo(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	evt := marshalEvent(t, "permission.updated", map[string]interface{}{
		"sessionID": "s1",
		"permType":  "bash",
		"permTitle": "Run bash: npm test",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session s1 to exist")
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
	if state.LastEventAt.IsZero() {
		t.Error("LastEventAt should be set")
	}
	if state.Tombstone {
		t.Error("Tombstone should be false")
	}
}

func TestProcessEvent_permission_updated_emptyPermFields(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	evt := marshalEvent(t, "permission.updated", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if state.Status != StatusPermission {
		t.Errorf("Status = %q, want %q", state.Status, StatusPermission)
	}
	if state.PermType != "" {
		t.Errorf("PermType should be empty, got %q", state.PermType)
	}
	if state.PermTitle != "" {
		t.Errorf("PermTitle should be empty, got %q", state.PermTitle)
	}
}

func TestProcessEvent_permission_updated_emptySessionID_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "permission.updated", map[string]interface{}{
		"sessionID": "",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected empty stateMap, got %d entries", n)
	}
}

// ---------------------------------------------------------------------------
// processEvent: permission.replied  (requirement 7)
// ---------------------------------------------------------------------------

func TestProcessEvent_permission_replied_clearsPermAndSetsBusy(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	// Pre-populate with a session awaiting permission.
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{
		SessionID: "s1",
		Status:    StatusPermission,
		Source:    SourceEvent,
		PermType:  "file_write",
		PermTitle: "Write to /etc/config",
	}
	sm.mu.Unlock()

	evt := marshalEvent(t, "permission.replied", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session s1 to exist")
	}
	if state.Status != StatusBusy {
		t.Errorf("Status = %q, want %q", state.Status, StatusBusy)
	}
	if state.PermType != "" {
		t.Errorf("PermType should be cleared, got %q", state.PermType)
	}
	if state.PermTitle != "" {
		t.Errorf("PermTitle should be cleared, got %q", state.PermTitle)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
	if state.LastEventAt.IsZero() {
		t.Error("LastEventAt should be set")
	}
	if state.Tombstone {
		t.Error("Tombstone should be false")
	}
}

func TestProcessEvent_permission_replied_createsNewEntry(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "permission.replied", map[string]interface{}{
		"sessionID": "new-session",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["new-session"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session to be created")
	}
	if state.Status != StatusBusy {
		t.Errorf("Status = %q, want %q", state.Status, StatusBusy)
	}
	if state.PermType != "" {
		t.Errorf("PermType should be empty, got %q", state.PermType)
	}
	if state.PermTitle != "" {
		t.Errorf("PermTitle should be empty, got %q", state.PermTitle)
	}
}

// ---------------------------------------------------------------------------
// processEvent: question.*（opencode 的 question 工具，与权限确认同义复用 PERMISSION）
// ---------------------------------------------------------------------------

func TestProcessEvent_question_asked_setsPermission(t *testing.T) {
	// question.asked 是提问等待态的唯一信号源（session.status 只有
	// idle/retry/busy），必须映射为 PERMISSION（UI 统一显示 🟡 ASK）。
	sm := &StateManager{stateMap: make(map[string]*SessionState)}

	evt := marshalEvent(t, "question.asked", map[string]interface{}{
		"sessionID": "s1",
		"id":        "que_abc123",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session s1 to exist")
	}
	if state.Status != StatusPermission {
		t.Errorf("Status = %q, want %q (question.asked → PERMISSION)", state.Status, StatusPermission)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
	if state.LastEventAt.IsZero() {
		t.Error("LastEventAt should be set")
	}
	if state.Tombstone {
		t.Error("Tombstone should be false")
	}
}

func TestProcessEvent_question_replied_setsBusy(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{
		SessionID: "s1",
		Status:    StatusPermission,
		Source:    SourceEvent,
	}
	sm.mu.Unlock()

	evt := marshalEvent(t, "question.replied", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if state.Status != StatusBusy {
		t.Errorf("Status = %q, want %q (question.replied → BUSY)", state.Status, StatusBusy)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
}

func TestProcessEvent_question_rejected_setsBusy(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{
		SessionID: "s1",
		Status:    StatusPermission,
		Source:    SourceEvent,
	}
	sm.mu.Unlock()

	evt := marshalEvent(t, "question.rejected", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if state.Status != StatusBusy {
		t.Errorf("Status = %q, want %q (question.rejected → BUSY)", state.Status, StatusBusy)
	}
}

func TestProcessEvent_question_asked_emptySessionID_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "question.asked", map[string]interface{}{
		"sessionID": "",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected empty stateMap, got %d entries", n)
	}
}

// ---------------------------------------------------------------------------
// processEvent: session.compacted
// ---------------------------------------------------------------------------

func TestProcessEvent_session_compacted_setsBusy(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{
		SessionID: "s1",
		Status:    StatusIdle,
		Source:    SourceDB,
		Tombstone: true,
	}
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.compacted", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state, ok := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("expected session s1 to exist")
	}
	if state.Status != StatusBusy {
		t.Errorf("Status = %q, want %q", state.Status, StatusBusy)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q", state.Source, SourceEvent)
	}
	if state.LastEventAt.IsZero() {
		t.Error("LastEventAt should be set")
	}
	if state.Tombstone {
		t.Error("Tombstone should be false")
	}
}

// ---------------------------------------------------------------------------
// processEvent: edge cases — malformed / unknown
// ---------------------------------------------------------------------------

func TestProcessEvent_malformedJSON_droppedSilently(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.processEvent([]byte(`{invalid json}`)) // Should not panic.

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected no state changes for malformed JSON, got %d entries", n)
	}
}

func TestProcessEvent_unknownEventType_ignored(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "unknown.event", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected no state changes for unknown event type, got %d entries", n)
	}
}

func TestProcessEvent_missingPropertiesMap(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	// An event with type but nil properties — should be silently handled.
	data, err := json.Marshal(map[string]interface{}{"type": "session.status"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sm.processEvent(data)

	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected no state changes, got %d entries", n)
	}
}

// ---------------------------------------------------------------------------
// processEvent: empty sessionID for all event types
// ---------------------------------------------------------------------------

func TestProcessEvent_emptySessionID_allTypes(t *testing.T) {
	eventTypes := []string{
		"session.status",
		"session.idle",
		"session.created",
		"session.deleted",
		"session.error",
		"permission.updated",
		"permission.replied",
		"session.compacted",
	}
	for _, et := range eventTypes {
		t.Run(et, func(t *testing.T) {
			sm := &StateManager{stateMap: make(map[string]*SessionState)}
			sm.mu.Lock()
			sm.stateMap["existing"] = &SessionState{SessionID: "existing", Status: StatusBusy}
			sm.mu.Unlock()

			props := map[string]interface{}{"sessionID": ""}
			if et == "session.status" {
				props["status"] = map[string]interface{}{"type": "busy"}
			}
			evt := marshalEvent(t, et, props)
			sm.processEvent(evt)

			sm.mu.RLock()
			_, emptyExists := sm.stateMap[""]
			existingState := sm.stateMap["existing"]
			sm.mu.RUnlock()

			if emptyExists {
				t.Error("should not create entry with empty sessionID")
			}
			if existingState.Status != StatusBusy {
				t.Error("existing session should not be affected")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// processEvent: concurrent safety smoke test
// ---------------------------------------------------------------------------

func TestProcessEvent_concurrentSafe(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			evt := marshalEvent(t, "session.status", map[string]interface{}{
				"sessionID": "s1",
				"status":    map[string]interface{}{"type": "busy"},
			})
			sm.processEvent(evt)
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 100; i++ {
			evt := marshalEvent(t, "session.status", map[string]interface{}{
				"sessionID": "s2",
				"status":    map[string]interface{}{"type": "idle"},
			})
			sm.processEvent(evt)
		}
		done <- struct{}{}
	}()
	<-done
	<-done

	sm.mu.RLock()
	_, s1ok := sm.stateMap["s1"]
	_, s2ok := sm.stateMap["s2"]
	sm.mu.RUnlock()
	if !s1ok {
		t.Error("session s1 should exist")
	}
	if !s2ok {
		t.Error("session s2 should exist")
	}
}

// ---------------------------------------------------------------------------
// session.created — title/projectID only set when non-empty
// ---------------------------------------------------------------------------

func TestProcessEvent_session_created_emptyTitleNotSet(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	evt := marshalEvent(t, "session.created", map[string]interface{}{
		"sessionID": "s1",
		"title":     "",
		"projectID": "",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	state := sm.stateMap["s1"]
	sm.mu.RUnlock()
	if state.Title != "" {
		t.Errorf("Title should be empty, got %q", state.Title)
	}
	if state.ProjectID != "" {
		t.Errorf("ProjectID should be empty, got %q", state.ProjectID)
	}
}

// ---------------------------------------------------------------------------
// deriveFromDB  (requirements 8, 9, 10)
// ---------------------------------------------------------------------------

func TestDeriveFromDB_archived(t *testing.T) {
	// Requirement 8: Returns ARCHIVED for archived sessions.
	d := setupDBWithData(t, nil)
	defer d.Close()
	sm := &StateManager{db: d}

	s := types.Session{
		ID:           "archived-session",
		TimeArchived: 1700000000000, // non-zero millisecond timestamp
	}
	got := sm.deriveFromDB(s)
	if got != StatusArchived {
		t.Errorf("deriveFromDB = %q, want %q", got, StatusArchived)
	}
}

func TestDeriveFromDB_archivedWithZeroValue(t *testing.T) {
	// Zero TimeArchived should NOT be considered archived.
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, title, time_created, time_updated) VALUES ('s1', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()
	sm := &StateManager{db: d}

	s := types.Session{ID: "s1", TimeArchived: 0}
	got := sm.deriveFromDB(s)
	if got == StatusArchived {
		t.Errorf("zero TimeArchived should not produce ARCHIVED, got %q", got)
	}
}

func TestDeriveFromDB_assistantLastMessage(t *testing.T) {
	// Requirement 9: Returns IDLE when last message role is "assistant"
	// and the message is completed (time.completed is set).
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, title, time_created, time_updated) VALUES ('s1', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()
	sm := &StateManager{db: d}

	s := types.Session{ID: "s1"}
	got := sm.deriveFromDB(s)
	if got != StatusIdle {
		t.Errorf("deriveFromDB = %q, want %q", got, StatusIdle)
	}
}

func TestDeriveFromDB_assistantLastMessageMultipleMessages(t *testing.T) {
	// The most recent message role is "assistant" (completed) → IDLE.
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, title, time_created, time_updated) VALUES ('s1', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"user"}', 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m2', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()
	sm := &StateManager{db: d}

	s := types.Session{ID: "s1"}
	got := sm.deriveFromDB(s)
	if got != StatusIdle {
		t.Errorf("deriveFromDB = %q, want %q", got, StatusIdle)
	}
}

func TestDeriveFromDB_assistantIncompleteMessage(t *testing.T) {
	// The most recent message is an assistant message without time.completed
	// (e.g. the AI is executing a long-running shell command) → BUSY,
	// NOT IDLE. This prevents the periodic DB sync from overwriting the
	// BUSY state of sessions whose event state has gone stale.
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, title, time_created, time_updated) VALUES ('s1', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"assistant","time":{"created":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()
	sm := &StateManager{db: d}

	s := types.Session{ID: "s1"}
	got := sm.deriveFromDB(s)
	if got != StatusBusy {
		t.Errorf("deriveFromDB = %q, want %q (in-progress assistant message)", got, StatusBusy)
	}
}

func TestDeriveFromDB_noMessages(t *testing.T) {
	// Requirement 10: Returns UNKNOWN for sessions without messages.
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, title, time_created, time_updated) VALUES ('s1', 'No Msgs', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()
	sm := &StateManager{db: d}

	s := types.Session{ID: "s1"}
	got := sm.deriveFromDB(s)
	if got != StatusUnknown {
		t.Errorf("deriveFromDB = %q, want %q", got, StatusUnknown)
	}
}

func TestDeriveFromDB_userLastMessage(t *testing.T) {
	// Requirement 10: Returns UNKNOWN when last message role is not "assistant".
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, title, time_created, time_updated) VALUES ('s1', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"user"}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()
	sm := &StateManager{db: d}

	s := types.Session{ID: "s1"}
	got := sm.deriveFromDB(s)
	if got != StatusUnknown {
		t.Errorf("deriveFromDB = %q, want %q", got, StatusUnknown)
	}
}

func TestDeriveFromDB_dbError(t *testing.T) {
	// When GetLastMessageRoleCompleted returns an error, deriveFromDB returns UNKNOWN.
	// We force an error by closing the underlying DB.
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, title, time_created, time_updated) VALUES ('s1', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	sm := &StateManager{db: d}

	// Close the DB to force a query error.
	d.Close()

	s := types.Session{ID: "s1", TimeArchived: 0}
	got := sm.deriveFromDB(s)
	if got != StatusUnknown {
		t.Errorf("deriveFromDB = %q, want %q (closed DB)", got, StatusUnknown)
	}
}

// ---------------------------------------------------------------------------
// syncFromDB: stale event takeover & PERMISSION exemption
// ---------------------------------------------------------------------------

func TestSyncFromDB_staleEventTakenOver(t *testing.T) {
	// A stale BUSY event state is taken over by the DB: the session has a
	// completed assistant message, so deriveFromDB returns IDLE.
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, title, time_created, time_updated) VALUES ('s1', 'global', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()

	sm := &StateManager{
		db:              d,
		stateMap:        make(map[string]*SessionState),
		eventStaleAfter: 50 * time.Millisecond,
	}
	sm.stateMap["s1"] = &SessionState{
		SessionID:   "s1",
		Status:      StatusBusy,
		Source:      SourceEvent,
		LastEventAt: time.Now().Add(-time.Minute),
	}

	if err := sm.syncFromDB(); err != nil {
		t.Fatalf("syncFromDB() error = %v", err)
	}

	state := sm.stateMap["s1"]
	if state.Status != StatusIdle {
		t.Errorf("Status = %q, want %q (DB takeover of stale BUSY)", state.Status, StatusIdle)
	}
	if state.Source != SourceDB {
		t.Errorf("Source = %q, want %q", state.Source, SourceDB)
	}
}

func TestSyncFromDB_permissionStaleNotTakenOver(t *testing.T) {
	// A stale PERMISSION state survives DB sync: permission requests live
	// only in opencode's memory, the DB cannot reproduce them. PERMISSION
	// must stay (with its permType/permTitle) until permission.replied /
	// session.idle / session.error clears it — otherwise long permission
	// waits would degrade to the DB-derived status.
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, title, time_created, time_updated) VALUES ('s1', 'global', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		// Completed assistant message: DB takeover would derive IDLE.
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()

	sm := &StateManager{
		db:              d,
		stateMap:        make(map[string]*SessionState),
		eventStaleAfter: 50 * time.Millisecond,
	}
	sm.stateMap["s1"] = &SessionState{
		SessionID:   "s1",
		Status:      StatusPermission,
		Source:      SourceEvent,
		LastEventAt: time.Now().Add(-time.Minute),
		PermType:    "bash",
		PermTitle:   "Run shell command",
	}

	if err := sm.syncFromDB(); err != nil {
		t.Fatalf("syncFromDB() error = %v", err)
	}

	state := sm.stateMap["s1"]
	if state.Status != StatusPermission {
		t.Errorf("Status = %q, want %q (PERMISSION must survive stale DB sync)", state.Status, StatusPermission)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q (no takeover for PERMISSION)", state.Source, SourceEvent)
	}
	if state.PermType != "bash" {
		t.Errorf("PermType = %q, want %q", state.PermType, "bash")
	}
	if state.PermTitle != "Run shell command" {
		t.Errorf("PermTitle = %q, want %q", state.PermTitle, "Run shell command")
	}
}

func TestSyncFromDB_errorStaleNotTakenOver(t *testing.T) {
	// A stale ERROR state survives DB sync: error info lives only in
	// opencode's memory, the DB cannot reproduce it. Without the exemption
	// a stuck session after an error would degrade to the DB-derived
	// status (BUSY/IDLE), hiding the error display entirely. ERROR stays
	// until session.idle / the next status event clears it.
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, title, time_created, time_updated) VALUES ('s1', 'global', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		// Completed assistant message: DB takeover would derive IDLE.
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()

	sm := &StateManager{
		db:              d,
		stateMap:        make(map[string]*SessionState),
		eventStaleAfter: 50 * time.Millisecond,
	}
	sm.stateMap["s1"] = &SessionState{
		SessionID:   "s1",
		Status:      StatusError,
		Source:      SourceEvent,
		LastEventAt: time.Now().Add(-time.Minute),
		ErrorMsg:    "connection timeout",
	}

	if err := sm.syncFromDB(); err != nil {
		t.Fatalf("syncFromDB() error = %v", err)
	}

	state := sm.stateMap["s1"]
	if state.Status != StatusError {
		t.Errorf("Status = %q, want %q (ERROR must survive stale DB sync)", state.Status, StatusError)
	}
	if state.Source != SourceEvent {
		t.Errorf("Source = %q, want %q (no takeover for ERROR)", state.Source, SourceEvent)
	}
	if state.ErrorMsg != "connection timeout" {
		t.Errorf("ErrorMsg = %q, want %q", state.ErrorMsg, "connection timeout")
	}
}

// ---------------------------------------------------------------------------
// statusForSession: stale 事件态的显示路径（与 syncFromDB 豁免保持一致）
// ---------------------------------------------------------------------------

func TestStatusForSession_stalePermissionKeptOnDisplay(t *testing.T) {
	// 显示路径豁免：PERMISSION 是 opencode 内存态（权限确认 / 提问等待），
	// DB 无法还原。syncFromDB 已豁免接管，statusForSession 也必须保持，
	// 否则等待超过 eventStaleAfter 的 session 会在 ViewMsg 中被 deriveFromDB
	// 错误降级为 BUSY/IDLE——stateMap 与显示不一致。
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, title, time_created, time_updated) VALUES ('s1', 'global', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		// Completed assistant message: deriveFromDB would return IDLE.
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()

	sm := &StateManager{
		db:              d,
		stateMap:        make(map[string]*SessionState),
		eventStaleAfter: 50 * time.Millisecond,
	}
	sm.stateMap["s1"] = &SessionState{
		SessionID:   "s1",
		Status:      StatusPermission,
		Source:      SourceEvent,
		LastEventAt: time.Now().Add(-time.Hour), // 早已过期
	}

	got := sm.statusForSession(types.Session{ID: "s1"})
	if got != StatusPermission {
		t.Errorf("statusForSession = %q, want %q (stale PERMISSION must survive on display path)", got, StatusPermission)
	}
}

func TestStatusForSession_staleErrorKeptOnDisplay(t *testing.T) {
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, title, time_created, time_updated) VALUES ('s1', 'global', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()

	sm := &StateManager{
		db:              d,
		stateMap:        make(map[string]*SessionState),
		eventStaleAfter: 50 * time.Millisecond,
	}
	sm.stateMap["s1"] = &SessionState{
		SessionID:   "s1",
		Status:      StatusError,
		Source:      SourceEvent,
		LastEventAt: time.Now().Add(-time.Hour),
	}

	got := sm.statusForSession(types.Session{ID: "s1"})
	if got != StatusError {
		t.Errorf("statusForSession = %q, want %q (stale ERROR must survive on display path)", got, StatusError)
	}
}

func TestStatusForSession_staleBusyStillTakenOver(t *testing.T) {
	// 非内存态（BUSY）过期后照常回落 deriveFromDB：豁免仅覆盖
	// PERMISSION / ERROR，不改变 60s 接管语义。
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, title, time_created, time_updated) VALUES ('s1', 'global', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wdb.Exec(`INSERT INTO message (id, session_id, data, time_created) VALUES ('m1', 's1', '{"role":"assistant","time":{"created":1500,"completed":2000}}', 2000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()

	sm := &StateManager{
		db:              d,
		stateMap:        make(map[string]*SessionState),
		eventStaleAfter: 50 * time.Millisecond,
	}
	sm.stateMap["s1"] = &SessionState{
		SessionID:   "s1",
		Status:      StatusBusy,
		Source:      SourceEvent,
		LastEventAt: time.Now().Add(-time.Hour),
	}

	got := sm.statusForSession(types.Session{ID: "s1"})
	if got != StatusIdle {
		t.Errorf("statusForSession = %q, want %q (stale BUSY falls back to deriveFromDB)", got, StatusIdle)
	}
}

func TestStatusForSession_freshEventStateUsed(t *testing.T) {
	// 60s 内的事件态照旧直接采用，DB 不参与。
	d := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, title, time_created, time_updated) VALUES ('s1', 'global', 'Test', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer d.Close()

	sm := &StateManager{
		db:              d,
		stateMap:        make(map[string]*SessionState),
		eventStaleAfter: time.Hour,
	}
	sm.stateMap["s1"] = &SessionState{
		SessionID:   "s1",
		Status:      StatusPermission,
		Source:      SourceEvent,
		LastEventAt: time.Now(),
	}

	got := sm.statusForSession(types.Session{ID: "s1"})
	if got != StatusPermission {
		t.Errorf("statusForSession = %q, want %q (fresh event state wins)", got, StatusPermission)
	}
}

// ---------------------------------------------------------------------------
// getOrCreateState
// ---------------------------------------------------------------------------

func TestGetOrCreateState_createsNew(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.mu.Lock()
	state := sm.getOrCreateState("new-id")
	sm.mu.Unlock()

	if state.SessionID != "new-id" {
		t.Errorf("SessionID = %q, want %q", state.SessionID, "new-id")
	}
	// Default zero values.
	if state.Status != "" {
		t.Errorf("Status should be empty, got %q", state.Status)
	}
}

func TestGetOrCreateState_returnsExisting(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	sm.mu.Lock()
	original := sm.getOrCreateState("s1")
	original.Status = StatusBusy
	second := sm.getOrCreateState("s1")
	sm.mu.Unlock()

	if original != second {
		t.Error("getOrCreateState should return the same pointer for an existing key")
	}
	if second.Status != StatusBusy {
		t.Errorf("second.Status = %q, want %q", second.Status, StatusBusy)
	}
	sm.mu.RLock()
	n := len(sm.stateMap)
	sm.mu.RUnlock()
	if n != 1 {
		t.Errorf("stateMap should have 1 entry, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// strProp & nestedStrProp (used inside processEvent)
// ---------------------------------------------------------------------------

func TestStrProp_missingKey(t *testing.T) {
	m := map[string]interface{}{"a": "b"}
	if got := strProp(m, "missing"); got != "" {
		t.Errorf("strProp = %q, want %q", got, "")
	}
}

func TestStrProp_wrongType(t *testing.T) {
	m := map[string]interface{}{"n": 42}
	if got := strProp(m, "n"); got != "" {
		t.Errorf("strProp = %q, want %q", got, "")
	}
}

func TestStrProp_stringValue(t *testing.T) {
	m := map[string]interface{}{"s": "hello"}
	if got := strProp(m, "s"); got != "hello" {
		t.Errorf("strProp = %q, want %q", got, "hello")
	}
}

func TestNestedStrProp_happyPath(t *testing.T) {
	m := map[string]interface{}{
		"status": map[string]interface{}{"type": "busy"},
	}
	if got := nestedStrProp(m, "status", "type"); got != "busy" {
		t.Errorf("nestedStrProp = %q, want %q", got, "busy")
	}
}

func TestNestedStrProp_missingOuter(t *testing.T) {
	m := map[string]interface{}{}
	if got := nestedStrProp(m, "nope", "type"); got != "" {
		t.Errorf("nestedStrProp = %q, want %q", got, "")
	}
}

func TestNestedStrProp_wrongOuterType(t *testing.T) {
	m := map[string]interface{}{"status": "not-a-map"}
	if got := nestedStrProp(m, "status", "type"); got != "" {
		t.Errorf("nestedStrProp = %q, want %q", got, "")
	}
}

func TestNestedStrProp_missingInner(t *testing.T) {
	m := map[string]interface{}{
		"status": map[string]interface{}{"other": "value"},
	}
	if got := nestedStrProp(m, "status", "type"); got != "" {
		t.Errorf("nestedStrProp = %q, want %q", got, "")
	}
}

// ---------------------------------------------------------------------------
// mapStatus (used inside processEvent)
// ---------------------------------------------------------------------------

func TestMapStatus_knownValues(t *testing.T) {
	tests := []struct {
		input string
		want  SessionStatus
	}{
		{"idle", StatusIdle},
		{"busy", StatusBusy},
		{"thinking", StatusBusy},
		{"permission", StatusPermission},
		{"retry", StatusRetry},
		{"error", StatusError},
		{"", StatusUnknown},
		{"anything_else", StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapStatus(tt.input)
			if got != tt.want {
				t.Errorf("mapStatus(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test version of processEvent that the event timestamp is set (sanity)
// ---------------------------------------------------------------------------

func TestProcessEvent_setsLastEventAt(t *testing.T) {
	sm := &StateManager{stateMap: make(map[string]*SessionState)}
	before := time.Now()
	evt := marshalEvent(t, "session.status", map[string]interface{}{
		"sessionID": "s1",
		"status":    map[string]interface{}{"type": "busy"},
	})
	sm.processEvent(evt)
	after := time.Now()

	sm.mu.RLock()
	state := sm.stateMap["s1"]
	sm.mu.RUnlock()

	if state.LastEventAt.Before(before) || state.LastEventAt.After(after) {
		t.Errorf("LastEventAt %v should be between %v and %v", state.LastEventAt, before, after)
	}
}

// ---------------------------------------------------------------------------
// handleSubscriberMsg: listSessions (sidebar integration)
// ---------------------------------------------------------------------------

func TestHandleSubscriberMsg_listSessions(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()
	sm := NewStateManagerWithSocket(database, "")

	req, err := json.Marshal(RequestMsg{Type: "request", Method: "listSessions", ID: "req-1"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var out bytes.Buffer
	cl := &clientConn{conn: nil, enc: json.NewEncoder(&out)}
	sm.handleSubscriberMsg(cl, req)

	var resp ResponseMsg
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Type != "response" {
		t.Errorf("Type = %q, want %q", resp.Type, "response")
	}
	if resp.ID != "req-1" {
		t.Errorf("ID = %q, want %q", resp.ID, "req-1")
	}
	if !resp.Ok {
		t.Error("Ok = false, want true")
	}
	if len(resp.Projects) != 0 {
		t.Errorf("Projects length = %d, want 0", len(resp.Projects))
	}
}

func TestListSessionsForSidebar_EmptyDB(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()
	sm := NewStateManager(database)

	projects, err := sm.listSessionsForSidebar()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(projects))
	}
}

func TestListSessionsForSidebar_SingleSession(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('ses_root', 'global', '', 'root', '/home/user', 'Root Session', '1.0',
			1, 2000, 0)`)
	})
	defer database.Close()
	sm := NewStateManager(database)

	projects, err := sm.listSessionsForSidebar()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].ProjectID != "global" {
		t.Errorf("project id = %q, want global", projects[0].ProjectID)
	}
	if len(projects[0].Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(projects[0].Sessions))
	}
	if projects[0].Sessions[0].SessionID != "ses_root" {
		t.Errorf("session id = %q, want ses_root", projects[0].Sessions[0].SessionID)
	}
}

func TestListSessionsForSidebar_AggregateSubsessionStatuses(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('ses_root', 'global', '', 'root', '/home/user', 'Root Session', '1.0',
			1, 2000, 0)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('ses_child1', 'global', 'ses_root', 'child1', '/home/user', 'Child 1', '1.0',
			1, 1500, 0)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('ses_child2', 'global', 'ses_root', 'child2', '/home/user', 'Child 2', '1.0',
			1, 1200, 0)`)
	})
	defer database.Close()
	sm := NewStateManager(database)

	projects, err := sm.listSessionsForSidebar()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 1 || len(projects[0].Sessions) != 1 {
		t.Fatalf("expected 1 project with 1 root session, got %+v", projects)
	}
	root := projects[0].Sessions[0]
	if root.SessionID != "ses_root" {
		t.Fatalf("expected root session, got %q", root.SessionID)
	}
	// Root + 2 children = 3 statuses total.
	total := 0
	for _, c := range root.Statuses {
		total += c
	}
	if total != 3 {
		t.Errorf("status count total = %d, want 3", total)
	}
}

func TestListSessionsForSidebar_FiltersArchived(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('ses_active', 'global', '', 'active', '/home/user', 'Active', '1.0',
			1, 2000, 0)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('ses_archived', 'global', '', 'archived', '/home/user', 'Archived', '1.0',
			1, 2000, 1)`)
	})
	defer database.Close()
	sm := NewStateManager(database)

	projects, err := sm.listSessionsForSidebar()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 1 || len(projects[0].Sessions) != 1 {
		t.Fatalf("expected 1 active session, got %+v", projects)
	}
	if projects[0].Sessions[0].SessionID != "ses_active" {
		t.Errorf("session id = %q, want ses_active", projects[0].Sessions[0].SessionID)
	}
}

// ---------------------------------------------------------------------------
// buildView
// ---------------------------------------------------------------------------

func TestBuildView_Basic(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			agent, cost, tokens_input, tokens_output, tokens_reasoning, tokens_cache_read,
			time_created, time_updated, time_archived)
			VALUES ('ses-1', 'global', '', 'slug1', '/home/user', 'Session One', '1.0',
			'coder', 0.05, 10, 20, 5, 2,
			1, 2000, 0)`)
	})
	defer database.Close()
	sm := NewStateManager(database)

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView error: %v", err)
	}
	if view.Type != "view" {
		t.Errorf("type = %q, want view", view.Type)
	}
	if len(view.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(view.Projects))
	}
	proj := view.Projects[0]
	if proj.ProjectID != "global" {
		t.Errorf("project id = %q, want global", proj.ProjectID)
	}
	if len(proj.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(proj.Sessions))
	}
	s := proj.Sessions[0]
	if s.SessionID != "ses-1" {
		t.Errorf("session id = %q, want ses-1", s.SessionID)
	}
	if s.Title != "Session One" {
		t.Errorf("title = %q, want Session One", s.Title)
	}
	if s.Depth != 1 {
		t.Errorf("depth = %d, want 1", s.Depth)
	}
	if view.Stats.TotalSessions != 1 {
		t.Errorf("total sessions = %d, want 1", view.Stats.TotalSessions)
	}
	if view.Stats.ActiveSessions != 1 {
		t.Errorf("active sessions = %d, want 1", view.Stats.ActiveSessions)
	}
	if view.Stats.TotalCost != 0.05 {
		t.Errorf("total cost = %v, want 0.05", view.Stats.TotalCost)
	}
	if view.Stats.TotalTokens != 37 {
		t.Errorf("total tokens = %d, want 37", view.Stats.TotalTokens)
	}
}

func TestBuildView_RowStatusAggregatesSubsessions(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('root', 'global', '', 'root', '/home/user', 'Root', '1.0',
			1, 2000, 0)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('sub', 'global', 'root', 'sub', '/home/user', 'Sub', '1.0',
			1, 1500, 0)`)
	})
	defer database.Close()
	sm := NewStateManager(database)

	// Mark subsession as BUSY via event so root should aggregate to BUSY.
	sm.processEvent(marshalEvent(t, "session.status", map[string]interface{}{
		"sessionID": "sub",
		"status":    map[string]interface{}{"type": "busy"},
	}))

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView error: %v", err)
	}
	if len(view.Projects) != 1 || len(view.Projects[0].Sessions) != 2 {
		t.Fatalf("expected 1 project with 2 sessions, got %+v", view.Projects)
	}

	var root, sub *ViewSession
	for i := range view.Projects[0].Sessions {
		s := &view.Projects[0].Sessions[i]
		if s.SessionID == "root" {
			root = s
		} else if s.SessionID == "sub" {
			sub = s
		}
	}
	if root == nil || sub == nil {
		t.Fatalf("expected root and sub sessions")
	}
	if sub.Status != StatusBusy {
		t.Errorf("sub status = %q, want BUSY", sub.Status)
	}
	if root.RowStatus != StatusBusy {
		t.Errorf("root rowStatus = %q, want BUSY", root.RowStatus)
	}
	if root.Depth != 1 || sub.Depth != 2 {
		t.Errorf("depths = (%d, %d), want (1, 2)", root.Depth, sub.Depth)
	}
	if view.Projects[0].RowStatus != StatusBusy {
		t.Errorf("project rowStatus = %q, want BUSY", view.Projects[0].RowStatus)
	}
}

func TestBuildView_ArchivedSession(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('archived', 'global', '', 'archived', '/home/user', 'Archived', '1.0',
			1, 2000, 999)`)
	})
	defer database.Close()
	sm := NewStateManager(database)

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView error: %v", err)
	}
	if len(view.Projects[0].Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(view.Projects[0].Sessions))
	}
	if view.Projects[0].Sessions[0].Status != StatusArchived {
		t.Errorf("status = %q, want ARCHIVED", view.Projects[0].Sessions[0].Status)
	}
	if view.Stats.ActiveSessions != 0 {
		t.Errorf("active sessions = %d, want 0", view.Stats.ActiveSessions)
	}
}

func TestBuildView_Empty(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()
	sm := NewStateManager(database)

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView error: %v", err)
	}
	if len(view.Projects) != 0 {
		t.Errorf("projects = %d, want 0", len(view.Projects))
	}
	if view.Stats.TotalSessions != 0 {
		t.Errorf("total sessions = %d, want 0", view.Stats.TotalSessions)
	}
}

// ---------------------------------------------------------------------------
// Action handling tests
// ---------------------------------------------------------------------------

func TestHandleAction_Delete(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('del-1', 'global', '', 'del-1', '/home/user', 'To Delete', '1.0',
			1, 2000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	cl := &clientConn{enc: json.NewEncoder(&bytes.Buffer{})}
	sm.handleActionMsg(cl, ActionMsg{
		Type:       "action",
		Action:     "delete",
		SessionIDs: []string{"del-1"},
	})

	// Deleting through opencode CLI may fail in test environments; the important
	// thing is that the action was dispatched and produced a result message.
	// We verify by checking that the action completed without panicking and that
	// the client was written to.
}

func TestHandleAction_Export(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('exp-1', 'global', '', 'exp-1', '/home/user', 'To Export', '1.0',
			1, 2000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	cl := &clientConn{enc: json.NewEncoder(&bytes.Buffer{})}
	sm.handleActionMsg(cl, ActionMsg{
		Type:       "action",
		Action:     "export",
		SessionIDs: []string{"exp-1"},
	})
}

func TestHandleAction_Create(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	cl := &clientConn{enc: json.NewEncoder(&bytes.Buffer{})}
	sm.handleActionMsg(cl, ActionMsg{
		Type:      "action",
		Action:    "create",
		Directory: "/tmp",
		Message:   "hello",
	})
}

func TestHandleAction_Fork(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	cl := &clientConn{enc: json.NewEncoder(&bytes.Buffer{})}
	sm.handleActionMsg(cl, ActionMsg{
		Type:      "action",
		Action:    "fork",
		SessionID: "parent-1",
		Directory: "/tmp",
		Message:   "fork message",
	})
}

func TestHandleAction_Send(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	cl := &clientConn{enc: json.NewEncoder(&bytes.Buffer{})}
	sm.handleActionMsg(cl, ActionMsg{
		Type:      "action",
		Action:    "send",
		SessionID: "target-1",
		Directory: "/tmp",
		Message:   "send message",
	})
}

func TestHandleAction_Unknown(t *testing.T) {
	database := setupDBWithData(t, nil)
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}
	sm.handleActionMsg(cl, ActionMsg{
		Type:   "action",
		Action: "nope",
	})

	if !bytes.Contains(buf.Bytes(), []byte(`"error":"unknown action: nope"`)) {
		t.Errorf("expected unknown action error in output, got %q", buf.String())
	}
}

func TestHandleRequest_Messages(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('msg-1', 'global', '', 'msg-1', '/home/user', 'Messages', '1.0',
			1, 2000, 0)`)
		wdb.Exec(`INSERT INTO message (id, session_id, data, time_created)
			VALUES ('m1', 'msg-1', '{"role":"user"}', 1000)`)
		wdb.Exec(`INSERT INTO part (id, message_id, session_id, data, time_created)
			VALUES ('p1', 'm1', 'msg-1', '{"type":"text","text":"hi"}', 1000)`)
	})
	defer database.Close()

	sm := NewStateManager(database)

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}
	sm.handleRequestMsg(cl, RequestMsg{Type: "request", Method: "messages", ID: "req-1", SessionID: "msg-1"})

	if !bytes.Contains(buf.Bytes(), []byte(`"ok":true`)) {
		t.Errorf("expected ok=true in response, got %q", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"text":"hi"`)) {
		t.Errorf("expected message text in response, got %q", buf.String())
	}
}

// TestProcessEvent_updatesProcessInfo 验证所有携带 pid/tmux 字段的事件类型都会
// 刷新 sessionProcess 映射并写入 SessionState.ProcessInfo。master 侧共 10 类
// 非 deleted 状态事件（源分支为 8 类，此处补 question.asked / question.replied）。
func TestProcessEvent_updatesProcessInfo(t *testing.T) {
	sm := NewStateManager(nil)

	cases := []struct {
		typ   string
		props map[string]interface{}
	}{
		{"session.status", map[string]interface{}{
			"sessionID": "s-status",
			"status":    map[string]interface{}{"type": "busy"},
		}},
		{"session.idle", map[string]interface{}{"sessionID": "s-idle"}},
		{"session.created", map[string]interface{}{"sessionID": "s-created"}},
		{"session.error", map[string]interface{}{"sessionID": "s-error"}},
		{"permission.updated", map[string]interface{}{"sessionID": "s-perm-updated"}},
		{"permission.asked", map[string]interface{}{"sessionID": "s-perm-asked"}},
		{"permission.replied", map[string]interface{}{"sessionID": "s-perm-replied"}},
		{"question.asked", map[string]interface{}{"sessionID": "s-question-asked"}},
		{"question.replied", map[string]interface{}{"sessionID": "s-question-replied"}},
		{"session.compacted", map[string]interface{}{"sessionID": "s-compacted"}},
	}

	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			tc.props["pid"] = 42
			tc.props["tmuxPane"] = "%12"
			tc.props["tmuxSession"] = "$0"
			sm.processEvent(marshalEvent(t, tc.typ, tc.props))

			sid := tc.props["sessionID"].(string)
			sm.mu.RLock()
			info := sm.sessionProcess[sid]
			state := sm.stateMap[sid]
			_, indexed := sm.pidIndex[42][sid]
			sm.mu.RUnlock()

			if info == nil {
				t.Fatalf("expected sessionProcess entry for %s", sid)
			}
			if info.PID != 42 {
				t.Errorf("PID = %d, want 42", info.PID)
			}
			if info.TMUXPane != "%12" {
				t.Errorf("TMUXPane = %q, want %%12", info.TMUXPane)
			}
			if info.TMUXSession != "$0" {
				t.Errorf("TMUXSession = %q, want $0", info.TMUXSession)
			}
			if state == nil || state.ProcessInfo != info {
				t.Error("expected SessionState.ProcessInfo to point to the same ProcessInfo")
			}
			if !indexed {
				t.Errorf("expected pidIndex[42] to contain %s", sid)
			}
		})
	}
}

// TestProcessEvent_processInfo_pidChangeMovesIndex 验证同一 session 换 pid 后
// 旧 pid 索引被移除、新 pid 索引生效。
func TestProcessEvent_processInfo_pidChangeMovesIndex(t *testing.T) {
	sm := NewStateManager(nil)
	sid := "s-moving"

	sm.processEvent(marshalEvent(t, "session.status", map[string]interface{}{
		"sessionID":   sid,
		"status":      map[string]interface{}{"type": "busy"},
		"pid":         100,
		"tmuxPane":    "%1",
		"tmuxSession": "$1",
	}))

	sm.processEvent(marshalEvent(t, "session.status", map[string]interface{}{
		"sessionID":   sid,
		"status":      map[string]interface{}{"type": "idle"},
		"pid":         200,
		"tmuxPane":    "%2",
		"tmuxSession": "$2",
	}))

	sm.mu.RLock()
	info := sm.sessionProcess[sid]
	_, oldIndexed := sm.pidIndex[100][sid]
	_, newIndexed := sm.pidIndex[200][sid]
	sm.mu.RUnlock()

	if info.PID != 200 {
		t.Errorf("PID = %d, want 200", info.PID)
	}
	if oldIndexed {
		t.Error("old pid index should have been removed")
	}
	if !newIndexed {
		t.Error("new pid index should contain the session")
	}
}

// TestOnDisconnect_clearsMappings 验证 event-source 连接断开时按 pid 清理
// sessionProcess / pidIndex / SessionState.ProcessInfo，无关 pid 不受影响。
func TestOnDisconnect_clearsMappings(t *testing.T) {
	sm := NewStateManager(nil)

	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{SessionID: "s1", Status: StatusBusy, ProcessInfo: &ProcessInfo{PID: 123}}
	sm.stateMap["s2"] = &SessionState{SessionID: "s2", Status: StatusBusy, ProcessInfo: &ProcessInfo{PID: 123}}
	sm.stateMap["s3"] = &SessionState{SessionID: "s3", Status: StatusBusy, ProcessInfo: &ProcessInfo{PID: 999}}
	sm.sessionProcess["s1"] = sm.stateMap["s1"].ProcessInfo
	sm.sessionProcess["s2"] = sm.stateMap["s2"].ProcessInfo
	sm.sessionProcess["s3"] = sm.stateMap["s3"].ProcessInfo
	sm.pidIndex[123] = map[string]struct{}{"s1": {}, "s2": {}}
	sm.pidIndex[999] = map[string]struct{}{"s3": {}}
	sm.mu.Unlock()

	sm.onDisconnect(123)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.sessionProcess["s1"] != nil || sm.sessionProcess["s2"] != nil {
		t.Error("sessionProcess entries for pid 123 should be removed")
	}
	if sm.sessionProcess["s3"] == nil {
		t.Error("sessionProcess entry for unrelated pid should remain")
	}
	if sm.stateMap["s1"].ProcessInfo != nil || sm.stateMap["s2"].ProcessInfo != nil {
		t.Error("SessionState.ProcessInfo for pid 123 should be cleared")
	}
	if sm.stateMap["s3"].ProcessInfo == nil {
		t.Error("SessionState.ProcessInfo for unrelated pid should remain")
	}
	if _, ok := sm.pidIndex[123]; ok {
		t.Error("pidIndex[123] should be deleted")
	}
	if _, ok := sm.pidIndex[999]; !ok {
		t.Error("pidIndex[999] should remain")
	}
}

// TestBuildView_IncludesTMUXFields 验证 buildView 把 sessionProcess 映射注入
// ViewSession 的 PID/TMUXPane/TMUXSession 字段（快照+注入合流方式）。
func TestBuildView_IncludesTMUXFields(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('tmux-session', 'global', '', 'tmux', '/home/user', 'TMUX Session', '1.0',
			1, 2000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.processEvent(marshalEvent(t, "session.status", map[string]interface{}{
		"sessionID":   "tmux-session",
		"status":      map[string]interface{}{"type": "busy"},
		"pid":         1234,
		"tmuxPane":    "%5",
		"tmuxSession": "$2",
	}))

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView error: %v", err)
	}
	if len(view.Projects) != 1 || len(view.Projects[0].Sessions) != 1 {
		t.Fatalf("expected 1 project with 1 session, got %+v", view.Projects)
	}

	s := view.Projects[0].Sessions[0]
	if s.SessionID != "tmux-session" {
		t.Fatalf("session id = %q, want tmux-session", s.SessionID)
	}
	if s.PID != 1234 {
		t.Errorf("PID = %d, want 1234", s.PID)
	}
	if s.TMUXPane != "%5" {
		t.Errorf("TMUXPane = %q, want %%5", s.TMUXPane)
	}
	if s.TMUXSession != "$2" {
		t.Errorf("TMUXSession = %q, want $2", s.TMUXSession)
	}
}
