package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/tomasWade/octl/internal/daemon"
	"github.com/tomasWade/octl/internal/db"
)

func TestRealDBSchema(t *testing.T) {
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".local/share/opencode/opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Skip("real DB not found")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Part table schema
	if rows, err := db.Query("PRAGMA table_info(part)"); err == nil {
		fmt.Println("=== part table ===")
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt sql.NullString
			rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
			fmt.Printf("  %s (%s) notnull=%d pk=%d\n", name, ctype, notnull, pk)
		}
		rows.Close()
	}

	// A few part rows showing raw data
	if rows, err := db.Query(`SELECT id, session_id, substr(data,1,200) as data FROM part ORDER BY time_created DESC LIMIT 5`); err == nil {
		fmt.Println("\n=== last 5 parts (raw data) ===")
		for rows.Next() {
			var id, sid, data string
			rows.Scan(&id, &sid, &data)
			fmt.Printf("  %s | %s\n  data=%s\n\n", id, sid, data)
		}
		rows.Close()
	}

	// Message table schema
	if rows, err := db.Query("PRAGMA table_info(message)"); err == nil {
		fmt.Println("=== message table ===")
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt sql.NullString
			rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
			fmt.Printf("  %s (%s)\n", name, ctype)
		}
		rows.Close()
	}

	// A few message rows
	if rows, err := db.Query(`SELECT id, session_id, substr(data,1,200) as data FROM message ORDER BY time_created DESC LIMIT 5`); err == nil {
		fmt.Println("\n=== last 5 messages (raw data) ===")
		for rows.Next() {
			var id, sid, data string
			rows.Scan(&id, &sid, &data)
			fmt.Printf("  %s | %s\n  data=%s\n\n", id, sid, data)
		}
		rows.Close()
	}

	// Check all JSON keys in part.data
	if rows, err := db.Query(`SELECT json_keys(part.data) FROM part WHERE json_valid(part.data) LIMIT 5`); err == nil {
		fmt.Println("\n=== JSON keys in part.data ===")
		for rows.Next() {
			var keys string
			rows.Scan(&keys)
			fmt.Printf("  keys: %s\n", keys)
		}
		rows.Close()
	}

	// Check JSON keys in message.data
	if rows, err := db.Query(`SELECT json_keys(message.data) FROM message WHERE json_valid(message.data) LIMIT 5`); err == nil {
		fmt.Println("\n=== JSON keys in message.data ===")
		for rows.Next() {
			var keys string
			rows.Scan(&keys)
			fmt.Printf("  keys: %s\n", keys)
		}
		rows.Close()
	}
}

// TestSnapshotCh_NonNil verifies that daemon.NewStateManager returns a valid
// snapshot channel when the daemon is enabled.
func TestSnapshotCh_NonNil(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create an empty SQLite database so db.New can open it (read-only mode)
	writeDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to create test DB: %v", err)
	}
	writeDB.Close()

	database, err := db.New(dbPath)
	if err != nil {
		t.Fatalf("db.New() error = %v", err)
	}
	defer database.Close()

	ch := daemon.NewStateManager(database).SnapshotCh()
	if ch == nil {
		t.Error("expected non-nil SnapshotCh from StateManager")
	}
}
