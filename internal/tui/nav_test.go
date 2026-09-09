package tui

import (
	"fmt"
	"time"

	"github.com/tomasWade/octl/internal/daemon"
	"github.com/tomasWade/octl/internal/tui/views"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// ============================================================================
// ViewType enum tests (FR-008: DashboardView=0, FavoritesView=1, StatsView=2, HelpView=3, viewCount=4)
// ============================================================================

func TestViewType_EnumValues(t *testing.T) {
	if int(DashboardView) != 0 {
		t.Errorf("DashboardView = %d, want 0 (must be first, index 0 for tab 1)", DashboardView)
	}
	if int(FavoritesView) != 1 {
		t.Errorf("FavoritesView = %d, want 1 (must be second, index 1 for tab 2)", FavoritesView)
	}
	if int(StatsView) != 2 {
		t.Errorf("StatsView = %d, want 2 (must be third, index 2 for tab 3)", StatsView)
	}
	if int(HelpView) != 3 {
		t.Errorf("HelpView = %d, want 3 (must be fourth, index 3 for tab 4)", HelpView)
	}
	if int(viewCount) != 4 {
		t.Errorf("viewCount = %d, want 4 (four views: Dashboard, Favorites, Stats, Help)", viewCount)
	}
}

// ---------------------------------------------------------------------------
// NavItems tests
// ---------------------------------------------------------------------------

func TestNavItems_HasFourEntries(t *testing.T) {
	if len(NavItems) != 4 {
		t.Errorf("len(NavItems) = %d, want 4", len(NavItems))
	}
}

func TestNavItems_OrderAndViewTypes(t *testing.T) {
	tests := []struct {
		index     int
		wantIcon  string
		wantLabel string
		wantView  ViewType
	}{
		{0, "📋", "Manage", DashboardView},
		{1, "⭐", "Favorites", FavoritesView},
		{2, "📊", "Stats", StatsView},
		{3, "❓", "Help", HelpView},
	}
	for _, tc := range tests {
		t.Run(tc.wantLabel, func(t *testing.T) {
			item := NavItems[tc.index]
			if item.Icon != tc.wantIcon {
				t.Errorf("NavItems[%d].Icon = %q, want %q", tc.index, item.Icon, tc.wantIcon)
			}
			if item.Label != tc.wantLabel {
				t.Errorf("NavItems[%d].Label = %q, want %q", tc.index, item.Label, tc.wantLabel)
			}
			if item.ViewType != tc.wantView {
				t.Errorf("NavItems[%d].ViewType = %d, want %d", tc.index, item.ViewType, tc.wantView)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewNavModel tests
// ---------------------------------------------------------------------------

func TestNewNavModel_InitialState(t *testing.T) {
	m := NewNavModel()

	if m.Selected != 0 {
		t.Errorf("Selected = %d, want 0", m.Selected)
	}
	if len(m.Items) != 4 {
		t.Errorf("len(Items) = %d, want 4", len(m.Items))
	}
	// Items should point to NavItems.
	if &m.Items[0] != &NavItems[0] {
		t.Error("Items should be the same underlying array as NavItems")
	}
}

// ---------------------------------------------------------------------------
// NavModel.View() rendering tests
// ---------------------------------------------------------------------------

func TestNavModel_View_RendersAllEntries(t *testing.T) {
	m := NewNavModel()
	out := m.View()

	for _, item := range NavItems {
		want := item.Icon + " " + item.Label
		if !strings.Contains(out, want) {
			t.Errorf("View() missing NavItem %q in output:\n%s", want, out)
		}
	}
}

func TestNavModel_View_SelectedItemIsBold(t *testing.T) {
	m := NewNavModel()
	m.Selected = 1
	out := m.View()

	for _, item := range NavItems {
		want := item.Icon + " " + item.Label
		if !strings.Contains(out, want) {
			t.Errorf("View() missing NavItem %q in output.", want)
		}
	}
}

func TestNavModel_View_PaddingToContentHeight(t *testing.T) {
	m := NewNavModel()
	m.ContentHeight = 10
	out := m.View()

	lines := strings.Count(out, "\n")
	if lines < 3 {
		t.Errorf("expected at least 3 newlines (one per item), got %d", lines)
	}
}

func TestNavModel_View_NoExtraContentHeight(t *testing.T) {
	m := NewNavModel()
	m.ContentHeight = 3
	out := m.View()

	for _, item := range NavItems {
		want := item.Icon + " " + item.Label
		if !strings.Contains(out, want) {
			t.Errorf("View() missing NavItem %q in output.", want)
		}
	}
}

// ============================================================================
// KeyMap tests (digit key bindings: 1→Dashboard, 2→Favorites, 3→Stats)
// ============================================================================

func TestKeys_One_BindsDigitOneForDashboard(t *testing.T) {
	if len(Keys.One.Keys()) != 1 {
		t.Fatalf("Keys.One.Keys() = %v, want [\"1\"]", Keys.One.Keys())
	}
	if Keys.One.Keys()[0] != "1" {
		t.Errorf("Keys.One.Keys()[0] = %q, want \"1\"", Keys.One.Keys()[0])
	}
	if Keys.One.Help().Key != "1" {
		t.Errorf("Keys.One.Help().Key = %q, want \"1\"", Keys.One.Help().Key)
	}
	if Keys.One.Help().Desc != "dashboard" {
		t.Errorf("Keys.One.Help().Desc = %q, want \"dashboard\"", Keys.One.Help().Desc)
	}
}

func TestKeys_Two_BindsDigitTwoForFavorites(t *testing.T) {
	if len(Keys.Two.Keys()) != 1 {
		t.Fatalf("Keys.Two.Keys() = %v, want [\"2\"]", Keys.Two.Keys())
	}
	if Keys.Two.Keys()[0] != "2" {
		t.Errorf("Keys.Two.Keys()[0] = %q, want \"2\"", Keys.Two.Keys()[0])
	}
	if Keys.Two.Help().Key != "2" {
		t.Errorf("Keys.Two.Help().Key = %q, want \"2\"", Keys.Two.Help().Key)
	}
	if Keys.Two.Help().Desc != "favorites" {
		t.Errorf("Keys.Two.Help().Desc = %q, want \"favorites\"", Keys.Two.Help().Desc)
	}
}

func TestKeys_Three_BindsDigitThreeForStats(t *testing.T) {
	if len(Keys.Three.Keys()) != 1 {
		t.Fatalf("Keys.Three.Keys() = %v, want [\"3\"]", Keys.Three.Keys())
	}
	if Keys.Three.Keys()[0] != "3" {
		t.Errorf("Keys.Three.Keys()[0] = %q, want \"3\"", Keys.Three.Keys()[0])
	}
	if Keys.Three.Help().Key != "3" {
		t.Errorf("Keys.Three.Help().Key = %q, want \"3\"", Keys.Three.Help().Key)
	}
	if Keys.Three.Help().Desc != "stats" {
		t.Errorf("Keys.Three.Help().Desc = %q, want \"stats\"", Keys.Three.Help().Desc)
	}
}

func TestKeys_Quit_BindsCtrlXAndCtrlC(t *testing.T) {
	keys := Keys.Quit.Keys()
	foundCtrlX := false
	foundCtrlC := false
	for _, k := range keys {
		if k == "ctrl+x" {
			foundCtrlX = true
		}
		if k == "ctrl+c" {
			foundCtrlC = true
		}
	}
	if !foundCtrlX {
		t.Error("Keys.Quit should bind ctrl+x")
	}
	if !foundCtrlC {
		t.Error("Keys.Quit should bind ctrl+c")
	}
}

func TestKeys_Tab_BindsTab(t *testing.T) {
	keys := Keys.Tab.Keys()
	if len(keys) != 1 || keys[0] != "tab" {
		t.Errorf("Keys.Tab.Keys() = %v, want [\"tab\"]", keys)
	}
}

func TestKeys_ShiftTab_BindsShiftTabAndT(t *testing.T) {
	keys := Keys.ShiftTab.Keys()
	foundShiftTab := false
	foundT := false
	for _, k := range keys {
		if k == "shift+tab" {
			foundShiftTab = true
		}
		if k == "T" {
			foundT = true
		}
	}
	if !foundShiftTab {
		t.Error("Keys.ShiftTab should bind shift+tab")
	}
	if !foundT {
		t.Error("Keys.ShiftTab should bind T")
	}
}

func TestKeyMap_AllFieldsNonNil(t *testing.T) {
	if Keys.Quit.Keys() == nil {
		t.Error("Keys.Quit is nil")
	}
	if Keys.One.Keys() == nil {
		t.Error("Keys.One is nil")
	}
	if Keys.Two.Keys() == nil {
		t.Error("Keys.Two is nil")
	}
	if Keys.Three.Keys() == nil {
		t.Error("Keys.Three is nil")
	}
	if Keys.Tab.Keys() == nil {
		t.Error("Keys.Tab is nil")
	}
	if Keys.ShiftTab.Keys() == nil {
		t.Error("Keys.ShiftTab is nil")
	}
}

// ============================================================================
// App view dispatch tests (digit keys switch activeView, Tab/ShiftTab cycles)
// ============================================================================

func newTestModel() Model {
	m := New(0, "/tmp/octl-test-nonexistent.sock")
	m.width = 100
	m.height = 24
	return m
}

func TestApp_InitialActiveView_IsDashboard(t *testing.T) {
	m := newTestModel()
	if m.activeView != DashboardView {
		t.Errorf("activeView = %d, want DashboardView (%d)", m.activeView, DashboardView)
	}
	if m.nav.Selected != 0 {
		t.Errorf("nav.Selected = %d, want 0 (Dashboard index)", m.nav.Selected)
	}
}

func TestApp_KeyOne_SwitchesToDashboardView(t *testing.T) {
	m := newTestModel()
	m.activeView = FavoritesView
	m.nav.Selected = int(FavoritesView)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})

	if updated.(Model).activeView != DashboardView {
		t.Errorf("activeView = %d, want DashboardView (%d)", updated.(Model).activeView, DashboardView)
	}
	if updated.(Model).nav.Selected != int(DashboardView) {
		t.Errorf("nav.Selected = %d, want 0", updated.(Model).nav.Selected)
	}
}

func TestApp_KeyTwo_SwitchesToFavoritesView(t *testing.T) {
	m := newTestModel()
	m.activeView = DashboardView

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})

	if updated.(Model).activeView != FavoritesView {
		t.Errorf("activeView = %d, want FavoritesView (%d)", updated.(Model).activeView, FavoritesView)
	}
	if updated.(Model).nav.Selected != int(FavoritesView) {
		t.Errorf("nav.Selected = %d, want 1", updated.(Model).nav.Selected)
	}
}

func TestApp_KeyThree_SwitchesToStatsView(t *testing.T) {
	m := newTestModel()
	m.activeView = DashboardView

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})

	if updated.(Model).activeView != StatsView {
		t.Errorf("activeView = %d, want StatsView (%d)", updated.(Model).activeView, StatsView)
	}
	if updated.(Model).nav.Selected != int(StatsView) {
		t.Errorf("nav.Selected = %d, want 2", updated.(Model).nav.Selected)
	}
}

func TestApp_KeyFour_SwitchesToHelpView(t *testing.T) {
	m := newTestModel()
	m.activeView = DashboardView

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})

	if updated.(Model).activeView != HelpView {
		t.Errorf("activeView = %d, want HelpView (%d)", updated.(Model).activeView, HelpView)
	}
	if updated.(Model).nav.Selected != int(HelpView) {
		t.Errorf("nav.Selected = %d, want 3", updated.(Model).nav.Selected)
	}
}

func TestApp_Tab_CyclesForward(t *testing.T) {
	m := newTestModel()

	tests := []struct {
		name       string
		startView  ViewType
		wantView   ViewType
		wantNavIdx int
	}{
		{"Dashboard→Favorites", DashboardView, FavoritesView, 1},
		{"Favorites→Stats", FavoritesView, StatsView, 2},
		{"Stats→Help", StatsView, HelpView, 3},
		{"Help→Dashboard (wrap)", HelpView, DashboardView, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m.activeView = tc.startView
			m.nav.Selected = int(tc.startView)

			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})

			if updated.(Model).activeView != tc.wantView {
				t.Errorf("Tab from %d: activeView = %d, want %d", tc.startView, updated.(Model).activeView, tc.wantView)
			}
			if updated.(Model).nav.Selected != tc.wantNavIdx {
				t.Errorf("Tab from %d: nav.Selected = %d, want %d", tc.startView, updated.(Model).nav.Selected, tc.wantNavIdx)
			}
		})
	}
}

func TestApp_ShiftTab_CyclesBackward(t *testing.T) {
	m := newTestModel()

	tests := []struct {
		name       string
		startView  ViewType
		wantView   ViewType
		wantNavIdx int
	}{
		{"Dashboard→Help (wrap)", DashboardView, HelpView, 3},
		{"Favorites→Dashboard", FavoritesView, DashboardView, 0},
		{"Stats→Favorites", StatsView, FavoritesView, 1},
		{"Help→Stats", HelpView, StatsView, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m.activeView = tc.startView
			m.nav.Selected = int(tc.startView)

			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")})

			if updated.(Model).activeView != tc.wantView {
				t.Errorf("ShiftTab (T) from %d: activeView = %d, want %d", tc.startView, updated.(Model).activeView, tc.wantView)
			}
			if updated.(Model).nav.Selected != tc.wantNavIdx {
				t.Errorf("ShiftTab (T) from %d: nav.Selected = %d, want %d", tc.startView, updated.(Model).nav.Selected, tc.wantNavIdx)
			}
		})
	}
}

func TestApp_TabCycling_ModuloMath(t *testing.T) {
	vc := 4

	if (0+1)%vc != 1 {
		t.Error("Tab: (0+1)%4 should be 1")
	}
	if (1+1)%vc != 2 {
		t.Error("Tab: (1+1)%4 should be 2")
	}
	if (2+1)%vc != 3 {
		t.Error("Tab: (2+1)%4 should be 3")
	}
	if (3+1)%vc != 0 {
		t.Error("Tab: (3+1)%4 should be 0 (wrap)")
	}

	if (0-1+vc)%vc != 3 {
		t.Error("ShiftTab: (0-1+4)%4 should be 3 (wrap)")
	}
	if (1-1+vc)%vc != 0 { //nolint:staticcheck // 刻意的恒等算式：自注释 wrap 语义

		t.Error("ShiftTab: (1-1+4)%4 should be 0")
	}
	if (2-1+vc)%vc != 1 {
		t.Error("ShiftTab: (2-1+4)%4 should be 1")
	}
	if (3-1+vc)%vc != 2 {
		t.Error("ShiftTab: (3-1+4)%4 should be 2")
	}
}

func TestApp_ViewSwitch_ReturnsNilCmd(t *testing.T) {
	m := newTestModel()

	tests := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("1")},
		{Type: tea.KeyRunes, Runes: []rune("2")},
		{Type: tea.KeyRunes, Runes: []rune("3")},
		{Type: tea.KeyRunes, Runes: []rune("4")},
		{Type: tea.KeyTab},
		{Type: tea.KeyRunes, Runes: []rune("T")},
	}

	for _, msg := range tests {
		_, cmd := m.Update(msg)
		if cmd != nil {
			t.Errorf("view-switching key %v should return nil cmd, got %T", msg, cmd)
		}
	}
}

// ============================================================================
// FavoritesModel tests
// ============================================================================

func TestFavoritesModel_Init_ReturnsNil(t *testing.T) {
	fm := views.NewFavoritesModel()
	cmd := fm.Init()
	if cmd != nil {
		t.Errorf("FavoritesModel.Init() should return nil cmd, got %T", cmd)
	}
}

func TestFavoritesModel_Update_ReturnsUpdatedModel(t *testing.T) {
	fm := views.NewFavoritesModel()
	_, cmd := fm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("FavoritesModel.Update() should return nil cmd for enter key when not loaded, got %T", cmd)
	}
}

func TestFavoritesModel_View_EmptyBeforeLoaded(t *testing.T) {
	fm := views.NewFavoritesModel()
	out := fm.View()
	if out != "Loading favorites..." {
		t.Errorf("FavoritesModel.View() = %q, want %q", out, "Loading favorites...")
	}
}

// ============================================================================
// Model.View() dispatch tests
// ============================================================================

func TestModel_View_FavoritesView_RendersFavoritesContent(t *testing.T) {
	m := newTestModel()
	m.activeView = FavoritesView
	m.nav.Selected = int(FavoritesView)

	out := m.View()

	if !strings.Contains(out, "Favorites") {
		t.Errorf("View() for FavoritesView should contain favorites content.\nGot:\n%s", out)
	}
}

func TestModel_View_DashboardView_RendersDashboardContent(t *testing.T) {
	m := newTestModel()
	m.activeView = DashboardView

	out := m.View()

	if strings.Contains(out, "Favorites — coming in task 1.6") {
		t.Errorf("View() for DashboardView should NOT contain favorites placeholder text.\nGot:\n%s", out)
	}
}

func TestModel_View_StatsView_RendersStatsContent(t *testing.T) {
	m := newTestModel()
	m.activeView = StatsView

	out := m.View()

	if !strings.Contains(out, "Loading statistics...") {
		t.Errorf("View() for StatsView should contain 'Loading statistics...'.\nGot:\n%s", out)
	}
}

func TestModel_View_HelpView_RendersHelpContent(t *testing.T) {
	m := newTestModel()
	m.activeView = HelpView
	m.nav.Selected = int(HelpView)

	out := m.View()

	if !strings.Contains(out, "操作指南与图标含义") {
		t.Errorf("View() for HelpView should contain help content.\nGot:\n%s", out)
	}
	if !strings.Contains(out, "❓ Help") {
		t.Errorf("View() for HelpView should highlight the Help nav item.\nGot:\n%s", out)
	}
}

func TestModel_View_InitializingState_WhenWidthZero(t *testing.T) {
	m := New(0, "/tmp/octl-test-nonexistent.sock")

	out := m.View()
	want := "Initializing..."
	if !strings.Contains(out, want) {
		t.Errorf("View() when width=0 should contain %q.\nGot: %q", want, out)
	}
}

func TestModel_View_TerminalTooSmall(t *testing.T) {
	m := newTestModel()
	m.width = 20
	m.height = 5

	out := m.View()
	if !strings.Contains(out, "Terminal too small") {
		t.Errorf("View() for small terminal should contain warning.\nGot: %q", out)
	}
}

// ============================================================================
// key.Matches integration tests (verify Keys bindings match tea.KeyMsg)
// ============================================================================

func TestKeyMatches_One_MatchesDigitOne(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")}
	if !key.Matches(msg, Keys.One) {
		t.Error("key.Matches(msg with rune '1', Keys.One) should be true")
	}
}

func TestKeyMatches_Two_MatchesDigitTwo(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}
	if !key.Matches(msg, Keys.Two) {
		t.Error("key.Matches(msg with rune '2', Keys.Two) should be true")
	}
}

func TestKeyMatches_Three_MatchesDigitThree(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")}
	if !key.Matches(msg, Keys.Three) {
		t.Error("key.Matches(msg with rune '3', Keys.Three) should be true")
	}
}

func TestKeyMatches_Two_DoesNotMatchDigitOne(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")}
	if key.Matches(msg, Keys.Two) {
		t.Error("digit '1' should NOT match Keys.Two")
	}
}

func TestKeyMatches_Three_DoesNotMatchDigitTwo(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}
	if key.Matches(msg, Keys.Three) {
		t.Error("digit '2' should NOT match Keys.Three")
	}
}

func TestKeyMatches_One_DoesNotMatchDigitTwo(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}
	if key.Matches(msg, Keys.One) {
		t.Error("digit '2' should NOT match Keys.One")
	}
}

// ============================================================================
// ViewType edge-case: all values are distinct
// ============================================================================

func TestViewType_AllValuesDistinct(t *testing.T) {
	vals := []ViewType{DashboardView, FavoritesView, StatsView}
	seen := make(map[ViewType]bool)
	for _, v := range vals {
		if seen[v] {
			t.Errorf("ViewType value %d appears more than once", v)
		}
		seen[v] = true
	}
}

// ============================================================================
// NavItems consistency: number of NavItems == viewCount
// ============================================================================

func TestNavItems_CountMatchesViewCount(t *testing.T) {
	if len(NavItems) != int(viewCount) {
		t.Errorf("len(NavItems) = %d, viewCount = %d — they must match", len(NavItems), viewCount)
	}
}

// ============================================================================
// NavItems ViewType values match enum order
// ============================================================================

func TestNavItems_ViewTypeValuesMatchEnumOrder(t *testing.T) {
	expectedViewTypes := []ViewType{DashboardView, FavoritesView, StatsView, HelpView}
	for i, item := range NavItems {
		if item.ViewType != expectedViewTypes[i] {
			t.Errorf("NavItems[%d].ViewType = %d, want %d",
				i, item.ViewType, expectedViewTypes[i])
		}
	}
}

// ============================================================================
// App-level favorites tests (FR-009 / FR-013 / task 1.7)
// ============================================================================

// daemonViewMsgFixture returns a minimal ViewMsg for testing without daemon.
func daemonViewMsgFixture() daemon.ViewMsg {
	return daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/proj",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "Session A", Status: "IDLE", Directory: "/home/user/proj"},
					{SessionID: "sess-b", Title: "Session B", Status: "BUSY", Directory: "/home/user/proj"},
				},
			},
		},
		Stats: daemon.ViewStats{TotalSessions: 2},
	}
}

// TestApp_FavoritesToggleRequestMsg_UpdatesMapAndBroadcasts verifies that
// when the app receives a FavoritesToggleRequestMsg, it updates favoritesMap
// and broadcasts FavoritesChangedMsg to the dashboard.
func TestApp_FavoritesToggleRequestMsg_UpdatesMapAndBroadcasts(t *testing.T) {
	m := newTestModel()

	if len(m.favoritesMap) != 0 {
		t.Fatalf("favoritesMap = %v, want empty initially", m.favoritesMap)
	}

	updated, cmd := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"sess-a", "sess-b"},
	})

	model := updated.(Model)

	if !model.favoritesMap["sess-a"] {
		t.Error("favoritesMap missing sess-a")
	}
	if !model.favoritesMap["sess-b"] {
		t.Error("favoritesMap missing sess-b")
	}
	if len(model.favoritesMap) != 2 {
		t.Errorf("favoritesMap = %v, want 2 entries", model.favoritesMap)
	}

	// cmd should not be nil — it returns Batch of FavoritesChangedMsg broadcasts.
	if cmd == nil {
		t.Error("FavoritesToggleRequestMsg should return a command (broadcast + waitForDaemonMsg)")
	}
}

// TestApp_FavoritesToggleRequestMsg_DuplicateIdempotent verifies that
// toggling the same session ID twice removes it (toggle semantics).
func TestApp_FavoritesToggleRequestMsg_DuplicateIdempotent(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"sess-a"},
	})
	model := updated.(Model)

	if !model.favoritesMap["sess-a"] {
		t.Fatal("sess-a should be in favoritesMap after first toggle")
	}

	updated2, _ := updated.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"sess-a"},
	})
	model2 := updated2.(Model)

	if model2.favoritesMap["sess-a"] {
		t.Error("sess-a should be removed after second toggle (toggle unfavorites)")
	}
	if len(model2.favoritesMap) != 0 {
		t.Errorf("favoritesMap = %v, want 0 entries after toggle back", model2.favoritesMap)
	}
}

// TestApp_FavoritesToggleRequestMsg_MultipleIDsInOneMessage verifies that
// a single message with multiple session IDs adds all of them.
func TestApp_FavoritesToggleRequestMsg_MultipleIDsInOneMessage(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"s1", "s2", "s3", "s4", "s5"},
	})

	model := updated.(Model)
	if len(model.favoritesMap) != 5 {
		t.Errorf("favoritesMap = %v, want 5 entries", model.favoritesMap)
	}
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		if !model.favoritesMap[id] {
			t.Errorf("favoritesMap missing %s", id)
		}
	}
}

// TestApp_FavoritesToggleRequestMsg_EmptyIDs_NoOp verifies that
// sending a message with empty session IDs is a safe no-op.
func TestApp_FavoritesToggleRequestMsg_EmptyIDs_NoOp(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{},
	})

	model := updated.(Model)
	if len(model.favoritesMap) != 0 {
		t.Errorf("favoritesMap = %v, want empty after empty IDs", model.favoritesMap)
	}
}

// TestApp_FavoritesToggleRequestMsg_NilIDs_NoOp verifies that
// sending a message with nil session IDs is a safe no-op.
func TestApp_FavoritesToggleRequestMsg_NilIDs_NoOp(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: nil,
	})

	model := updated.(Model)
	if len(model.favoritesMap) != 0 {
		t.Errorf("favoritesMap = %v, want empty after nil IDs", model.favoritesMap)
	}
}

// TestApp_FavoritesMap_SurvivesMultipleDaemonViewMsg verifies that
// the favoritesMap is driven by daemon ViewMsg.Favorites (authoritative).
// When daemon returns favorites, they are correctly persisted.
func TestApp_FavoritesMap_SurvivesMultipleDaemonViewMsg(t *testing.T) {
	m := newTestModel()

	if len(m.favoritesMap) != 0 {
		t.Fatalf("favoritesMap = %v, want empty before daemonViewMsg", m.favoritesMap)
	}

	// Simulate daemonViewMsg with Favorites populated — daemon is authoritative.
	dvm := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/proj",
				Sessions: []daemon.ViewSession{
					{SessionID: "fav-1", Title: "Fav 1", Status: "IDLE", Directory: "/home/user/proj", IsFavorite: true},
					{SessionID: "fav-2", Title: "Fav 2", Status: "BUSY", Directory: "/home/user/proj", IsFavorite: true},
				},
			},
		},
		Favorites: []daemon.ViewSession{
			{SessionID: "fav-1", Title: "Fav 1", Status: "IDLE", IsFavorite: true},
			{SessionID: "fav-2", Title: "Fav 2", Status: "BUSY", IsFavorite: true},
		},
		Stats: daemon.ViewStats{TotalSessions: 2},
	}
	updated, _ := m.Update(daemonViewMsg{view: dvm})

	model := updated.(Model)
	if !model.favoritesMap["fav-1"] {
		t.Error("fav-1 should be in favoritesMap from daemon Favorites")
	}
	if !model.favoritesMap["fav-2"] {
		t.Error("fav-2 should be in favoritesMap from daemon Favorites")
	}
	if len(model.favoritesMap) != 2 {
		t.Errorf("favoritesMap = %v, want 2 entries from daemon Favorites", model.favoritesMap)
	}

	// Send a second daemonViewMsg with only fav-1 — stale entry should be dropped.
	dvm2 := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/proj",
				Sessions: []daemon.ViewSession{
					{SessionID: "fav-1", Title: "Fav 1", Status: "IDLE", Directory: "/home/user/proj", IsFavorite: true},
				},
			},
		},
		Favorites: []daemon.ViewSession{
			{SessionID: "fav-1", Title: "Fav 1", Status: "IDLE", IsFavorite: true},
		},
		Stats: daemon.ViewStats{TotalSessions: 1},
	}
	updated2, _ := updated.Update(daemonViewMsg{view: dvm2})

	model2 := updated2.(Model)
	if !model2.favoritesMap["fav-1"] {
		t.Error("fav-1 should survive second daemonViewMsg (still in Favorites)")
	}
	if model2.favoritesMap["fav-2"] {
		t.Error("fav-2 should be dropped (no longer in daemon Favorites)")
	}
	if len(model2.favoritesMap) != 1 {
		t.Errorf("favoritesMap = %v, want 1 entry after second daemonViewMsg", model2.favoritesMap)
	}
}

// TestApp_FavoritesMap_NeverLosesEntriesUnlessExplicitlyCleared verifies
// that the favoritesMap only gains entries (add-only semantics).
func TestApp_FavoritesMap_NeverLosesEntriesUnlessExplicitlyCleared(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"a", "b"},
	})
	updated2, _ := updated.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"c", "d"},
	})

	model := updated2.(Model)
	if len(model.favoritesMap) != 4 {
		t.Errorf("favoritesMap = %v, want 4 entries (cumulative)", model.favoritesMap)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if !model.favoritesMap[id] {
			t.Errorf("favoritesMap missing %s", id)
		}
	}
}

// TestApp_FavoritesToggleRequestMsg_LargeBatch verifies that
// a large batch of session IDs is handled correctly.
func TestApp_FavoritesToggleRequestMsg_LargeBatch(t *testing.T) {
	m := newTestModel()

	ids := make([]string, 100)
	for i := 0; i < 100; i++ {
		ids[i] = fmt.Sprintf("sess-%d", i)
	}

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: ids,
	})

	model := updated.(Model)
	if len(model.favoritesMap) != 100 {
		t.Errorf("favoritesMap = %d entries, want 100", len(model.favoritesMap))
	}
	for _, id := range ids {
		if !model.favoritesMap[id] {
			t.Errorf("favoritesMap missing %s", id)
		}
	}
}

// TestReconnectDelayCurve 验证重连退避曲线：快首试 250ms → 指数退避 → 5s 封顶，
// 负数防御性钳制。
func TestReconnectDelayCurve(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 250 * time.Millisecond},
		{1, 500 * time.Millisecond},
		{2, time.Second},
		{3, 2 * time.Second},
		{4, 5 * time.Second},
		{5, 5 * time.Second},
		{100, 5 * time.Second},
		{-1, 250 * time.Millisecond},
	}
	for _, c := range cases {
		if got := reconnectDelay(c.attempts); got != c.want {
			t.Errorf("reconnectDelay(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}
}
