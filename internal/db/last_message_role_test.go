package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestGetLastMessageRoleCompleted_LastMessage(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// session-1 has 2 messages:
	//   msg-1 (role "user",      time_created 1700000001000)
	//   msg-2 (role "assistant", time_created 1700000002000)
	// The most recent message by time_created should be "assistant"
	role, _, err := d.GetLastMessageRoleCompleted("session-1")
	if err != nil {
		t.Fatalf("GetLastMessageRoleCompleted() error = %v", err)
	}
	if role != "assistant" {
		t.Errorf("Expected role 'assistant', got %q", role)
	}
}

func TestGetLastMessageRoleCompleted_User(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// session-2 has 1 message: msg-3 (role "user", time_created 1700000201000)
	role, _, err := d.GetLastMessageRoleCompleted("session-2")
	if err != nil {
		t.Fatalf("GetLastMessageRoleCompleted() error = %v", err)
	}
	if role != "user" {
		t.Errorf("Expected role 'user', got %q", role)
	}
}

func TestGetLastMessageRoleCompleted_NoMessages(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// session-3 has no messages — should return "" with nil error
	role, completed, err := d.GetLastMessageRoleCompleted("session-3")
	if err != nil {
		t.Fatalf("GetLastMessageRoleCompleted() error = %v", err)
	}
	if role != "" {
		t.Errorf("Expected empty role for session with no messages, got %q", role)
	}
	if completed != 0 {
		t.Errorf("Expected 0 completed for session with no messages, got %d", completed)
	}
}

func TestGetLastMessageRoleCompleted_NonExistentSession(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// A session ID that doesn't exist at all — should return "" with nil error
	role, completed, err := d.GetLastMessageRoleCompleted("does-not-exist")
	if err != nil {
		t.Fatalf("GetLastMessageRoleCompleted() error = %v", err)
	}
	if role != "" {
		t.Errorf("Expected empty role for non-existent session, got %q", role)
	}
	if completed != 0 {
		t.Errorf("Expected 0 completed for non-existent session, got %d", completed)
	}
}

func TestGetLastMessageRoleCompleted_Ordering(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// Verify that with multiple messages, the one with the highest time_created wins.
	// session-1 has msg-1 (user, 1700000001000) and msg-2 (assistant, 1700000002000).
	// msg-2 is newer, so the result should be "assistant" — this confirms ordering matters.
	role, _, err := d.GetLastMessageRoleCompleted("session-1")
	if err != nil {
		t.Fatalf("GetLastMessageRoleCompleted() error = %v", err)
	}
	if role != "assistant" {
		t.Errorf("Expected 'assistant' (most recent by time_created), got %q", role)
	}
}

func TestGetLastMessageRoleCompleted_MissingCompleted(t *testing.T) {
	d, _ := setupTestDB(t)
	defer d.Close()

	// msg-2 has data '{"role": "assistant"}' without time.completed —
	// the message is still being generated, so completedMs must be 0.
	_, completed, err := d.GetLastMessageRoleCompleted("session-1")
	if err != nil {
		t.Fatalf("GetLastMessageRoleCompleted() error = %v", err)
	}
	if completed != 0 {
		t.Errorf("Expected 0 completed for in-progress message, got %d", completed)
	}
}

func TestGetLastMessageRoleCompleted_WithCompleted(t *testing.T) {
	d, dir := setupTestDB(t)
	defer d.Close()

	// Add a session whose last assistant message has time.completed,
	// via a separate writable connection (setupTestDB's own is closed).
	wdb, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open writable DB: %v", err)
	}
	_, err = wdb.Exec(`
INSERT INTO session (id, project_id, slug, directory, title, time_created, time_updated)
VALUES ('session-done', 'proj-1', 'done', '/test/done', 'Done Session', 1700000500000, 1700000600000);
INSERT INTO message (id, session_id, data, time_created)
VALUES ('msg-done', 'session-done', '{"role":"assistant","time":{"created":1700000501000,"completed":1700000509000}}', 1700000501000);
`)
	if err != nil {
		wdb.Close()
		t.Fatalf("insert: %v", err)
	}
	wdb.Close()

	role, completed, err := d.GetLastMessageRoleCompleted("session-done")
	if err != nil {
		t.Fatalf("GetLastMessageRoleCompleted() error = %v", err)
	}
	if role != "assistant" {
		t.Errorf("Expected role 'assistant', got %q", role)
	}
	if completed != 1700000509000 {
		t.Errorf("Expected completed 1700000509000, got %d", completed)
	}
}
