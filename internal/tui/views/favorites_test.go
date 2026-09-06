package views

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tomasWade/octl/internal/daemon"
)

// ============================================================================
// Fixtures & helpers
// ============================================================================

func testFavView() daemon.ViewMsg {
	return daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Name:      "project-one",
				Worktree:  "/home/user/project-one",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "Session A", Status: daemon.StatusIdle, RowStatus: daemon.StatusIdle, Directory: "/home/user/project-one"},
					{SessionID: "sess-b", Title: "Session B", Status: daemon.StatusBusy, RowStatus: daemon.StatusBusy, Directory: "/home/user/project-one"},
				},
			},
			{
				ProjectID: "proj-2",
				Name:      "project-two",
				Worktree:  "/home/user/project-two",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-c", Title: "Session C", Status: daemon.StatusError, RowStatus: daemon.StatusError, Directory: "/home/user/project-two"},
				},
			},
		},
		Stats: daemon.ViewStats{TotalSessions: 3},
	}
}

func loadedFavModel(favs map[string]bool, width, height int) FavoritesModel {
	m := NewFavoritesModel()
	m.width = width
	m.height = height
	m.loaded = true
	m.view = testFavView()
	m.favorites = favs
	m.rebuildFavoriteIDs()
	return m
}

func favUpKey() tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")} }
func favDownKey() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")} }
func favFKey() tea.KeyMsg     { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")} }
func favDKey() tea.KeyMsg     { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")} }
func favSpaceKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}} }
func favEnterKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }
func favEscKey() tea.KeyMsg   { return tea.KeyMsg{Type: tea.KeyEsc} }
func favQKey() tea.KeyMsg     { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")} }
func favOtherKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")} }

func extractSequenceCmds(msg tea.Msg) ([]tea.Cmd, bool) {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice {
		return nil, false
	}
	cmds := make([]tea.Cmd, v.Len())
	for i := 0; i < v.Len(); i++ {
		cmd, ok := v.Index(i).Interface().(tea.Cmd)
		if !ok {
			return nil, false
		}
		cmds[i] = cmd
	}
	return cmds, true
}

func buildScrollFavModel(n, width, height int) FavoritesModel {
	favs := make(map[string]bool)
	viewSessions := make([]daemon.ViewSession, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sess-%02d", i)
		favs[id] = true
		viewSessions[i] = daemon.ViewSession{
			SessionID: id, Title: fmt.Sprintf("Item-%02d", i),
			Status: daemon.StatusIdle, RowStatus: daemon.StatusIdle,
			Directory: "/home/user/proj",
		}
	}
	view := daemon.ViewMsg{Type: "view", Projects: []daemon.ViewProject{{ProjectID: "proj-1", Sessions: viewSessions}}}
	m := NewFavoritesModel()
	m.width = width
	m.height = height
	m.loaded = true
	m.view = view
	m.favorites = favs
	m.rebuildFavoriteIDs()
	return m
}

// ============================================================================
// NewFavoritesModel — zero-value model, Init returns nil
// ============================================================================

func TestNewFavoritesModel_ZeroValue(t *testing.T) {
	m := NewFavoritesModel()
	if m.loaded {
		t.Error("loaded should be false for new model")
	}
	if len(m.favorites) != 0 {
		t.Errorf("favorites = %v, want empty", m.favorites)
	}
	if len(m.favoriteIDs) != 0 {
		t.Errorf("favoriteIDs = %v, want empty", m.favoriteIDs)
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.cursor)
	}
	if len(m.selected) != 0 {
		t.Errorf("selected = %v, want empty", m.selected)
	}
	if m.confirmingDelete {
		t.Error("confirmingDelete should be false for new model")
	}
	if len(m.pendingDeleteIDs) != 0 {
		t.Errorf("pendingDeleteIDs = %v, want empty", m.pendingDeleteIDs)
	}
}

func TestNewFavoritesModel_InitReturnsNil(t *testing.T) {
	m := NewFavoritesModel()
	cmd := m.Init()
	if cmd != nil {
		t.Errorf("Init() should return nil, got %T", cmd)
	}
}

// ============================================================================
// Not-loaded View
// ============================================================================

func TestFavoritesModel_View_NotLoaded_ReturnsLoadingText(t *testing.T) {
	m := NewFavoritesModel()
	out := m.View()
	want := "Loading favorites..."
	if out != want {
		t.Errorf("View() = %q, want %q", out, want)
	}
}

func TestFavoritesModel_Update_NotLoaded_IgnoresKeyPresses(t *testing.T) {
	m := NewFavoritesModel()
	tests := []tea.KeyMsg{
		favUpKey(), favDownKey(), favFKey(), favDKey(), favSpaceKey(),
		favEnterKey(), favEscKey(), favQKey(),
	}
	for _, key := range tests {
		updated, cmd := m.Update(key)
		if cmd != nil {
			t.Errorf("%v: should return nil cmd when not loaded, got %T", key, cmd)
		}
		if updated.loaded {
			t.Errorf("%v: loaded should remain false", key)
		}
	}
}

// ============================================================================
// ViewLoadedMsg
// ============================================================================

func TestFavoritesModel_RebuildFavoriteIDs_OrderByViewMsgAppearance(t *testing.T) {
	m := NewFavoritesModel()
	m.favorites = map[string]bool{"sess-c": true, "sess-a": true, "sess-b": true}
	m.loaded = true
	m.view = testFavView()
	m.rebuildFavoriteIDs()

	if len(m.favoriteIDs) != 3 {
		t.Fatalf("favoriteIDs = %v, want 3 entries", m.favoriteIDs)
	}
	if m.favoriteIDs[0] != "sess-a" {
		t.Errorf("favoriteIDs[0] = %q, want sess-a (first in ViewMsg)", m.favoriteIDs[0])
	}
	if m.favoriteIDs[1] != "sess-b" {
		t.Errorf("favoriteIDs[1] = %q, want sess-b", m.favoriteIDs[1])
	}
	if m.favoriteIDs[2] != "sess-c" {
		t.Errorf("favoriteIDs[2] = %q, want sess-c", m.favoriteIDs[2])
	}
}

func TestFavoritesModel_RebuildFavoriteIDs_AppendsExtraFavorites(t *testing.T) {
	m := NewFavoritesModel()
	m.favorites = map[string]bool{"sess-a": true, "sess-z": true}
	m.loaded = true
	m.view = testFavView()
	m.rebuildFavoriteIDs()

	if len(m.favoriteIDs) != 2 {
		t.Fatalf("favoriteIDs = %v, want 2 entries", m.favoriteIDs)
	}
	if m.favoriteIDs[0] != "sess-a" {
		t.Errorf("favoriteIDs[0] = %q, want sess-a", m.favoriteIDs[0])
	}
	found := false
	for _, id := range m.favoriteIDs {
		if id == "sess-z" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("favoriteIDs = %v, missing sess-z", m.favoriteIDs)
	}
}

// ============================================================================
// sessionByID
// ============================================================================

func TestFavoritesModel_SessionByID_Found(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := m.sessionByID("sess-a")
	if s.SessionID != "sess-a" {
		t.Errorf("sessionByID(sess-a).SessionID = %q, want sess-a", s.SessionID)
	}
	if s.Title != "Session A" {
		t.Errorf("sessionByID(sess-a).Title = %q, want Session A", s.Title)
	}
}

func TestFavoritesModel_SessionByID_NotFound_ReturnsZero(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := m.sessionByID("does-not-exist")
	if s.SessionID != "" {
		t.Errorf("sessionByID(not-found).SessionID = %q, want empty", s.SessionID)
	}
}

func TestFavoritesModel_SessionByID_StaleSession_DoesNotCrash(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-deleted": true}, 100, 24)
	s := m.sessionByID("sess-deleted")
	if s.SessionID != "" {
		t.Errorf("stale sessionID should return empty ViewSession, got %+v", s)
	}
	out := m.View()
	if !strings.Contains(out, "sess-deleted") {
		t.Errorf("View() should contain stale session ID as fallback title\nGot:\n%s", out)
	}
}

// ============================================================================
// FavoritesChangedMsg
// ============================================================================

func TestFavoritesModel_FavoritesChangedMsg_UpdatesMap(t *testing.T) {
	m := loadedFavModel(nil, 100, 24)
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{"sess-a": true, "sess-b": true}})
	if len(updated.favorites) != 2 {
		t.Fatalf("favorites = %v, want 2 entries", updated.favorites)
	}
	if !updated.favorites["sess-a"] {
		t.Error("favorites missing sess-a")
	}
	if !updated.favorites["sess-b"] {
		t.Error("favorites missing sess-b")
	}
}

func TestFavoritesModel_FavoritesChangedMsg_RebuildsList(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{"sess-c": true}})
	if len(updated.favoriteIDs) != 1 {
		t.Fatalf("favoriteIDs = %v, want [sess-c]", updated.favoriteIDs)
	}
	if updated.favoriteIDs[0] != "sess-c" {
		t.Errorf("favoriteIDs[0] = %q, want sess-c", updated.favoriteIDs[0])
	}
}

func TestFavoritesModel_FavoritesChangedMsg_EmptyMapClears(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{}})
	if len(updated.favoriteIDs) != 0 {
		t.Errorf("favoriteIDs = %v, want empty", updated.favoriteIDs)
	}
	if updated.cursor != 0 {
		t.Errorf("cursor = %d, want 0", updated.cursor)
	}
}

func TestFavoritesModel_FavoritesChangedMsg_NilMap_NoPanic(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: nil})
	if len(updated.favoriteIDs) != 0 {
		t.Errorf("favoriteIDs = %v, want empty for nil favorites", updated.favoriteIDs)
	}
}

func TestFavoritesModel_FavoritesChangedMsg_CleansStaleMultiSelect(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.selected = map[string]bool{"sess-a": true, "sess-z": true}
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{"sess-b": true}})
	if updated.selected["sess-a"] {
		t.Error("sess-a should be removed from selected (not in new favorites)")
	}
	if updated.selected["sess-z"] {
		t.Error("sess-z should be removed from selected (not in new favorites)")
	}
}

func TestFavoritesModel_FavoritesChangedMsg_PreservesValidSelection(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.selected = map[string]bool{"sess-a": true}
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{"sess-a": true, "sess-c": true}})
	if !updated.selected["sess-a"] {
		t.Error("sess-a should remain selected (still in favorites)")
	}
}

func TestFavoritesModel_FavoritesChangedMsg_ReturnsNilCmd(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	_, cmd := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{"sess-b": true}})
	if cmd != nil {
		t.Errorf("FavoritesChangedMsg handler must return nil cmd, got %T", cmd)
	}
}

// ============================================================================
// f key — cancel favorite
// ============================================================================

func TestFavoritesModel_FKey_CursorSession_SendsRemoveRequest(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 1
	_, cmd := m.Update(favFKey())
	if cmd == nil {
		t.Fatal("f key on a session should return a command")
	}
	msg := cmd()
	removeMsg, ok := msg.(FavoritesRemoveRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want FavoritesRemoveRequestMsg", msg)
	}
	if len(removeMsg.SessionIDs) != 1 {
		t.Fatalf("SessionIDs = %v, want [sess-b]", removeMsg.SessionIDs)
	}
	if removeMsg.SessionIDs[0] != "sess-b" {
		t.Errorf("SessionIDs[0] = %q, want sess-b", removeMsg.SessionIDs[0])
	}
}

func TestFavoritesModel_FKey_MultiSelect_SendsAllSelectedIDs(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.cursor = 0
	m.selected = map[string]bool{"sess-a": true, "sess-c": true}
	_, cmd := m.Update(favFKey())
	if cmd == nil {
		t.Fatal("f key with multi-select should return a command")
	}
	msg := cmd()
	removeMsg, ok := msg.(FavoritesRemoveRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want FavoritesRemoveRequestMsg", msg)
	}
	if len(removeMsg.SessionIDs) != 2 {
		t.Fatalf("SessionIDs = %v, want 2 entries", removeMsg.SessionIDs)
	}
	gotSet := make(map[string]bool)
	for _, id := range removeMsg.SessionIDs {
		gotSet[id] = true
	}
	if !gotSet["sess-a"] {
		t.Error("SessionIDs missing sess-a")
	}
	if !gotSet["sess-c"] {
		t.Error("SessionIDs missing sess-c")
	}
}

func TestFavoritesModel_FKey_EmptyList_NoOp(t *testing.T) {
	m := loadedFavModel(nil, 100, 24)
	_, cmd := m.Update(favFKey())
	if cmd != nil {
		t.Errorf("f key on empty list should return nil cmd, got %T", cmd)
	}
}

// ============================================================================
// d key — enters confirm mode (no longer immediately emits sequence)
// ============================================================================

func TestFavoritesModel_DKey_CursorSession_EntersConfirmMode(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	updated, cmd := m.Update(favDKey())
	if cmd != nil {
		t.Errorf("d key should NOT return a cmd (enters confirm mode), got %T", cmd)
	}
	if !updated.confirmingDelete {
		t.Fatal("d key should set confirmingDelete=true")
	}
	if len(updated.pendingDeleteIDs) != 1 {
		t.Fatalf("pendingDeleteIDs = %v, want [sess-a]", updated.pendingDeleteIDs)
	}
	if updated.pendingDeleteIDs[0] != "sess-a" {
		t.Errorf("pendingDeleteIDs[0] = %q, want sess-a", updated.pendingDeleteIDs[0])
	}
	if updated.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (preserved)", updated.cursor)
	}
}

func TestFavoritesModel_DKey_MultiSelect_EntersConfirmMode(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.cursor = 0
	m.selected = map[string]bool{"sess-a": true, "sess-c": true}
	updated, cmd := m.Update(favDKey())
	if cmd != nil {
		t.Errorf("d key should NOT return a cmd (enters confirm mode), got %T", cmd)
	}
	if !updated.confirmingDelete {
		t.Fatal("d key should set confirmingDelete=true")
	}
	if len(updated.pendingDeleteIDs) != 2 {
		t.Fatalf("pendingDeleteIDs = %v, want 2 entries", updated.pendingDeleteIDs)
	}
	gotSet := make(map[string]bool)
	for _, id := range updated.pendingDeleteIDs {
		gotSet[id] = true
	}
	if !gotSet["sess-a"] {
		t.Error("pendingDeleteIDs missing sess-a")
	}
	if !gotSet["sess-c"] {
		t.Error("pendingDeleteIDs missing sess-c")
	}
	if len(updated.selected) != 2 {
		t.Errorf("selected = %v, want 2 entries preserved", updated.selected)
	}
}

func TestFavoritesModel_DKey_EmptyList_NoOp(t *testing.T) {
	m := loadedFavModel(nil, 100, 24)
	updated, cmd := m.Update(favDKey())
	if cmd != nil {
		t.Errorf("d key on empty list should return nil cmd, got %T", cmd)
	}
	if updated.confirmingDelete {
		t.Error("d key on empty list should NOT set confirmingDelete")
	}
}

func TestFavoritesModel_DKey_NoSelection_CursorGetsSingle(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.cursor = 1
	updated, _ := m.Update(favDKey())
	if !updated.confirmingDelete {
		t.Fatal("d key should set confirmingDelete")
	}
	if len(updated.pendingDeleteIDs) != 1 {
		t.Fatalf("pendingDeleteIDs = %v, want [sess-b]", updated.pendingDeleteIDs)
	}
	if updated.pendingDeleteIDs[0] != "sess-b" {
		t.Errorf("pendingDeleteIDs[0] = %q, want sess-b", updated.pendingDeleteIDs[0])
	}
}

// ============================================================================
// Confirm mode: Enter executes delete sequence + clears state
// ============================================================================

func TestFavoritesModel_ConfirmMode_EnterExecutesSequence(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a"}

	updated, cmd := m.Update(favEnterKey())
	if cmd == nil {
		t.Fatal("Enter in confirm mode should return a command")
	}
	if updated.confirmingDelete {
		t.Error("confirmingDelete should be false after Enter")
	}
	if len(updated.pendingDeleteIDs) != 0 {
		t.Errorf("pendingDeleteIDs = %v, want empty after Enter", updated.pendingDeleteIDs)
	}

	msg := cmd()
	seq, ok := extractSequenceCmds(msg)
	if !ok {
		t.Fatalf("Enter in confirm mode should return tea.Sequence, got %T", msg)
	}
	if len(seq) != 2 {
		t.Fatalf("sequence length = %d, want 2 (delete + remove)", len(seq))
	}

	deleteMsg := seq[0]()
	actionMsg, ok := deleteMsg.(ManageActionMsg)
	if !ok {
		t.Fatalf("seq[0] returned %T, want ManageActionMsg", deleteMsg)
	}
	if actionMsg.Action != "delete" {
		t.Errorf("delete action = %q, want delete", actionMsg.Action)
	}
	if len(actionMsg.SessionIDs) != 1 || actionMsg.SessionIDs[0] != "sess-a" {
		t.Errorf("delete SessionIDs = %v, want [sess-a]", actionMsg.SessionIDs)
	}

	removeMsg := seq[1]()
	favRemoveMsg, ok := removeMsg.(FavoritesRemoveRequestMsg)
	if !ok {
		t.Fatalf("seq[1] returned %T, want FavoritesRemoveRequestMsg", removeMsg)
	}
	if len(favRemoveMsg.SessionIDs) != 1 || favRemoveMsg.SessionIDs[0] != "sess-a" {
		t.Errorf("remove SessionIDs = %v, want [sess-a]", favRemoveMsg.SessionIDs)
	}
}

func TestFavoritesModel_ConfirmMode_EnterMultiSelect_ExecutesSequence(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a", "sess-c"}

	updated, cmd := m.Update(favEnterKey())
	if cmd == nil {
		t.Fatal("Enter in confirm mode should return a command")
	}
	if updated.confirmingDelete {
		t.Error("confirmingDelete should be false after Enter")
	}

	msg := cmd()
	seq, ok := extractSequenceCmds(msg)
	if !ok {
		t.Fatalf("Enter should return tea.Sequence, got %T", msg)
	}
	if len(seq) != 2 {
		t.Fatalf("sequence length = %d, want 2", len(seq))
	}

	deleteMsg := seq[0]()
	actionMsg, ok := deleteMsg.(ManageActionMsg)
	if !ok {
		t.Fatalf("seq[0] returned %T, want ManageActionMsg", deleteMsg)
	}
	if actionMsg.Action != "delete" {
		t.Errorf("action = %q, want delete", actionMsg.Action)
	}
	gotSet := make(map[string]bool)
	for _, id := range actionMsg.SessionIDs {
		gotSet[id] = true
	}
	if !gotSet["sess-a"] || !gotSet["sess-c"] {
		t.Errorf("delete SessionIDs = %v, want [sess-a, sess-c]", actionMsg.SessionIDs)
	}
	if len(actionMsg.SessionIDs) != 2 {
		t.Errorf("delete SessionIDs = %v, want 2 entries", actionMsg.SessionIDs)
	}

	removeMsg := seq[1]()
	favRemoveMsg, ok := removeMsg.(FavoritesRemoveRequestMsg)
	if !ok {
		t.Fatalf("seq[1] returned %T, want FavoritesRemoveRequestMsg", removeMsg)
	}
	removeSet := make(map[string]bool)
	for _, id := range favRemoveMsg.SessionIDs {
		removeSet[id] = true
	}
	if !removeSet["sess-a"] || !removeSet["sess-c"] {
		t.Errorf("remove SessionIDs = %v, want [sess-a, sess-c]", favRemoveMsg.SessionIDs)
	}
}

// ============================================================================
// Confirm mode: Esc/q cancels and restores browse (NO messages)
// ============================================================================

func TestFavoritesModel_ConfirmMode_EscCancels(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a"}

	updated, cmd := m.Update(favEscKey())
	if cmd != nil {
		t.Errorf("Esc in confirm mode should return nil cmd, got %T", cmd)
	}
	if updated.confirmingDelete {
		t.Error("confirmingDelete should be false after Esc")
	}
	if len(updated.pendingDeleteIDs) != 0 {
		t.Errorf("pendingDeleteIDs = %v, want empty after Esc", updated.pendingDeleteIDs)
	}
	if updated.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (preserved after cancel)", updated.cursor)
	}
}

func TestFavoritesModel_ConfirmMode_QKeyCancels(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 1
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-b"}

	updated, cmd := m.Update(favQKey())
	if cmd != nil {
		t.Errorf("q in confirm mode should return nil cmd, got %T", cmd)
	}
	if updated.confirmingDelete {
		t.Error("confirmingDelete should be false after q")
	}
	if len(updated.pendingDeleteIDs) != 0 {
		t.Errorf("pendingDeleteIDs = %v, want empty after q", updated.pendingDeleteIDs)
	}
	if updated.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (preserved after cancel)", updated.cursor)
	}
}

// ============================================================================
// Confirm mode: other keys ignored
// ============================================================================

func TestFavoritesModel_ConfirmMode_OtherKeysIgnored(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a"}

	otherKeys := []tea.KeyMsg{
		favUpKey(), favDownKey(), favFKey(), favDKey(), favSpaceKey(),
		favOtherKey(),
	}
	for _, key := range otherKeys {
		updated, cmd := m.Update(key)
		if cmd != nil {
			t.Errorf("%v: should return nil cmd in confirm mode, got %T", key, cmd)
		}
		if !updated.confirmingDelete {
			t.Errorf("%v: confirmingDelete should remain true", key)
		}
		if len(updated.pendingDeleteIDs) != 1 {
			t.Errorf("%v: pendingDeleteIDs should be unchanged", key)
		}
		if updated.cursor != 0 {
			t.Errorf("%v: cursor should remain 0, got %d", key, updated.cursor)
		}
	}
}

// ============================================================================
// Confirm mode → View renders confirmView
// ============================================================================

func TestFavoritesModel_View_ConfirmMode_RendersConfirmView(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a"}

	out := m.View()
	if strings.Contains(out, "⭐ Favorites") {
		t.Errorf("confirm mode View() should NOT show favorites header\nGot:\n%s", out)
	}
	if !strings.Contains(out, "Delete this favorite session?") {
		t.Errorf("confirm mode View() should contain delete confirmation\nGot:\n%s", out)
	}
	if !strings.Contains(out, "Session A") {
		t.Errorf("confirm mode View() should contain session title\nGot:\n%s", out)
	}
	if !strings.Contains(out, "[Enter] Confirm") {
		t.Errorf("confirm mode View() should contain confirm footer\nGot:\n%s", out)
	}
}

func TestFavoritesModel_View_ConfirmMode_Multi_RendersCount(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a", "sess-b", "sess-c"}

	out := m.View()
	if !strings.Contains(out, "Delete 3 favorite sessions?") {
		t.Errorf("multi confirm should show count, got:\n%s", out)
	}
}

// ============================================================================
// confirmView() standalone
// ============================================================================

func TestFavoritesModel_ConfirmView_Single_ShowsTitleAndID(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a"}

	out := m.confirmView()
	if !strings.Contains(out, "Delete this favorite session?") {
		t.Errorf("single confirm should say 'this favorite session'\nGot:\n%s", out)
	}
	if !strings.Contains(out, "Session A") {
		t.Errorf("single confirm should show title, got:\n%s", out)
	}
	if !strings.Contains(out, "ID:") {
		t.Errorf("single confirm should show ID label\nGot:\n%s", out)
	}
}

func TestFavoritesModel_ConfirmView_Single_UnknownTitle_FallsBackToID(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-unknown": true}, 100, 24)
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-unknown"}

	out := m.confirmView()
	if !strings.Contains(out, "sess-unknown") {
		t.Errorf("unknown title should fall back to sessionID, got:\n%s", out)
	}
}

func TestFavoritesModel_ConfirmView_Single_LongTitle_Truncated(t *testing.T) {
	m := loadedFavModel(map[string]bool{}, 100, 24)
	longTitle := strings.Repeat("A", 80)
	m.view = daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{{
			ProjectID: "proj-1",
			Sessions:  []daemon.ViewSession{{SessionID: "sess-long", Title: longTitle, RowStatus: daemon.StatusIdle}},
		}},
	}
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-long"}

	out := m.confirmView()
	if strings.Contains(out, strings.Repeat("A", 80)) {
		t.Errorf("long title should be truncated, got full title in:\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("truncated long title should end with '…'\nGot:\n%s", out)
	}
}

func TestFavoritesModel_ConfirmView_Multi_ShowsCountAndIDs(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a", "sess-b", "sess-c"}

	out := m.confirmView()
	if !strings.Contains(out, "Delete 3 favorite sessions?") {
		t.Errorf("multi confirm should show count 3\nGot:\n%s", out)
	}
	for _, id := range m.pendingDeleteIDs {
		if !strings.Contains(out, abbreviateID(id)) {
			t.Errorf("multi confirm should contain abbreviated ID %q\nGot:\n%s", abbreviateID(id), out)
		}
	}
}

func TestFavoritesModel_ConfirmView_FooterPresent(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a"}

	out := m.confirmView()
	wantFooter := "[Enter] Confirm   [Esc] Cancel"
	if !strings.Contains(out, wantFooter) {
		t.Errorf("confirmView() should contain footer %q\nGot:\n%s", wantFooter, out)
	}
}

func TestFavoritesModel_ConfirmView_EmptyPendingIDs_ReturnsEmpty(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	m.confirmingDelete = true
	m.pendingDeleteIDs = nil

	out := m.confirmView()
	if out != "" {
		t.Errorf("confirmView with nil pendingDeleteIDs should return empty, got %q", out)
	}

	m.pendingDeleteIDs = []string{}
	out2 := m.confirmView()
	if out2 != "" {
		t.Errorf("confirmView with zero-length pendingDeleteIDs should return empty, got %q", out2)
	}
}

func TestFavoritesModel_ConfirmView_NarrowWidth_NoCrash(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 20, 24)
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a"}

	out := m.confirmView()
	if out == "" {
		t.Error("confirmView should return non-empty even with narrow width")
	}
}

// ============================================================================
// Edge: confirm mode + not loaded
// ============================================================================

func TestFavoritesModel_ConfirmMode_NotLoaded_KeyIgnored(t *testing.T) {
	m := NewFavoritesModel()
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a"}

	_, cmd := m.Update(favEnterKey())
	if cmd != nil {
		t.Errorf("Enter when not loaded should return nil cmd, got %T", cmd)
	}
}

// ============================================================================
// Edge: state preserved during confirm (selected)
// ============================================================================

func TestFavoritesModel_ConfirmMode_SelectedPreservedAfterCancel(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.cursor = 1
	m.selected = map[string]bool{"sess-a": true, "sess-c": true}
	m.confirmingDelete = true
	m.pendingDeleteIDs = []string{"sess-a", "sess-c"}

	updated, _ := m.Update(favEscKey())
	if !updated.selected["sess-a"] {
		t.Error("sess-a should remain selected after cancel")
	}
	if !updated.selected["sess-c"] {
		t.Error("sess-c should remain selected after cancel")
	}
	if len(updated.selected) != 2 {
		t.Errorf("selected should have 2 entries after cancel, got %d", len(updated.selected))
	}
}

// ============================================================================
// Status-color fix: renderRow applies status foreground to ALL paths
//
// In non-TTY envs lipgloss strips ANSI codes, so we verify the fix by
// checking that renderRow output for cursor/multi-select paths correctly
// includes both the status icon and session title — the styled variable is
// applied BEFORE cursor/multi-select background wrapping, which is the
// behavioral change from the previous code. We also verify that different
// statuses produce visually distinct output (different icon text).
// ============================================================================

func TestFavoritesModel_RenderRow_CursorRow_IncludesStatusIconAndTitle(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-a", Title: "Session A", RowStatus: daemon.StatusBusy}
	line := m.renderRow("sess-a", s, true, false)
	if !strings.Contains(line, "🔵") {
		t.Errorf("cursor row should contain BUSY glyph 🔵, got: %q", line)
	}
	if !strings.Contains(line, "Session A") {
		t.Errorf("cursor row should contain title, got: %q", line)
	}
	if !strings.Contains(line, "▶") {
		t.Errorf("cursor row should contain cursor marker ▶, got: %q", line)
	}
}

func TestFavoritesModel_RenderRow_MultiSelectedRow_IncludesStatusIconAndTitle(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-a", Title: "Session A", RowStatus: daemon.StatusError}
	line := m.renderRow("sess-a", s, false, true)
	if !strings.Contains(line, "🔴") {
		t.Errorf("multi-selected row should contain ERROR glyph 🔴, got: %q", line)
	}
	if !strings.Contains(line, "Session A") {
		t.Errorf("multi-selected row should contain title, got: %q", line)
	}
	if !strings.Contains(line, "✓") {
		t.Errorf("multi-selected row should contain checkmark ✓, got: %q", line)
	}
}

func TestFavoritesModel_RenderRow_CursorAndSelected_IncludesBothMarkers(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-a", Title: "Session A", RowStatus: daemon.StatusIdle}
	line := m.renderRow("sess-a", s, true, true)
	if !strings.Contains(line, "▶") {
		t.Errorf("cursor+selected row should contain cursor marker ▶, got: %q", line)
	}
	if !strings.Contains(line, "✓") {
		t.Errorf("cursor+selected row should contain checkmark ✓, got: %q", line)
	}
	if !strings.Contains(line, "🟢") {
		t.Errorf("cursor+selected row should contain IDLE glyph 🟢, got: %q", line)
	}
}

func TestFavoritesModel_RenderRow_DifferentStatuses_DifferentIcon(t *testing.T) {
	m := loadedFavModel(map[string]bool{}, 100, 24)
	sIdle := daemon.ViewSession{SessionID: "sess-x", Title: "Test", RowStatus: daemon.StatusIdle}
	sError := daemon.ViewSession{SessionID: "sess-x", Title: "Test", RowStatus: daemon.StatusError}

	lineIdle := m.renderRow("sess-x", sIdle, true, false)
	lineError := m.renderRow("sess-x", sError, true, false)

	// Different statuses produce different icon text — verifying the status
	// foreground color is applied to the correct status-specific icon.
	if lineIdle == lineError {
		t.Error("cursor rows with different statuses should produce different output")
	}
	if !strings.Contains(lineIdle, "🟢") {
		t.Errorf("Idle row should contain IDLE glyph 🟢\nGot: %q", lineIdle)
	}
	if !strings.Contains(lineError, "🔴") {
		t.Errorf("Error row should contain ERROR glyph 🔴\nGot: %q", lineError)
	}
}

func TestFavoritesModel_RenderRow_AllStatusesNonEmpty(t *testing.T) {
	m := loadedFavModel(map[string]bool{}, 100, 24)
	statuses := []daemon.SessionStatus{
		daemon.StatusIdle, daemon.StatusBusy, daemon.StatusPermission,
		daemon.StatusRetry, daemon.StatusError, daemon.StatusArchived,
		daemon.StatusUnknown,
	}
	for _, st := range statuses {
		s := daemon.ViewSession{SessionID: "s", Title: "T", RowStatus: st}
		for _, isCursor := range []bool{false, true} {
			for _, isSelected := range []bool{false, true} {
				line := m.renderRow("s", s, isCursor, isSelected)
				if line == "" {
					t.Errorf("renderRow returned empty for status=%q cursor=%v selected=%v", st, isCursor, isSelected)
				}
			}
		}
	}
}

// ============================================================================
// Cursor bounds
// ============================================================================

func TestFavoritesModel_Cursor_UpDecrements(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 1
	updated, _ := m.Update(favUpKey())
	if updated.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after up from 1", updated.cursor)
	}
}

func TestFavoritesModel_Cursor_UpAtZero_Clamps(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	updated, _ := m.Update(favUpKey())
	if updated.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (clamped at top)", updated.cursor)
	}
}

func TestFavoritesModel_Cursor_DownIncrements(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	updated, _ := m.Update(favDownKey())
	if updated.cursor != 1 {
		t.Errorf("cursor = %d, want 1 after down from 0", updated.cursor)
	}
}

func TestFavoritesModel_Cursor_DownAtBottom_Clamps(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 1
	updated, _ := m.Update(favDownKey())
	if updated.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (clamped at bottom)", updated.cursor)
	}
}

func TestFavoritesModel_Cursor_EmptyList_RemainsZero(t *testing.T) {
	m := loadedFavModel(nil, 100, 24)
	updated, _ := m.Update(favUpKey())
	if updated.cursor != 0 {
		t.Errorf("cursor = %d, want 0 on empty list", updated.cursor)
	}
}

func TestFavoritesModel_Cursor_ClampOnRemoval(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.cursor = 2
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{"sess-a": true, "sess-b": true}})
	if updated.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (clamped after removal)", updated.cursor)
	}
}

func TestFavoritesModel_Cursor_ClampedToZeroWhenAllRemoved(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{}})
	if updated.cursor != 0 {
		t.Errorf("cursor = %d, want 0 on empty list", updated.cursor)
	}
	if len(updated.favoriteIDs) != 0 {
		t.Errorf("favoriteIDs = %v, want empty", updated.favoriteIDs)
	}
}

// ============================================================================
// Multi-select — Space toggles
// ============================================================================

func TestFavoritesModel_Space_TogglesSelection(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	updated, _ := m.Update(favSpaceKey())
	if !updated.selected["sess-a"] {
		t.Errorf("sess-a should be selected after first Space")
	}
	if updated.selected["sess-b"] {
		t.Errorf("sess-b should NOT be selected (cursor is on sess-a)")
	}
	updated2, _ := updated.Update(favSpaceKey())
	if updated2.selected["sess-a"] {
		t.Errorf("sess-a should be deselected after second Space")
	}
}

func TestFavoritesModel_Space_MultipleItems(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.cursor = 0
	updated, _ := m.Update(favSpaceKey())
	if !updated.selected["sess-a"] {
		t.Error("sess-a should be selected")
	}
	updated.cursor = 1
	updated2, _ := updated.Update(favSpaceKey())
	if !updated2.selected["sess-a"] {
		t.Error("sess-a should still be selected")
	}
	if !updated2.selected["sess-b"] {
		t.Error("sess-b should now be selected")
	}
	updated2.cursor = 2
	updated3, _ := updated2.Update(favSpaceKey())
	if len(updated3.selected) != 3 {
		t.Errorf("selected = %v, want 3 entries", updated3.selected)
	}
}

func TestFavoritesModel_Space_EmptyList_NoOp(t *testing.T) {
	m := loadedFavModel(nil, 100, 24)
	updated, _ := m.Update(favSpaceKey())
	if len(updated.selected) != 0 {
		t.Errorf("selected = %v, want empty for empty list", updated.selected)
	}
}

// ============================================================================
// View — checkmark in selected row
// ============================================================================

func TestFavoritesModel_View_SelectedRowHasCheckmark(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	m.selected = map[string]bool{"sess-a": true}
	out := m.View()
	if !strings.Contains(out, "✓") {
		t.Errorf("View() should contain ✓ for selected row\nGot:\n%s", out)
	}
}

func TestFavoritesModel_View_UnselectedNoCheckmark(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	m.selected = nil
	out := m.View()
	if strings.Contains(out, "✓") {
		t.Errorf("View() should NOT contain ✓ when nothing selected\nGot:\n%s", out)
	}
}

func TestFavoritesModel_Selection_SurvivesFavoritesChangedMsg(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}, 100, 24)
	m.selected = map[string]bool{"sess-a": true, "sess-c": true}
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}})
	if !updated.selected["sess-a"] {
		t.Error("sess-a should remain selected after rebuild")
	}
	if !updated.selected["sess-c"] {
		t.Error("sess-c should remain selected after rebuild")
	}
	if updated.selected["sess-b"] {
		t.Error("sess-b should NOT be selected (wasn't before)")
	}
}

// ============================================================================
// View — empty favorites hint
// ============================================================================

func TestFavoritesModel_View_EmptyFavorites_ShowsHint(t *testing.T) {
	m := loadedFavModel(nil, 100, 24)
	out := m.View()
	if !strings.Contains(out, "No favorites yet") {
		t.Errorf("View() should show empty hint, got:\n%s", out)
	}
}

func TestFavoritesModel_View_LoadedWithExplicitEmptyMap_ShowsHint(t *testing.T) {
	m := NewFavoritesModel()
	m.loaded = true
	m.favorites = map[string]bool{}
	m.rebuildFavoriteIDs()
	out := m.View()
	want := "No favorites yet — press f in Manage to favorite a session"
	if out != want {
		t.Errorf("View() = %q, want %q", out, want)
	}
}

// ============================================================================
// View — header and rows
// ============================================================================

func TestFavoritesModel_View_ShowsHeader(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	out := m.View()
	if !strings.Contains(out, "⭐ Favorites") {
		t.Errorf("View() should contain ⭐ Favorites header\nGot:\n%s", out)
	}
}

func TestFavoritesModel_View_ShowsSessionTitle(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	out := m.View()
	if !strings.Contains(out, "Session A") {
		t.Errorf("View() should contain session title 'Session A'\nGot:\n%s", out)
	}
}

func TestFavoritesModel_View_ShowsStatusIcon(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	out := m.View()
	if !strings.Contains(out, "🟢") {
		t.Errorf("View() should contain IDLE glyph 🟢\nGot:\n%s", out)
	}
}

func TestFavoritesModel_View_CursorMarker(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	out := m.View()
	if !strings.Contains(out, "▶") {
		t.Errorf("View() should contain cursor marker ▶ for cursor row\nGot:\n%s", out)
	}
}

func TestFavoritesModel_View_CursorAndMultiSelect(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 24)
	m.cursor = 0
	m.selected = map[string]bool{"sess-a": true}
	out := m.View()
	if !strings.Contains(out, "▶") {
		t.Errorf("View() should contain ▶ for cursor row\nGot:\n%s", out)
	}
	if !strings.Contains(out, "✓") {
		t.Errorf("View() should contain ✓ for selected row\nGot:\n%s", out)
	}
}

// ============================================================================
// renderRow tests
// ============================================================================

func TestFavoritesModel_RenderRow_CursorOnly(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-a", Title: "Session A", RowStatus: daemon.StatusIdle}
	line := m.renderRow("sess-a", s, true, false)
	if !strings.Contains(line, favCursorMarker) {
		t.Errorf("cursor-only row should contain cursor marker %q, got %q", favCursorMarker, line)
	}
}

func TestFavoritesModel_RenderRow_SelectedOnly(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-a", Title: "Session A", RowStatus: daemon.StatusIdle}
	line := m.renderRow("sess-a", s, false, true)
	if !strings.Contains(line, "✓ ") {
		t.Errorf("selected-only row should contain '✓ ', got %q", line)
	}
}

func TestFavoritesModel_RenderRow_CursorAndSelected(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-a", Title: "Session A", RowStatus: daemon.StatusIdle}
	line := m.renderRow("sess-a", s, true, true)
	if !strings.Contains(line, favCursorMarker) {
		t.Errorf("cursor+selected row should contain cursor marker %q, got %q", favCursorMarker, line)
	}
	if !strings.Contains(line, "✓ ") {
		t.Errorf("cursor+selected row should contain '✓ ', got %q", line)
	}
}

func TestFavoritesModel_RenderRow_EmptyTitle_UsesSessionID(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-a", Title: "", RowStatus: daemon.StatusIdle}
	line := m.renderRow("sess-a", s, false, false)
	if !strings.Contains(line, "sess-a") {
		t.Errorf("renderRow with empty title should fall back to sessionID, got %q", line)
	}
}

func TestFavoritesModel_RenderRow_UnknownStatusColor(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-a", Title: "Session A", RowStatus: "MADE_UP_STATUS"}
	line := m.renderRow("sess-a", s, false, false)
	if line == "" {
		t.Error("renderRow should return non-empty for unknown status")
	}
}

// ============================================================================
// Scroll tests
// ============================================================================

func TestFavoritesModel_Scroll_CursorVisibleInWindow(t *testing.T) {
	m := buildScrollFavModel(15, 100, 10)
	out := m.View()
	for i := 0; i <= 6; i++ {
		want := fmt.Sprintf("Item-%02d", i)
		if !strings.Contains(out, want) {
			t.Errorf("cursor at 0: View() should contain %q", want)
		}
	}
	for i := 7; i < 15; i++ {
		notWant := fmt.Sprintf("Item-%02d", i)
		if strings.Contains(out, notWant) {
			t.Errorf("cursor at 0: View() should NOT contain %q", notWant)
		}
	}
}

func TestFavoritesModel_Scroll_CursorAtEnd_SeeLastItems(t *testing.T) {
	m := buildScrollFavModel(15, 100, 10)
	m.cursor = 14
	out := m.View()
	for i := 8; i <= 14; i++ {
		want := fmt.Sprintf("Item-%02d", i)
		if !strings.Contains(out, want) {
			t.Errorf("cursor at 14: View() should contain %q", want)
		}
	}
	for i := 0; i <= 7; i++ {
		notWant := fmt.Sprintf("Item-%02d", i)
		if strings.Contains(out, notWant) {
			t.Errorf("cursor at 14: View() should NOT contain %q", notWant)
		}
	}
}

func TestFavoritesModel_Scroll_DownKeyScrollsWindow(t *testing.T) {
	m := buildScrollFavModel(15, 100, 10)
	m.cursor = 6
	updated, _ := m.Update(favDownKey())
	if updated.cursor != 7 {
		t.Errorf("cursor = %d, want 7 after down from 6", updated.cursor)
	}
	out := updated.View()
	if strings.Contains(out, "Item-00") {
		t.Errorf("after scroll down: View() should NOT contain Item-00 (scrolled out)")
	}
	if !strings.Contains(out, "Item-07") {
		t.Errorf("after scroll down: View() should contain Item-07")
	}
}

// ============================================================================
// WindowSizeMsg
// ============================================================================

func TestFavoritesModel_WindowSizeMsg_UpdatesDimensions(t *testing.T) {
	m := NewFavoritesModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if updated.width != 80 {
		t.Errorf("width = %d, want 80", updated.width)
	}
	if updated.height != 30 {
		t.Errorf("height = %d, want 30", updated.height)
	}
}

// ============================================================================
// Status color map
// ============================================================================

func TestFavoritesModel_StatusColorMap_CoversAllNamedStatuses(t *testing.T) {
	expectedStatuses := []daemon.SessionStatus{
		daemon.StatusIdle, daemon.StatusBusy, daemon.StatusPermission,
		daemon.StatusRetry, daemon.StatusError, daemon.StatusArchived,
		daemon.StatusUnknown,
	}
	for _, status := range expectedStatuses {
		color, ok := favStatusColorMap[status]
		if !ok {
			t.Errorf("favStatusColorMap missing entry for status %q", status)
		}
		if color == "" {
			t.Errorf("favStatusColorMap[%q] has empty color", status)
		}
	}
}

// ============================================================================
// Style constants
// ============================================================================

func TestFavoritesModel_StyleConstants_NotEmpty(t *testing.T) {
	styles := []struct {
		name  string
		style lipgloss.Style
	}{
		{"favEmptyStyle", favEmptyStyle},
		{"favHeaderStyle", favHeaderStyle},
		{"favRowStyle", favRowStyle},
		{"favCursorStyle", favCursorStyle},
		{"favMultiSelectedStyle", favMultiSelectedStyle},
	}
	for _, s := range styles {
		rendered := s.style.Render("test")
		if rendered == "" {
			t.Errorf("%s.Render() returned empty", s.name)
		}
	}
}

// ============================================================================
// Boundary: zero / negative / unicode
// ============================================================================

func TestFavoritesModel_View_ZeroHeight_DoesNotCrash(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true, "sess-b": true}, 100, 0)
	out := m.View()
	if out == "" {
		t.Error("View() should return non-empty string even with zero height")
	}
}

func TestFavoritesModel_View_NegativeHeight_DoesNotCrash(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 100, -5)
	out := m.View()
	if out == "" {
		t.Error("View() should return non-empty string even with negative height")
	}
}

func TestFavoritesModel_View_ZeroWidth_DoesNotCrash(t *testing.T) {
	m := loadedFavModel(map[string]bool{"sess-a": true}, 0, 24)
	out := m.View()
	if out == "" {
		t.Error("View() should return non-empty even with zero width")
	}
}

func TestFavoritesModel_RenderRow_UnicodeTitle(t *testing.T) {
	m := loadedFavModel(map[string]bool{}, 100, 24)
	s := daemon.ViewSession{SessionID: "sess-cn", Title: "你好世界 — Testing Unicode 🔥", RowStatus: daemon.StatusIdle}
	line := m.renderRow("sess-cn", s, false, false)
	if !strings.Contains(line, "你好世界") {
		t.Errorf("renderRow with Unicode title should contain Unicode chars\nGot:\n%s", line)
	}
}

func TestFavoritesModel_RenderRow_LongTitle(t *testing.T) {
	m := loadedFavModel(map[string]bool{}, 200, 24)
	longTitle := strings.Repeat("A", 500)
	s := daemon.ViewSession{SessionID: "sess-long", Title: longTitle, RowStatus: daemon.StatusIdle}
	line := m.renderRow("sess-long", s, false, false)
	if len(line) == 0 {
		t.Error("renderRow should handle long titles without crashing")
	}
}

// ============================================================================
// Each session status icon
// ============================================================================

func TestFavoritesModel_View_RendersEachStatusIcon(t *testing.T) {
	statuses := []struct {
		status daemon.SessionStatus
		icon   string
	}{
		{daemon.StatusIdle, "🟢"},
		{daemon.StatusBusy, "🔵"},
		{daemon.StatusPermission, "🟡"},
		{daemon.StatusRetry, "🟠"},
		{daemon.StatusError, "🔴"},
		{daemon.StatusUnknown, "❔"},
		{daemon.StatusArchived, "📦"},
	}
	for _, tc := range statuses {
		t.Run(string(tc.status), func(t *testing.T) {
			view := daemon.ViewMsg{
				Type: "view",
				Projects: []daemon.ViewProject{
					{ProjectID: "proj-1", Sessions: []daemon.ViewSession{
						{SessionID: "sess-x", Title: "Test", Status: tc.status, RowStatus: tc.status, Directory: "/tmp"},
					}},
				},
			}
			m := NewFavoritesModel()
			m.width = 100
			m.height = 24
			m.loaded = true
			m.view = view
			m.favorites = map[string]bool{"sess-x": true}
			m.rebuildFavoriteIDs()
			out := m.View()
			if !strings.Contains(out, tc.icon) {
				t.Errorf("View() for status %q should contain icon %q\nGot:\n%s", tc.status, tc.icon, out)
			}
		})
	}
}
