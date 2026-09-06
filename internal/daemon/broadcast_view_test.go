package daemon

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomasWade/octl/internal/plugins"
)

// newIntegrationSMWithSeed is like newIntegrationSM but allows seeding the DB.
func newIntegrationSMWithSeed(t *testing.T, seedFn func(wdb *sql.DB)) (*StateManager, string, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "octl-int.sock")
	database := setupDBWithData(t, seedFn)

	sm := NewStateManagerWithSocket(database, sockPath)
	cleanup := func() {
		sm.Close()
		database.Close()
	}
	return sm, sockPath, cleanup
}

// TestIntegration_BroadcastView verifies that a view subscriber receives the
// initial view and subsequent view pushes after events.
func TestIntegration_BroadcastView(t *testing.T) {
	sm, sockPath, cleanup := newIntegrationSMWithSeed(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, name, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
			"proj1", "/tmp/proj1", "proj1", 1, 1)
		wdb.Exec(`INSERT INTO session (id, project_id, slug, version, title, directory, agent, model, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"view-session", "proj1", "view-session", "v1", "View Session", "/tmp/proj1", "test-agent", "test-model", 1, 1)
	})
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Subscribe to the view channel.
	sub := SubscribeMsg{
		Type:     "subscribe",
		Channels: []string{"view"},
		Version:  plugins.ProtocolMD5(),
	}
	subData, err := json.Marshal(sub)
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}
	if _, err := conn.Write(append(subData, '\n')); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Read the first line from the daemon.
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read subscribed: %v", err)
	}
	var ack BaseMsg
	if err := json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("parse subscribed: %v", err)
	}
	if ack.Type != "subscribed" {
		t.Fatalf("expected subscribed, got %s", ack.Type)
	}

	// Read the initial view.
	line, err = r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read initial view: %v", err)
	}
	var view1 ViewMsg
	if err := json.Unmarshal(line, &view1); err != nil {
		t.Fatalf("parse initial view: %v", err)
	}
	if view1.Type != "view" {
		t.Fatalf("expected view, got %s", view1.Type)
	}

	// Send a session.status event from another connection.
	evConn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial event source: %v", err)
	}
	defer evConn.Close()
	writeEvent(t, evConn, "session.status", map[string]interface{}{
		"sessionID": "view-session",
		"status":    map[string]interface{}{"type": "busy"},
	})

	// Wait for the broadcast view.
	done := make(chan struct{})
	go func() {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Errorf("read broadcast view: %v", err)
			close(done)
			return
		}
		var view2 ViewMsg
		if err := json.Unmarshal(line, &view2); err != nil {
			t.Errorf("parse broadcast view: %v", err)
			close(done)
			return
		}
		if view2.Type != "view" {
			t.Errorf("expected view, got %s", view2.Type)
		}
		found := false
		for _, p := range view2.Projects {
			for _, s := range p.Sessions {
				if s.SessionID == "view-session" && s.Status == StatusBusy {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("broadcast view did not contain busy view-session")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broadcast view")
	}
}
