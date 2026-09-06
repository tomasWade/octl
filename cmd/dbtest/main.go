package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tomasWade/octl/internal/db"
)

func main() {
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".local/share/opencode/opencode.db")

	d, err := db.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: New() error: %v\n", err)
		os.Exit(1)
	}
	defer d.Close()
	fmt.Println("PASS: New() connected to database")

	// 测试 ListSessions
	sessions, err := d.ListSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: ListSessions() error: %v\n", err)
		os.Exit(1)
	}
	if len(sessions) == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: ListSessions() returned 0 sessions\n")
		os.Exit(1)
	}
	fmt.Printf("PASS: ListSessions() = %d sessions\n", len(sessions))
	fmt.Printf("  Latest: %s | %s | Agent=%s\n", sessions[0].ID[:16], sessions[0].Title, sessions[0].Agent)

	// 测试 GetSession
	s, err := d.GetSession(sessions[0].ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: GetSession() error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("PASS: GetSession(%s...) = \"%s\"\n", s.ID[:16], s.Title)

	// 测试 GetSession 未找到
	_, err = d.GetSession("nonexistent")
	if err != db.ErrSessionNotFound {
		fmt.Fprintf(os.Stderr, "FAIL: GetSession('nonexistent') expected ErrSessionNotFound, got %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS: GetSession('nonexistent') -> ErrSessionNotFound")

	// 测试 GetSessionStats
	stats, err := d.GetSessionStats()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: GetSessionStats() error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("PASS: GetSessionStats() = %d sessions, $%.4f cost\n", stats.TotalSessions, stats.TotalCost)

	// 测试 GetModelUsage
	usage, err := d.GetModelUsage()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: GetModelUsage() error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("PASS: GetModelUsage() = %d model entries\n", len(usage))
	for _, u := range usage {
		fmt.Printf("  %s/%s: %d tokens, $%.4f\n", u.ProviderID, u.ModelID, u.TokenCount, u.Cost)
	}

	fmt.Println("\nALL TESTS PASSED")
}
