package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHelpModel_InitReturnsNil verifies Init returns nil.
func TestHelpModel_InitReturnsNil(t *testing.T) {
	m := NewHelpModel()
	cmd := m.Init()
	if cmd != nil {
		t.Errorf("HelpModel.Init() should return nil, got %T", cmd)
	}
}

// TestHelpModel_View_ContainsTitle verifies the Help view renders its title.
func TestHelpModel_View_ContainsTitle(t *testing.T) {
	m := NewHelpModel()
	out := m.View()
	if !strings.Contains(out, "Help") {
		t.Errorf("View() should contain the Help title\nGot:\n%s", out)
	}
}

// TestHelpModel_View_ExplainsStatusGlyphs verifies the Help view documents
// every first-column status glyph plus the ⭐ favorite marker.
func TestHelpModel_View_ExplainsStatusGlyphs(t *testing.T) {
	m := NewHelpModel()
	out := m.View()

	glyphs := []string{"🔵", "🟢", "🟡", "🟠", "🔴", "❔", "📦", "⭐"}
	for _, g := range glyphs {
		if !strings.Contains(out, g) {
			t.Errorf("View() should document glyph %q\nGot:\n%s", g, out)
		}
	}
}

// TestHelpModel_View_ListsKeyBindings verifies the Help view includes the
// main key bindings for the Manage and Favorites views.
func TestHelpModel_View_ListsKeyBindings(t *testing.T) {
	m := NewHelpModel()
	out := m.View()

	sections := []string{"视图切换", "Manage 视图按键", "Favorites 视图按键", "第一列图标含义"}
	for _, s := range sections {
		if !strings.Contains(out, s) {
			t.Errorf("View() should contain section %q\nGot:\n%s", s, out)
		}
	}

	keys := []string{"Space", "Ctrl+X", "Tab"}
	for _, k := range keys {
		if !strings.Contains(out, k) {
			t.Errorf("View() should mention key %q\nGot:\n%s", k, out)
		}
	}
}

// TestHelpModel_WindowSizeMsg_NoCrash verifies a resize message is handled.
func TestHelpModel_WindowSizeMsg_NoCrash(t *testing.T) {
	m := NewHelpModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if updated.width != 100 || updated.height != 30 {
		t.Errorf("width/height = %d/%d, want 100/30", updated.width, updated.height)
	}
}
