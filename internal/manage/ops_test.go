package manage

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/tomasWade/octl/internal/db"
	"github.com/tomasWade/octl/internal/types"
)

// setupTestDB creates a temporary database with test schema and data for manage tests.
func setupTestDB(t *testing.T) (*db.DB, string) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	writeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB for writing: %v", err)
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
	if _, err := writeDB.Exec(schema); err != nil {
		writeDB.Close()
		t.Fatalf("Failed to create schema: %v", err)
	}

	testData := `
	INSERT INTO session (id, project_id, slug, directory, title, version, agent, model, cost, tokens_input, tokens_output, time_created, time_updated, time_archived)
	VALUES
		('sess-export-1', 'proj-1', 'test-session-1', '/test/dir1', 'Test Session 1', 'v1', 'coder', '{"id": "gpt-4", "providerID": "openai"}', 1.50, 1000, 500, 1700000000000, 1700000100000, NULL),
		('sess-export-2', 'proj-1', 'test-session-2', '/test/dir2', 'Test Session 2', 'v1', 'reviewer', '{"id": "gpt-4", "providerID": "openai"}', 2.00, 1500, 750, 1700000200000, 1700000300000, NULL);

	INSERT INTO message (id, session_id, data, time_created)
	VALUES
		('msg-e1', 'sess-export-1', '{"role": "user"}', 1700000001000),
		('msg-e2', 'sess-export-1', '{"role": "assistant"}', 1700000002000);

	INSERT INTO part (id, message_id, session_id, data, time_created)
	VALUES
		('part-e1', 'msg-e1', 'sess-export-1', '{"text": "Hello, write some code"}', 1700000001000),
		('part-e2', 'msg-e2', 'sess-export-1', '{"text": "Here is the code"}', 1700000002000);
	`
	if _, err := writeDB.Exec(testData); err != nil {
		writeDB.Close()
		t.Fatalf("Failed to insert test data: %v", err)
	}
	writeDB.Close()

	d, err := db.New(dbPath)
	if err != nil {
		t.Fatalf("db.New() error = %v", err)
	}

	return d, dir
}

func TestManager_New(t *testing.T) {
	m := New(nil)
	if m == nil {
		t.Fatal("New(nil) returned nil")
	}

	d, _ := setupTestDB(t)
	defer d.Close()

	m2 := New(d)
	if m2 == nil {
		t.Fatal("New(db) returned nil")
	}
}

func TestDeleteProject_Global_ReturnsError(t *testing.T) {
	// Manager with nil DB is sufficient because the "global" guard runs before
	// any database access.
	m := New(nil)
	err := m.DeleteProject("global")
	if err == nil {
		t.Fatal("DeleteProject('global') expected error, got nil")
	}
	if !strings.Contains(err.Error(), "the global project cannot be deleted") {
		t.Errorf("DeleteProject('global') error = %q, want containing %q",
			err.Error(), "the global project cannot be deleted")
	}
}

func TestExportSession(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	outputDir := t.TempDir()

	session := types.Session{ID: "sess-export-1", Directory: "/test/dir1"}
	filePath, err := m.ExportSession(session, outputDir)
	if err != nil {
		t.Fatalf("ExportSession() error = %v", err)
	}

	expectedPath := filepath.Join(outputDir, "sess-export-1.json")
	if filePath != expectedPath {
		t.Errorf("Expected file path %q, got %q", expectedPath, filePath)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read exported file: %v", err)
	}

	var export exportData
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatalf("Failed to unmarshal export JSON: %v", err)
	}

	if export.Session.ID != "sess-export-1" {
		t.Errorf("Expected session ID 'sess-export-1', got %q", export.Session.ID)
	}
	if export.Session.Title != "Test Session 1" {
		t.Errorf("Expected session title 'Test Session 1', got %q", export.Session.Title)
	}
	if export.Session.Cost != 1.50 {
		t.Errorf("Expected session cost 1.50, got %f", export.Session.Cost)
	}

	if len(export.Messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(export.Messages))
	}
	if export.Messages[0].Role != "user" {
		t.Errorf("Expected first message role 'user', got %q", export.Messages[0].Role)
	}
	if export.Messages[0].Text != "Hello, write some code" {
		t.Errorf("Expected first message text %q, got %q", "Hello, write some code", export.Messages[0].Text)
	}
	if export.Messages[0].TimeCreated != 1700000001000 {
		t.Errorf("Expected first message TimeCreated 1700000001000, got %d", export.Messages[0].TimeCreated)
	}
	if export.Messages[1].Role != "assistant" {
		t.Errorf("Expected second message role 'assistant', got %q", export.Messages[1].Role)
	}
	if export.Messages[1].Text != "Here is the code" {
		t.Errorf("Expected second message text %q, got %q", "Here is the code", export.Messages[1].Text)
	}
}

func TestExportSession_NoSession(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	outputDir := t.TempDir()

	session := types.Session{ID: "nonexistent", Directory: "/test/dir1"}
	_, err := m.ExportSession(session, outputDir)
	if err == nil {
		t.Fatal("Expected error for nonexistent session, got nil")
	}
}

func TestExportSession_CreatesOutputDir(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	outputDir := filepath.Join(t.TempDir(), "subdir", "nested")

	session := types.Session{ID: "sess-export-1", Directory: "/test/dir1"}
	_, err := m.ExportSession(session, outputDir)
	if err != nil {
		t.Fatalf("ExportSession() should create output dir, got error: %v", err)
	}

	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		t.Error("ExportSession did not create the output directory")
	}

	expectedPath := filepath.Join(outputDir, "sess-export-1.json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Error("ExportSession did not create the export file in the created directory")
	}
}

func TestExportSession_JSONStructure(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	outputDir := t.TempDir()

	session := types.Session{ID: "sess-export-1", Directory: "/test/dir1"}
	filePath, err := m.ExportSession(session, outputDir)
	if err != nil {
		t.Fatalf("ExportSession() error = %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read exported file: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if _, ok := raw["session"]; !ok {
		t.Error("Export JSON missing 'session' key")
	}
	if _, ok := raw["messages"]; !ok {
		t.Error("Export JSON missing 'messages' key")
	}

	var export exportData
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatalf("Failed to parse export JSON: %v", err)
	}

	for i, msg := range export.Messages {
		if msg.Role == "" {
			t.Errorf("Messages[%d] missing 'role'", i)
		}
		if msg.Text == "" {
			t.Errorf("Messages[%d] missing 'text'", i)
		}
		if msg.TimeCreated == 0 {
			t.Errorf("Messages[%d] missing 'timeCreated'", i)
		}
	}
}

func TestArchiveSession(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	archiveDir := t.TempDir()

	session := types.Session{ID: "sess-export-1", Directory: "/test/dir1"}
	err := m.ArchiveSession(session, archiveDir)
	if err != nil {
		t.Fatalf("ArchiveSession() error = %v", err)
	}

	sessionDir := filepath.Join(archiveDir, "sess-export-1")
	info, err := os.Stat(sessionDir)
	if os.IsNotExist(err) {
		t.Fatal("ArchiveSession did not create the session directory")
	}
	if !info.IsDir() {
		t.Fatal("Expected session directory to be a directory")
	}

	sessionPath := filepath.Join(sessionDir, "session.json")
	sessionData, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("Failed to read session.json: %v", err)
	}
	var s types.Session
	if err := json.Unmarshal(sessionData, &s); err != nil {
		t.Fatalf("Failed to unmarshal session.json: %v", err)
	}
	if s.ID != "sess-export-1" {
		t.Errorf("Expected session ID 'sess-export-1', got %q", s.ID)
	}
	if s.Title != "Test Session 1" {
		t.Errorf("Expected session title 'Test Session 1', got %q", s.Title)
	}

	messagesPath := filepath.Join(sessionDir, "messages.json")
	messagesData, err := os.ReadFile(messagesPath)
	if err != nil {
		t.Fatalf("Failed to read messages.json: %v", err)
	}
	var parts []types.MessagePart
	if err := json.Unmarshal(messagesData, &parts); err != nil {
		t.Fatalf("Failed to unmarshal messages.json: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("Expected 2 message parts, got %d", len(parts))
	}
	if parts[0].Text != "Hello, write some code" {
		t.Errorf("Expected first part text %q, got %q", "Hello, write some code", parts[0].Text)
	}
	if parts[1].Text != "Here is the code" {
		t.Errorf("Expected second part text %q, got %q", "Here is the code", parts[1].Text)
	}
}

func TestArchiveSession_NoSession(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	archiveDir := t.TempDir()

	session := types.Session{ID: "nonexistent", Directory: "/test/dir1"}
	err := m.ArchiveSession(session, archiveDir)
	if err == nil {
		t.Fatal("Expected error for nonexistent session, got nil")
	}
}

func TestArchiveSession_CreatesArchiveDir(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	archiveDir := filepath.Join(t.TempDir(), "archives", "deep")

	session := types.Session{ID: "sess-export-1", Directory: "/test/dir1"}
	err := m.ArchiveSession(session, archiveDir)
	if err != nil {
		t.Fatalf("ArchiveSession() should create archive dir, got error: %v", err)
	}

	sessionDir := filepath.Join(archiveDir, "sess-export-1")
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		t.Error("ArchiveSession did not create the session directory inside the new archive dir")
	}
}

func TestBatchDelete_Empty(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)

	summary := m.BatchDelete([]types.Session{})
	if summary.Total != 0 {
		t.Errorf("Expected Total 0, got %d", summary.Total)
	}
	if summary.Succeeded != 0 {
		t.Errorf("Expected Succeeded 0, got %d", summary.Succeeded)
	}
	if summary.Failed != 0 {
		t.Errorf("Expected Failed 0, got %d", summary.Failed)
	}
	if len(summary.Results) != 0 {
		t.Errorf("Expected 0 results, got %d", len(summary.Results))
	}
}

func TestBatchDelete_Single(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)

	sessions := []types.Session{
		{ID: "sess-fail-1", Directory: "/tmp"},
	}
	summary := m.BatchDelete(sessions)

	if summary.Total != 1 {
		t.Errorf("Expected Total 1, got %d", summary.Total)
	}
	if summary.Succeeded != 0 {
		t.Errorf("Expected Succeeded 0, got %d", summary.Succeeded)
	}
	if summary.Failed != 1 {
		t.Errorf("Expected Failed 1, got %d", summary.Failed)
	}
	if len(summary.Results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(summary.Results))
	}

	r := summary.Results[0]
	if r.SessionID != "sess-fail-1" {
		t.Errorf("Expected SessionID 'sess-fail-1', got %q", r.SessionID)
	}
	if r.Action != "delete" {
		t.Errorf("Expected Action 'delete', got %q", r.Action)
	}
	if r.Success {
		t.Error("Expected Success to be false (no opencode binary)")
	}
	if r.Error == "" {
		t.Error("Expected non-empty Error message")
	}
}

func TestBatchDelete_Multiple(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)

	sessions := []types.Session{
		{ID: "sess-fail-1", Directory: "/tmp"},
		{ID: "sess-fail-2", Directory: "/tmp"},
	}
	summary := m.BatchDelete(sessions)

	if summary.Total != 2 {
		t.Errorf("Expected Total 2, got %d", summary.Total)
	}
	if summary.Succeeded != 0 {
		t.Errorf("Expected Succeeded 0, got %d", summary.Succeeded)
	}
	if summary.Failed != 2 {
		t.Errorf("Expected Failed 2, got %d", summary.Failed)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(summary.Results))
	}

	for i, r := range summary.Results {
		if r.Action != "delete" {
			t.Errorf("Results[%d] Action = %q, want 'delete'", i, r.Action)
		}
		if r.Success {
			t.Errorf("Results[%d] should have Success = false", i)
		}
		if r.Error == "" {
			t.Errorf("Results[%d] should have non-empty Error", i)
		}
	}

	if summary.Succeeded+summary.Failed != summary.Total {
		t.Errorf("Succeeded + Failed (%d) != Total (%d)", summary.Succeeded+summary.Failed, summary.Total)
	}
}

func TestBatchExport_AllSuccess(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	outputDir := t.TempDir()

	sessions := []types.Session{
		{ID: "sess-export-1", Directory: "/test/dir1"},
		{ID: "sess-export-2", Directory: "/test/dir2"},
	}
	summary := m.BatchExport(sessions, outputDir)

	if summary.Total != 2 {
		t.Errorf("Expected Total 2, got %d", summary.Total)
	}
	if summary.Succeeded != 2 {
		t.Errorf("Expected Succeeded 2, got %d", summary.Succeeded)
	}
	if summary.Failed != 0 {
		t.Errorf("Expected Failed 0, got %d", summary.Failed)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(summary.Results))
	}

	for i, r := range summary.Results {
		if r.Action != "export" {
			t.Errorf("Results[%d] Action = %q, want 'export'", i, r.Action)
		}
		if !r.Success {
			t.Errorf("Results[%d] should have Success = true", i)
		}
		if r.Error != "" {
			t.Errorf("Results[%d] should have no error, got %q", i, r.Error)
		}
	}

	for _, s := range sessions {
		expectedPath := filepath.Join(outputDir, s.ID+".json")
		if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
			t.Errorf("Export file not found: %s", expectedPath)
		}
	}
}

func TestBatchExport_PartialFailure(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	outputDir := t.TempDir()

	sessions := []types.Session{
		{ID: "sess-export-1", Directory: "/test/dir1"},
		{ID: "nonexistent", Directory: "/test/dir1"},
	}
	summary := m.BatchExport(sessions, outputDir)

	if summary.Total != 2 {
		t.Errorf("Expected Total 2, got %d", summary.Total)
	}
	if summary.Succeeded != 1 {
		t.Errorf("Expected Succeeded 1, got %d", summary.Succeeded)
	}
	if summary.Failed != 1 {
		t.Errorf("Expected Failed 1, got %d", summary.Failed)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(summary.Results))
	}

	if !summary.Results[0].Success {
		t.Error("Expected results[0] to succeed (sess-export-1 exists)")
	}
	if summary.Results[0].SessionID != "sess-export-1" {
		t.Errorf("Expected results[0] SessionID 'sess-export-1', got %q", summary.Results[0].SessionID)
	}
	if summary.Results[0].Error != "" {
		t.Errorf("Expected results[0] no error, got %q", summary.Results[0].Error)
	}

	if summary.Results[1].Success {
		t.Error("Expected results[1] to fail (nonexistent session)")
	}
	if summary.Results[1].SessionID != "nonexistent" {
		t.Errorf("Expected results[1] SessionID 'nonexistent', got %q", summary.Results[1].SessionID)
	}
	if summary.Results[1].Error == "" {
		t.Error("Expected non-empty error for failed export")
	}

	successPath := filepath.Join(outputDir, "sess-export-1.json")
	if _, err := os.Stat(successPath); os.IsNotExist(err) {
		t.Error("Export file for sess-export-1 should exist")
	}
	failPath := filepath.Join(outputDir, "nonexistent.json")
	if _, err := os.Stat(failPath); !os.IsNotExist(err) {
		t.Error("Export file for nonexistent session should NOT exist")
	}
}

func TestBatchArchive_AllSuccess(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	archiveDir := t.TempDir()

	sessions := []types.Session{
		{ID: "sess-export-1", Directory: "/test/dir1"},
		{ID: "sess-export-2", Directory: "/test/dir2"},
	}
	summary := m.BatchArchive(sessions, archiveDir)

	if summary.Total != 2 {
		t.Errorf("Expected Total 2, got %d", summary.Total)
	}
	if summary.Succeeded != 2 {
		t.Errorf("Expected Succeeded 2, got %d", summary.Succeeded)
	}
	if summary.Failed != 0 {
		t.Errorf("Expected Failed 0, got %d", summary.Failed)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(summary.Results))
	}

	for i, r := range summary.Results {
		if r.Action != "archive" {
			t.Errorf("Results[%d] Action = %q, want 'archive'", i, r.Action)
		}
		if !r.Success {
			t.Errorf("Results[%d] should have Success = true", i)
		}
		if r.Error != "" {
			t.Errorf("Results[%d] should have no error, got %q", i, r.Error)
		}
	}

	for _, s := range sessions {
		sessionDir := filepath.Join(archiveDir, s.ID)
		if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
			t.Errorf("Archive directory not found: %s", sessionDir)
		}
		if _, err := os.Stat(filepath.Join(sessionDir, "session.json")); os.IsNotExist(err) {
			t.Errorf("session.json not found in %s", sessionDir)
		}
		if _, err := os.Stat(filepath.Join(sessionDir, "messages.json")); os.IsNotExist(err) {
			t.Errorf("messages.json not found in %s", sessionDir)
		}
	}
}

func TestBatchArchive_PartialFailure(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	archiveDir := t.TempDir()

	sessions := []types.Session{
		{ID: "sess-export-1", Directory: "/test/dir1"},
		{ID: "nonexistent", Directory: "/test/dir1"},
	}
	summary := m.BatchArchive(sessions, archiveDir)

	if summary.Total != 2 {
		t.Errorf("Expected Total 2, got %d", summary.Total)
	}
	if summary.Succeeded != 1 {
		t.Errorf("Expected Succeeded 1, got %d", summary.Succeeded)
	}
	if summary.Failed != 1 {
		t.Errorf("Expected Failed 1, got %d", summary.Failed)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(summary.Results))
	}

	if !summary.Results[0].Success {
		t.Error("Expected results[0] to succeed (sess-export-1 exists)")
	}
	if summary.Results[0].SessionID != "sess-export-1" {
		t.Errorf("Expected results[0] SessionID 'sess-export-1', got %q", summary.Results[0].SessionID)
	}

	if summary.Results[1].Success {
		t.Error("Expected results[1] to fail (nonexistent session)")
	}
	if summary.Results[1].SessionID != "nonexistent" {
		t.Errorf("Expected results[1] SessionID 'nonexistent', got %q", summary.Results[1].SessionID)
	}
	if summary.Results[1].Error == "" {
		t.Error("Expected non-empty error for failed archive")
	}

	successDir := filepath.Join(archiveDir, "sess-export-1")
	if _, err := os.Stat(successDir); os.IsNotExist(err) {
		t.Error("Archive directory for sess-export-1 should exist")
	}
	// The failed session dir may exist (MkdirAll runs before GetSession)
	// but session.json and messages.json should NOT be written.
	failDir := filepath.Join(archiveDir, "nonexistent")
	if _, err := os.Stat(filepath.Join(failDir, "session.json")); err == nil {
		t.Error("session.json should NOT exist for failed archive")
	}
	if _, err := os.Stat(filepath.Join(failDir, "messages.json")); err == nil {
		t.Error("messages.json should NOT exist for failed archive")
	}
}

func TestSummary_MixedResults(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	outputDir := t.TempDir()

	sessions := []types.Session{
		{ID: "sess-export-1", Directory: "/test/dir1"},
		{ID: "nonexistent", Directory: "/test/dir1"},
	}
	summary := m.BatchExport(sessions, outputDir)

	if summary.Total != 2 {
		t.Errorf("Expected Total 2, got %d", summary.Total)
	}
	if summary.Succeeded != 1 {
		t.Errorf("Expected Succeeded 1, got %d", summary.Succeeded)
	}
	if summary.Failed != 1 {
		t.Errorf("Expected Failed 1, got %d", summary.Failed)
	}
	if summary.Succeeded+summary.Failed != summary.Total {
		t.Errorf("Succeeded + Failed (%d) != Total (%d)", summary.Succeeded+summary.Failed, summary.Total)
	}
	if len(summary.Results) != summary.Total {
		t.Errorf("len(Results) (%d) != Total (%d)", len(summary.Results), summary.Total)
	}

	for i, r := range summary.Results {
		if r.Action != "export" {
			t.Errorf("Results[%d] Action = %q, want 'export'", i, r.Action)
		}
		if r.SessionID == "" {
			t.Errorf("Results[%d] SessionID should not be empty", i)
		}
		if r.Success {
			if r.Error != "" {
				t.Errorf("Results[%d] should have no error for success, got %q", i, r.Error)
			}
		} else {
			if r.Error == "" {
				t.Errorf("Results[%d] should have non-empty error for failure", i)
			}
		}
	}
}

func TestSummary_AllFailed(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	m := New(d)
	sessions := []types.Session{
		{ID: "fail-1", Directory: "/tmp"},
		{ID: "fail-2", Directory: "/tmp"},
	}
	summary := m.BatchDelete(sessions)

	if summary.Total != 2 {
		t.Errorf("Expected Total 2, got %d", summary.Total)
	}
	if summary.Succeeded != 0 {
		t.Errorf("Expected Succeeded 0, got %d", summary.Succeeded)
	}
	if summary.Failed != 2 {
		t.Errorf("Expected Failed 2, got %d", summary.Failed)
	}
	if summary.Succeeded+summary.Failed != summary.Total {
		t.Errorf("Succeeded + Failed (%d) != Total (%d)", summary.Succeeded+summary.Failed, summary.Total)
	}
	if len(summary.Results) != summary.Total {
		t.Errorf("len(Results) (%d) != Total (%d)", len(summary.Results), summary.Total)
	}

	for i, r := range summary.Results {
		if r.Action != "delete" {
			t.Errorf("Results[%d] Action = %q, want 'delete'", i, r.Action)
		}
		if r.Success {
			t.Errorf("Results[%d] should have Success = false", i)
		}
	}
}
