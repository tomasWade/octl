// Package db provides tests for the database layer.
package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/tomasWade/octl/internal/types"
)

// setupTestDB creates a temporary database with test schema and data.
// It returns the DB instance and the temp dir (caller should clean up).
func setupTestDB(t *testing.T) (*DB, string) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First, create the database with write access to set up schema
	writeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB for writing: %v", err)
	}

	// Create schema (matching the real database)
	schema := `
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
		FOREIGN KEY (session_id) REFERENCES session(id)
	);

	CREATE TABLE IF NOT EXISTS part (
		id TEXT PRIMARY KEY,
		message_id TEXT,
		session_id TEXT,
		data TEXT,
		time_created INTEGER,
		FOREIGN KEY (message_id) REFERENCES message(id),
		FOREIGN KEY (session_id) REFERENCES session(id)
	);
	`
	if _, err := writeDB.Exec(schema); err != nil {
		writeDB.Close()
		t.Fatalf("Failed to create schema: %v", err)
	}

	// Insert test data - model is stored as JSON
	testData := `
	INSERT INTO session (id, project_id, slug, directory, title, version, agent, model, cost, tokens_input, tokens_output, time_created, time_updated, time_archived)
	VALUES
		('session-1', 'proj-1', 'test-session-1', '/test/dir1', 'Test Session 1', 'v1', 'coder', '{"id": "gpt-4", "providerID": "openai"}', 1.50, 1000, 500, 1700000000000, 1700000100000, NULL),
		('session-2', 'proj-1', 'test-session-2', '/test/dir2', 'Test Session 2', 'v1', 'reviewer', '{"id": "gpt-4", "providerID": "openai"}', 2.00, 1500, 750, 1700000200000, 1700000300000, NULL),
		('session-3', 'proj-2', 'test-session-3', '/test/dir3', 'Test Session 3', 'v1', 'coder', '{"id": "claude-3", "providerID": "anthropic"}', 0.50, 500, 250, 1700000300000, 1700000400000, NULL);

	INSERT INTO message (id, session_id, data, time_created)
	VALUES
		('msg-1', 'session-1', '{"role": "user"}', 1700000001000),
		('msg-2', 'session-1', '{"role": "assistant"}', 1700000002000),
		('msg-3', 'session-2', '{"role": "user"}', 1700000201000);

	INSERT INTO part (id, message_id, session_id, data, time_created)
	VALUES
		('part-1', 'msg-1', 'session-1', '{"type": "text", "text": "Hello, write some code"}', 1700000001000),
		('part-2', 'msg-2', 'session-1', '{"type": "text", "text": "Here is the code you requested"}', 1700000002000),
		('part-3', 'msg-3', 'session-2', '{"type": "text", "text": "Review this code please"}', 1700000201000);
	`
	if _, err := writeDB.Exec(testData); err != nil {
		writeDB.Close()
		t.Fatalf("Failed to insert test data: %v", err)
	}

	writeDB.Close()

	// Now open with read-only mode (as the real code does)
	d, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return d, dir
}

func TestNewAndClose(t *testing.T) {
	// Create a temp DB file
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First create the DB with write access
	writeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB for writing: %v", err)
	}
	writeDB.Close()

	d, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if d == nil {
		t.Fatal("New() returned nil DB")
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNew_ReadOnly(t *testing.T) {
	// Test with non-existent file - modernc.org/sqlite may fail on non-existent files
	// in read-only mode, or may return empty results. Both behaviors are acceptable.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nonexistent.db")

	d, err := New(dbPath)
	if err != nil {
		// Expected behavior - some SQLite implementations fail on non-existent files in ro mode
		return
	}
	defer d.Close()

	// If no error, verify it returns empty results (file exists but is empty)
	sessions, err := d.ListSessions()
	if err != nil {
		// Also acceptable - query fails on empty/invalid DB
		return
	}
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions on non-existent DB, got %d", len(sessions))
	}
}

func TestListSessions(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	sessions, err := d.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}

	if len(sessions) != 3 {
		t.Errorf("Expected 3 sessions, got %d", len(sessions))
	}

	// Check first session (most recent by time_created DESC: session-3 at 1700000300000)
	if sessions[0].ID != "session-3" {
		t.Errorf("Expected first session ID to be session-3, got %s", sessions[0].ID)
	}

	// Verify message count is computed
	// session-1 has 2 messages, session-2 has 1 message, session-3 has 0 messages
	if sessions[0].MessageCount != 0 {
		t.Errorf("Expected session-3 to have 0 messages, got %d", sessions[0].MessageCount)
	}
	if sessions[1].MessageCount != 1 {
		t.Errorf("Expected session-2 to have 1 message, got %d", sessions[1].MessageCount)
	}
	if sessions[2].MessageCount != 2 {
		t.Errorf("Expected session-1 to have 2 messages, got %d", sessions[2].MessageCount)
	}
}

func TestGetSession(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	session, err := d.GetSession("session-1")
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}

	if session.ID != "session-1" {
		t.Errorf("Expected ID 'session-1', got '%s'", session.ID)
	}
	if session.Title != "Test Session 1" {
		t.Errorf("Expected Title 'Test Session 1', got '%s'", session.Title)
	}
	if session.Cost != 1.50 {
		t.Errorf("Expected Cost 1.50, got %f", session.Cost)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	_, err := d.GetSession("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent session")
	}
	if err != ErrSessionNotFound {
		t.Errorf("Expected ErrSessionNotFound, got %v", err)
	}
}

func TestGetSessionStats(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	stats, err := d.GetSessionStats()
	if err != nil {
		t.Fatalf("GetSessionStats() error = %v", err)
	}

	if stats.TotalSessions != 3 {
		t.Errorf("Expected TotalSessions 3, got %d", stats.TotalSessions)
	}
	if stats.ActiveSessions != 3 {
		t.Errorf("Expected ActiveSessions 3, got %d", stats.ActiveSessions)
	}
	if stats.TotalCost != 4.00 {
		t.Errorf("Expected TotalCost 4.00, got %f", stats.TotalCost)
	}
	if stats.TotalTokensInput != 3000 {
		t.Errorf("Expected TotalTokensInput 3000, got %d", stats.TotalTokensInput)
	}
}

func TestGetModelUsage(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	usages, err := d.GetModelUsage()
	if err != nil {
		t.Fatalf("GetModelUsage() error = %v", err)
	}

	// Should have 2 unique models (gpt-4 and claude-3)
	if len(usages) != 2 {
		t.Errorf("Expected 2 model usages, got %d", len(usages))
	}

	// Check gpt-4 usage
	var gpt4Usage *types.ModelUsage
	for i := range usages {
		if usages[i].ModelID == "gpt-4" {
			gpt4Usage = &usages[i]
			break
		}
	}
	if gpt4Usage == nil {
		t.Error("Expected to find gpt-4 usage")
	} else {
		// gpt-4 has session-1 (1500 tokens) + session-2 (2250 tokens) = 3750
		if gpt4Usage.TokenCount != 3750 {
			t.Errorf("Expected gpt-4 TokenCount 3750, got %d", gpt4Usage.TokenCount)
		}
	}
}

func TestClose_EmptyDB(t *testing.T) {
	// Test closing a nil DB doesn't panic
	d := &DB{}
	if err := d.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestListSessions_Empty(t *testing.T) {
	// Create empty database with full schema
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "empty.db")

	writeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	// Create full schema
	schema := `
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
		time_created INTEGER
	);
	CREATE TABLE IF NOT EXISTS part (
		id TEXT PRIMARY KEY,
		message_id TEXT,
		session_id TEXT,
		data TEXT,
		time_created INTEGER
	);
	`
	_, err = writeDB.Exec(schema)
	if err != nil {
		writeDB.Close()
		t.Fatalf("Failed to create schema: %v", err)
	}
	writeDB.Close()

	d, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer d.Close()

	sessions, err := d.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}

	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions, got %d", len(sessions))
	}
}

func TestGetSessionStats_Empty(t *testing.T) {
	// Create empty database with full schema
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "empty.db")

	writeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	schema := `
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
	`
	_, err = writeDB.Exec(schema)
	if err != nil {
		writeDB.Close()
		t.Fatalf("Failed to create schema: %v", err)
	}
	writeDB.Close()

	d, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer d.Close()

	stats, err := d.GetSessionStats()
	if err != nil {
		t.Fatalf("GetSessionStats() error = %v", err)
	}

	if stats.TotalSessions != 0 {
		t.Errorf("Expected TotalSessions 0, got %d", stats.TotalSessions)
	}
	if stats.TotalCost != 0 {
		t.Errorf("Expected TotalCost 0, got %f", stats.TotalCost)
	}
}
func TestGetSessionMessages(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	parts, err := d.GetSessionMessages("session-1")
	if err != nil {
		t.Fatalf("GetSessionMessages() error = %v", err)
	}

	if len(parts) != 2 {
		t.Fatalf("Expected 2 message parts, got %d", len(parts))
	}

	// Verify ordering by time_created ASC (oldest first)
	if parts[0].PartID != "part-1" {
		t.Errorf("Expected first part to be part-1 (time_created 1700000001000), got %s", parts[0].PartID)
	}
	if parts[1].PartID != "part-2" {
		t.Errorf("Expected second part to be part-2 (time_created 1700000002000), got %s", parts[1].PartID)
	}

	// Verify role from message.data JSON extraction
	if parts[0].Role != "user" {
		t.Errorf("Expected part-1 role 'user', got %q", parts[0].Role)
	}
	if parts[1].Role != "assistant" {
		t.Errorf("Expected part-2 role 'assistant', got %q", parts[1].Role)
	}

	// Verify text from part.data JSON extraction
	if parts[0].Text != "Hello, write some code" {
		t.Errorf("Expected part-1 text %q, got %q", "Hello, write some code", parts[0].Text)
	}
	if parts[1].Text != "Here is the code you requested" {
		t.Errorf("Expected part-2 text %q, got %q", "Here is the code you requested", parts[1].Text)
	}

	// Verify session ID is populated on every part
	for i, p := range parts {
		if p.SessionID != "session-1" {
			t.Errorf("parts[%d] SessionID = %q, want 'session-1'", i, p.SessionID)
		}
	}
}

func TestGetSessionMessages_NotFound(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	parts, err := d.GetSessionMessages("nonexistent-session")
	if err != nil {
		t.Fatalf("GetSessionMessages() error = %v", err)
	}
	if len(parts) != 0 {
		t.Errorf("Expected 0 message parts for nonexistent session, got %d", len(parts))
	}
}

func TestGetSessionMessages_Empty(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// session-3 has no messages or parts
	parts, err := d.GetSessionMessages("session-3")
	if err != nil {
		t.Fatalf("GetSessionMessages() error = %v", err)
	}
	if len(parts) != 0 {
		t.Errorf("Expected 0 message parts for session-3, got %d", len(parts))
	}
}

func TestGetSessionMessages_Ordering(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	parts, err := d.GetSessionMessages("session-1")
	if err != nil {
		t.Fatalf("GetSessionMessages() error = %v", err)
	}

	// Verify strict ascending order by time_created
	if len(parts) >= 2 {
		if parts[0].TimeCreated >= parts[1].TimeCreated {
			t.Errorf("Expected ascending order by time_created: parts[0].TimeCreated (%d) < parts[1].TimeCreated (%d)",
				parts[0].TimeCreated, parts[1].TimeCreated)
		}
	}
}

// mustExec is a test helper that runs an Exec statement and fails the test on error.
func mustExec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("exec: %v\nstmt: %s", err, stmt)
	}
}
