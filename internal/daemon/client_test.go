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

// TestSocketClient_SubscribeAndReceiveSnapshot verifies that a client can
// connect, subscribe, and receive a snapshot broadcast after sending an event.
func TestSocketClient_SubscribeAndReceiveSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "octl-sub.sock")

	database := setupDBWithData(t, func(wdb *sql.DB) {
		_, err := wdb.Exec(`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) VALUES ('s1', 'global', 'test', '/tmp', 'Test', '1.0', 1000, 1000)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	defer database.Close()

	sm := NewStateManagerWithSocket(database, sockPath)
	defer sm.Close()

	go func() {
		if err := sm.Run(); err != nil {
			t.Logf("Run returned: %v", err)
		}
	}()

	waitForSocket(t, sockPath, time.Second)

	client := NewSocketClientWithSocket(sockPath)
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	if err := client.Subscribe(); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Read the subscribed confirmation.
	select {
	case msg := <-client.Msgs():
		if _, ok := msg.(SubscribedMsg); !ok {
			t.Fatalf("expected subscribed message, got %T", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscribed message")
	}

	// Read the initial snapshot.
	select {
	case msg := <-client.Msgs():
		snap, ok := msg.(SnapshotMsg)
		if !ok {
			t.Fatalf("expected snapshot message, got %T", msg)
		}
		if len(snap.States) != 1 {
			t.Fatalf("expected 1 state, got %d", len(snap.States))
		}
		if snap.States[0].SessionID != "s1" {
			t.Errorf("expected session s1, got %s", snap.States[0].SessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}

	// Send an event as a plain event source (no subscribe).
	conn, err := netDialUnix(t, sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	data, err := json.Marshal(rawEvent{
		Type:       "session.status",
		Properties: map[string]interface{}{"sessionID": "s1", "status": map[string]interface{}{"type": "busy"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write event: %v", err)
	}

	// The subscriber should receive an updated snapshot.
	select {
	case msg := <-client.Msgs():
		snap, ok := msg.(SnapshotMsg)
		if !ok {
			t.Fatalf("expected snapshot message after event, got %T", msg)
		}
		state := findState(snap.States, "s1")
		if state == nil {
			t.Fatal("s1 not found in snapshot after event")
		}
		if state.Status != StatusBusy {
			t.Errorf("expected BUSY after event, got %q", state.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for updated snapshot")
	}
}

// TestSocketClient_EventSourceCompatibility verifies that a connection that
// never sends subscribe is treated as an event source and does not receive
// snapshots.
func TestSocketClient_EventSourceCompatibility(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "octl-source.sock")

	database := setupDBWithData(t, nil)
	defer database.Close()

	sm := NewStateManagerWithSocket(database, sockPath)
	defer sm.Close()

	go func() {
		if err := sm.Run(); err != nil {
			t.Logf("Run returned: %v", err)
		}
	}()

	waitForSocket(t, sockPath, time.Second)

	conn, err := netDialUnix(t, sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send an event directly without subscribing.
	data, err := json.Marshal(rawEvent{
		Type:       "session.status",
		Properties: map[string]interface{}{"sessionID": "src1", "status": map[string]interface{}{"type": "busy"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write event: %v", err)
	}

	// Give the daemon time to process.
	time.Sleep(100 * time.Millisecond)

	sm.mu.RLock()
	state, ok := sm.stateMap["src1"]
	sm.mu.RUnlock()
	if !ok {
		t.Fatal("src1 should exist in stateMap after event")
	}
	if state.Status != StatusBusy {
		t.Errorf("expected BUSY, got %q", state.Status)
	}
}

// TestSocketClient_Reconnect verifies that closing the daemon closes the
// client's message channel, and that a new daemon on the same socket can be
// connected to again.
func TestSocketClient_Reconnect(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "octl-reconnect.sock")

	database := setupDBWithData(t, nil)
	defer database.Close()

	// First daemon instance.
	sm1 := NewStateManagerWithSocket(database, sockPath)
	go func() {
		if err := sm1.Run(); err != nil {
			t.Logf("Run returned: %v", err)
		}
	}()
	waitForSocket(t, sockPath, time.Second)

	client := NewSocketClientWithSocket(sockPath)
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := client.Subscribe(); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Wait for the channel to become ready and consume the first messages.
	<-client.Msgs()
	// Give the daemon a moment to register the subscriber before shutting down.
	time.Sleep(500 * time.Millisecond)

	// Close the first daemon; the client's Msgs() channel should close.
	sm1.Close()

	// Drain any buffered messages, then verify the channel is closed.
	drained := false
	for !drained {
		select {
		case _, ok := <-client.Msgs():
			drained = !ok
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Msgs() to close")
		}
	}

	// Start a second daemon on the same socket path.
	sm2 := NewStateManagerWithSocket(database, sockPath)
	go func() {
		if err := sm2.Run(); err != nil {
			t.Logf("Run returned: %v", err)
		}
	}()
	defer sm2.Close()
	waitForSocket(t, sockPath, time.Second)

	// A fresh client should be able to connect and subscribe.
	client2 := NewSocketClientWithSocket(sockPath)
	if err := client2.Connect(); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer client2.Close()
	if err := client2.Subscribe(); err != nil {
		t.Fatalf("resubscribe: %v", err)
	}
	select {
	case msg := <-client2.Msgs():
		if _, ok := msg.(SubscribedMsg); !ok {
			t.Fatalf("expected subscribed message after reconnect, got %T", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscribed message after reconnect")
	}
}

func netDialUnix(t *testing.T, path string) (net.Conn, error) {
	t.Helper()
	return net.DialTimeout("unix", path, time.Second)
}
