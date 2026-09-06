package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tomasWade/octl/internal/daemon"
	"github.com/tomasWade/octl/internal/tui/views"
)

// ============================================================================
// Task 2.4: Frontend-to-daemon favorites integration tests
// ============================================================================

// daemonViewMsgWithFavorites returns a ViewMsg with a non-empty Favorites field.
func daemonViewMsgWithFavorites() daemon.ViewMsg {
	return daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/proj",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "Session A", Status: "IDLE", Directory: "/home/user/proj", IsFavorite: true},
					{SessionID: "sess-b", Title: "Session B", Status: "BUSY", Directory: "/home/user/proj"},
				},
			},
		},
		Favorites: []daemon.ViewSession{
			{SessionID: "fav-1", Title: "Favorite 1", Status: "IDLE", IsFavorite: true},
			{SessionID: "fav-2", Title: "Favorite 2", Status: "BUSY", IsFavorite: true},
		},
		Stats: daemon.ViewStats{TotalSessions: 2},
	}
}

// ---------------------------------------------------------------------------
// daemonViewMsg → favoritesMap rebuilt from daemon Favorites (authoritative)
// ---------------------------------------------------------------------------

// TestApp_DaemonViewMsg_RebuildsFavoritesMap_DropsStaleLocalOnly verifies that
// daemonViewMsg rebuilds favoritesMap exclusively from ViewMsg.Favorites,
// dropping any stale local-only entries.
func TestApp_DaemonViewMsg_RebuildsFavoritesMap_DropsStaleLocalOnly(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{
		"stale-1": true,
		"fav-1":   true,
		"stale-2": true,
	}

	dvm := daemonViewMsgWithFavorites()
	updated, _ := m.Update(daemonViewMsg{view: dvm})
	model := updated.(Model)

	if len(model.favoritesMap) != 2 {
		t.Errorf("favoritesMap length = %d, want 2 (daemon authoritative)", len(model.favoritesMap))
	}
	if !model.favoritesMap["fav-1"] {
		t.Error("favoritesMap should contain fav-1 from ViewMsg.Favorites")
	}
	if !model.favoritesMap["fav-2"] {
		t.Error("favoritesMap should contain fav-2 from ViewMsg.Favorites")
	}
	if model.favoritesMap["stale-1"] {
		t.Error("favoritesMap should NOT contain stale-1 (not in daemon Favorites)")
	}
	if model.favoritesMap["stale-2"] {
		t.Error("favoritesMap should NOT contain stale-2 (not in daemon Favorites)")
	}
}

// TestApp_DaemonViewMsg_EmptyFavorites_ClearsMap verifies that
// a ViewMsg with empty/nil Favorites clears the favoritesMap.
func TestApp_DaemonViewMsg_EmptyFavorites_ClearsMap(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{"local-fav": true}

	dvm := daemonViewMsgFixture()
	updated, _ := m.Update(daemonViewMsg{view: dvm})
	model := updated.(Model)

	if len(model.favoritesMap) != 0 {
		t.Errorf("favoritesMap = %v, want empty (no Favorites in ViewMsg)", model.favoritesMap)
	}
}

// TestApp_DaemonViewMsg_NilFavorites_NoCrash verifies that
// a ViewMsg with nil Favorites (old daemon) does not crash.
func TestApp_DaemonViewMsg_NilFavorites_NoCrash(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{"old-fav": true}

	dvm := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/proj",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "A", Status: "IDLE", Directory: "/home/user/proj"},
				},
			},
		},
		Stats: daemon.ViewStats{TotalSessions: 1},
	}

	updated, _ := m.Update(daemonViewMsg{view: dvm})
	model := updated.(Model)

	if len(model.favoritesMap) != 0 {
		t.Errorf("favoritesMap = %v, want empty for nil Favorites", model.favoritesMap)
	}
}

// TestApp_DaemonViewMsg_OnlyDaemonFavorites_Survive verifies that
// daemonViewMsg with Favorites populates an empty favoritesMap correctly.
func TestApp_DaemonViewMsg_OnlyDaemonFavorites_Survive(t *testing.T) {
	m := newTestModel()

	dvm := daemonViewMsgWithFavorites()
	updated, _ := m.Update(daemonViewMsg{view: dvm})
	model := updated.(Model)

	if len(model.favoritesMap) != 2 {
		t.Fatalf("favoritesMap length = %d, want 2", len(model.favoritesMap))
	}
	if !model.favoritesMap["fav-1"] {
		t.Error("favoritesMap missing fav-1")
	}
	if !model.favoritesMap["fav-2"] {
		t.Error("favoritesMap missing fav-2")
	}
}

// ---------------------------------------------------------------------------
// FavoritesToggleRequestMsg: toggle semantics (favorite ↔ unfavorite split)
// ---------------------------------------------------------------------------

// TestApp_FavoritesToggleRequestMsg_MixedFavoritesAndUnfavorites verifies
// toggle behavior: already-favorited IDs become unfavorited, new IDs favorited.
func TestApp_FavoritesToggleRequestMsg_MixedFavoritesAndUnfavorites(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{"already-fav": true}

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"already-fav", "new-fav"},
	})
	model := updated.(Model)

	if model.favoritesMap["already-fav"] {
		t.Error("already-fav should be unfavorited (was true, toggle removes)")
	}
	if !model.favoritesMap["new-fav"] {
		t.Error("new-fav should be favorited (was false, toggle adds)")
	}
	if len(model.favoritesMap) != 1 {
		t.Errorf("favoritesMap = %v, want exactly 1 entry (new-fav)", model.favoritesMap)
	}
}

// TestApp_FavoritesToggleRequestMsg_ToggleRoundTrip verifies round-trip toggle.
func TestApp_FavoritesToggleRequestMsg_ToggleRoundTrip(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"sess-a"},
	})
	model := updated.(Model)
	if !model.favoritesMap["sess-a"] {
		t.Fatal("sess-a should be favorited after first toggle")
	}

	updated2, _ := updated.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"sess-a"},
	})
	model2 := updated2.(Model)
	if model2.favoritesMap["sess-a"] {
		t.Error("sess-a should be unfavorited after second toggle (round-trip)")
	}
}

// TestApp_FavoritesToggleRequestMsg_AllNew_OptimisticAdd verifies
// toggling all-new IDs adds them all.
func TestApp_FavoritesToggleRequestMsg_AllNew_OptimisticAdd(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"a", "b", "c"},
	})
	model := updated.(Model)

	if len(model.favoritesMap) != 3 {
		t.Errorf("favoritesMap = %v, want 3 entries", model.favoritesMap)
	}
}

// TestApp_FavoritesToggleRequestMsg_AllExisting_OptimisticRemove verifies
// toggling all already-favorited IDs removes them.
func TestApp_FavoritesToggleRequestMsg_AllExisting_OptimisticRemove(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{"x": true, "y": true, "z": true}

	updated, _ := m.Update(views.FavoritesToggleRequestMsg{
		SessionIDs: []string{"x", "y", "z"},
	})
	model := updated.(Model)

	if len(model.favoritesMap) != 0 {
		t.Errorf("favoritesMap = %v, want empty (all removed)", model.favoritesMap)
	}
}

// TestApp_ResultMsg_Favorite_DoesNotHijackDashboardIntoProgress verifies that
// a favorite/unfavorite result from the daemon is NOT routed to the dashboard's
// ManageResultMsg handler. Otherwise the dashboard would switch into
// modeProgress and show the "Executing favorite... / Operation complete"
// dialog — the "收藏后卡在 execute 弹窗" bug.
func TestApp_ResultMsg_Favorite_DoesNotHijackDashboardIntoProgress(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	updated, _ = m.Update(daemonViewMsg{view: daemonViewMsgWithFavorites()})
	m = updated.(Model)

	// 用户按 f 触发收藏 toggle（app 发 favorite action 并乐观更新）。
	updated, _ = m.Update(views.FavoritesToggleRequestMsg{SessionIDs: []string{"sess-a"}})
	m = updated.(Model)

	// daemon 执行 favorite 后回 result 消息。
	updated, _ = m.Update(daemonResultMsg{result: daemon.ResultMsg{
		Type:   "result",
		Action: "favorite",
		Summary: daemon.Summary{
			Total:     1,
			Succeeded: 1,
			Results:   []daemon.Result{{SessionID: "sess-a", Action: "favorite", Success: true}},
		},
	}})
	m = updated.(Model)

	out := m.dashboard.View()
	if strings.Contains(out, "Operation complete") || strings.Contains(out, "Executing") {
		t.Errorf("favorite result must NOT hijack dashboard into the progress dialog\nGot:\n%s", out)
	}
	// 浏览模式仍应渲染项目树。
	if !strings.Contains(out, "📁") {
		t.Errorf("dashboard should still render the project tree after favorite\nGot:\n%s", out)
	}
}

// TestApp_ResultMsg_Unfavorite_DoesNotHijackDashboardIntoProgress verifies the
// same behavior for the unfavorite action.
func TestApp_ResultMsg_Unfavorite_DoesNotHijackDashboardIntoProgress(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	updated, _ = m.Update(daemonViewMsg{view: daemonViewMsgWithFavorites()})
	m = updated.(Model)

	updated, _ = m.Update(views.FavoritesRemoveRequestMsg{SessionIDs: []string{"fav-1"}})
	m = updated.(Model)

	updated, _ = m.Update(daemonResultMsg{result: daemon.ResultMsg{
		Type:   "result",
		Action: "unfavorite",
		Summary: daemon.Summary{
			Total:     1,
			Succeeded: 1,
			Results:   []daemon.Result{{SessionID: "fav-1", Action: "unfavorite", Success: true}},
		},
	}})
	m = updated.(Model)

	out := m.dashboard.View()
	if strings.Contains(out, "Operation complete") || strings.Contains(out, "Executing") {
		t.Errorf("unfavorite result must NOT hijack dashboard into the progress dialog\nGot:\n%s", out)
	}
}

// TestApp_ResultMsg_Delete_StillShowsProgressDialog is the control test: a
// real management action (delete) result must still switch the dashboard into
// the progress dialog.
func TestApp_ResultMsg_Delete_StillShowsProgressDialog(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	updated, _ = m.Update(daemonViewMsg{view: daemonViewMsgWithFavorites()})
	m = updated.(Model)

	updated, _ = m.Update(daemonResultMsg{result: daemon.ResultMsg{
		Type:   "result",
		Action: "delete",
		Summary: daemon.Summary{
			Total:     1,
			Succeeded: 1,
			Results:   []daemon.Result{{SessionID: "sess-a", Action: "delete", Success: true}},
		},
	}})
	m = updated.(Model)

	out := m.dashboard.View()
	if !strings.Contains(out, "Operation complete") {
		t.Errorf("delete result should still show the progress dialog\nGot:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// FavoritesRemoveRequestMsg: remove + unfavorite action
// ---------------------------------------------------------------------------

// TestApp_FavoritesRemoveRequestMsg_RemovesEntries verifies
// FavoritesRemoveRequestMsg removes specified entries.
func TestApp_FavoritesRemoveRequestMsg_RemovesEntries(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{"sess-a": true, "sess-b": true, "sess-c": true}

	updated, _ := m.Update(views.FavoritesRemoveRequestMsg{
		SessionIDs: []string{"sess-a", "sess-c"},
	})
	model := updated.(Model)

	if model.favoritesMap["sess-a"] {
		t.Error("sess-a should be removed")
	}
	if model.favoritesMap["sess-c"] {
		t.Error("sess-c should be removed")
	}
	if !model.favoritesMap["sess-b"] {
		t.Error("sess-b should remain")
	}
	if len(model.favoritesMap) != 1 {
		t.Errorf("favoritesMap = %v, want 1 entry", model.favoritesMap)
	}
}

// TestApp_FavoritesRemoveRequestMsg_RemovesAll verifies removing all entries.
func TestApp_FavoritesRemoveRequestMsg_RemovesAll(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{"only": true}

	updated, _ := m.Update(views.FavoritesRemoveRequestMsg{
		SessionIDs: []string{"only"},
	})
	model := updated.(Model)

	if len(model.favoritesMap) != 0 {
		t.Errorf("favoritesMap = %v, want empty", model.favoritesMap)
	}
}

// TestApp_FavoritesRemoveRequestMsg_EmptyIDs_NoOp verifies empty IDs is no-op.
func TestApp_FavoritesRemoveRequestMsg_EmptyIDs_NoOp(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{"keep": true}

	updated, _ := m.Update(views.FavoritesRemoveRequestMsg{
		SessionIDs: []string{},
	})
	model := updated.(Model)

	if !model.favoritesMap["keep"] {
		t.Error("existing entry should survive empty removal")
	}
}

// TestApp_FavoritesRemoveRequestMsg_NonExistent_NoCrash verifies
// removing a non-existent ID does not crash.
func TestApp_FavoritesRemoveRequestMsg_NonExistent_NoCrash(t *testing.T) {
	m := newTestModel()
	m.favoritesMap = map[string]bool{"real": true}

	updated, _ := m.Update(views.FavoritesRemoveRequestMsg{
		SessionIDs: []string{"not-there"},
	})
	model := updated.(Model)

	if len(model.favoritesMap) != 1 {
		t.Errorf("favoritesMap = %v, want 1 entry", model.favoritesMap)
	}
	if !model.favoritesMap["real"] {
		t.Error("real should survive removal of non-existent ID")
	}
}
