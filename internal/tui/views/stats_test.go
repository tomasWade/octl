package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tomasWade/octl/internal/daemon"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testViewStats returns a ViewStats fixture with non-zero values.
func testViewStats() daemon.ViewStats {
	return daemon.ViewStats{
		TotalSessions:  42,
		ActiveSessions: 10,
		TotalCost:      123.45,
		TotalTokens:    2_300_000,
	}
}

// ---------------------------------------------------------------------------
// NewStatsModel tests
// ---------------------------------------------------------------------------

func TestNewStatsModel_InitialState(t *testing.T) {
	m := NewStatsModel()

	if m.loaded {
		t.Error("loaded should be false initially")
	}
	if m.stats.TotalSessions != 0 {
		t.Errorf("TotalSessions = %d, want 0", m.stats.TotalSessions)
	}
	if m.width != 0 {
		t.Errorf("width = %d, want 0", m.width)
	}
	if m.height != 0 {
		t.Errorf("height = %d, want 0", m.height)
	}
}

// ---------------------------------------------------------------------------
// Init tests
// ---------------------------------------------------------------------------

func TestStatsModel_Init_ReturnsNilCmd(t *testing.T) {
	m := NewStatsModel()
	cmd := m.Init()
	if cmd != nil {
		t.Error("Init() should return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// ViewLoadedMsg update tests
// ---------------------------------------------------------------------------

func TestUpdate_ViewLoadedMsg_SetsStats(t *testing.T) {
	m := NewStatsModel()
	view := daemon.ViewMsg{
		Type:  "view",
		Stats: testViewStats(),
	}

	updatedModel, cmd := m.Update(ViewLoadedMsg{View: view})

	if cmd != nil {
		t.Error("ViewLoadedMsg should return nil cmd")
	}
	if !updatedModel.loaded {
		t.Error("loaded should be true after ViewLoadedMsg")
	}
	if updatedModel.stats.TotalSessions != 42 {
		t.Errorf("TotalSessions = %d, want 42", updatedModel.stats.TotalSessions)
	}
	if updatedModel.stats.ActiveSessions != 10 {
		t.Errorf("ActiveSessions = %d, want 10", updatedModel.stats.ActiveSessions)
	}
	if updatedModel.stats.TotalCost != 123.45 {
		t.Errorf("TotalCost = %v, want 123.45", updatedModel.stats.TotalCost)
	}
	if updatedModel.stats.TotalTokens != 2_300_000 {
		t.Errorf("TotalTokens = %d, want 2300000", updatedModel.stats.TotalTokens)
	}
}

func TestUpdate_ViewLoadedMsg_OverwritesPreviousStats(t *testing.T) {
	m := NewStatsModel()
	m.loaded = true
	m.stats = daemon.ViewStats{TotalSessions: 1}

	view := daemon.ViewMsg{
		Type:  "view",
		Stats: testViewStats(),
	}
	updatedModel, _ := m.Update(ViewLoadedMsg{View: view})

	if updatedModel.stats.TotalSessions != 42 {
		t.Errorf("TotalSessions = %d, want 42", updatedModel.stats.TotalSessions)
	}
}

// ---------------------------------------------------------------------------
// WindowSizeMsg tests
// ---------------------------------------------------------------------------

func TestUpdate_WindowSizeMsg_StoresDimensions(t *testing.T) {
	m := NewStatsModel()
	msg := tea.WindowSizeMsg{Width: 100, Height: 50}
	updatedModel, cmd := m.Update(msg)

	if cmd != nil {
		t.Error("WindowSizeMsg should return nil cmd")
	}
	if updatedModel.width != 100 {
		t.Errorf("width = %d, want 100", updatedModel.width)
	}
	if updatedModel.height != 50 {
		t.Errorf("height = %d, want 50", updatedModel.height)
	}
}

// ---------------------------------------------------------------------------
// View rendering tests
// ---------------------------------------------------------------------------

func TestStatsView_LoadingState(t *testing.T) {
	m := NewStatsModel()
	out := m.View()

	if !strings.Contains(out, "Loading statistics...") {
		t.Errorf("View() for loading state should contain 'Loading statistics...': %q", out)
	}
}

func TestStatsView_LoadedWithData(t *testing.T) {
	m := NewStatsModel()
	updatedModel, _ := m.Update(ViewLoadedMsg{View: daemon.ViewMsg{Stats: testViewStats()}})

	out := updatedModel.View()

	if !strings.Contains(out, "Global Statistics") {
		t.Errorf("View() should contain 'Global Statistics': %q", out)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("View() should contain total sessions count: %q", out)
	}
	if !strings.Contains(out, "10") {
		t.Errorf("View() should contain active sessions count: %q", out)
	}
	if !strings.Contains(out, "$123.45") {
		t.Errorf("View() should contain total cost '$123.45': %q", out)
	}
	if !strings.Contains(out, "2.3M") {
		t.Errorf("View() should contain total formatted tokens '2.3M': %q", out)
	}
}

func TestStatsView_LoadedWithZeroStats(t *testing.T) {
	m := NewStatsModel()
	updatedModel, _ := m.Update(ViewLoadedMsg{View: daemon.ViewMsg{Stats: daemon.ViewStats{}}})

	out := updatedModel.View()
	if !strings.Contains(out, "Global Statistics") {
		t.Errorf("View() should still render global statistics: %q", out)
	}
	if !strings.Contains(out, "$0.00") {
		t.Errorf("View() should show '$0.00' for zero cost: %q", out)
	}
	if !strings.Contains(out, "0") {
		t.Errorf("View() should show zero values: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestUpdate_UnrelatedMsg_NoChange(t *testing.T) {
	m := NewStatsModel()
	m.loaded = true
	m.stats = testViewStats()
	originalStats := m.stats

	updatedModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd != nil {
		t.Error("unrelated message should return nil cmd")
	}
	if updatedModel.stats != originalStats {
		t.Error("stats should be preserved")
	}
	if updatedModel.loaded != m.loaded {
		t.Error("loaded should be preserved")
	}
}

func TestUpdate_ViewLoadedMsg_LargeValues(t *testing.T) {
	m := NewStatsModel()
	stats := daemon.ViewStats{
		TotalSessions:  1<<31 - 1,
		ActiveSessions: 0,
		TotalCost:      1e15,
		TotalTokens:    1<<62 - 1,
	}
	updatedModel, _ := m.Update(ViewLoadedMsg{View: daemon.ViewMsg{Stats: stats}})
	if !updatedModel.loaded {
		t.Error("loaded should be true")
	}
	out := updatedModel.View()
	if !strings.Contains(out, "2147483647") {
		t.Errorf("View() should show max int32 session count: %q", out)
	}
}

func TestUpdate_ViewLoadedMsg_NegativeCost(t *testing.T) {
	m := NewStatsModel()
	stats := daemon.ViewStats{TotalCost: -5.00}
	updatedModel, _ := m.Update(ViewLoadedMsg{View: daemon.ViewMsg{Stats: stats}})
	if !updatedModel.loaded {
		t.Error("loaded should be true")
	}
	out := updatedModel.View()
	if !strings.Contains(out, "$-5.00") {
		t.Errorf("View() should show negative cost '$-5.00': %q", out)
	}
}

// ---------------------------------------------------------------------------
// formatCost and formatTokens (used in stats view)
// ---------------------------------------------------------------------------

func TestFormatTokens_Zero(t *testing.T) {
	got := formatTokens(0)
	want := "0"
	if got != want {
		t.Errorf("formatTokens(0) = %q, want %q", got, want)
	}
}

func TestFormatTokens_Hundreds(t *testing.T) {
	got := formatTokens(500)
	want := "500"
	if got != want {
		t.Errorf("formatTokens(500) = %q, want %q", got, want)
	}
}

func TestFormatTokens_Thousands(t *testing.T) {
	got := formatTokens(1500)
	want := "1.5K"
	if got != want {
		t.Errorf("formatTokens(1500) = %q, want %q", got, want)
	}
}

func TestFormatTokens_Millions(t *testing.T) {
	got := formatTokens(2_500_000)
	want := "2.5M"
	if got != want {
		t.Errorf("formatTokens(2500000) = %q, want %q", got, want)
	}
}

func TestFormatTokens_ExactThousand(t *testing.T) {
	got := formatTokens(1000)
	want := "1.0K"
	if got != want {
		t.Errorf("formatTokens(1000) = %q, want %q", got, want)
	}
}

func TestFormatCost_Zero(t *testing.T) {
	got := formatCost(0)
	want := "$0.00"
	if got != want {
		t.Errorf("formatCost(0) = %q, want %q", got, want)
	}
}

func TestFormatCost_Small(t *testing.T) {
	got := formatCost(0.04)
	want := "$0.04"
	if got != want {
		t.Errorf("formatCost(0.04) = %q, want %q", got, want)
	}
}

func TestFormatCost_Large(t *testing.T) {
	got := formatCost(1234.56)
	want := "$1234.56"
	if got != want {
		t.Errorf("formatCost(1234.56) = %q, want %q", got, want)
	}
}

func TestFormatCost_Negative(t *testing.T) {
	got := formatCost(-5.00)
	// fmt.Sprintf("$%.2f", -5.00) produces "$-5.00"
	want := "$-5.00"
	if got != want {
		t.Errorf("formatCost(-5.00) = %q, want %q", got, want)
	}
}
