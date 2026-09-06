// Package daemon provides tests for task 2.2: ViewMsg protocol extension
// (ViewMsg.Favorites + ViewSession.IsFavorite + buildView injection +
// findSessionInProjects + integration with favorite/unfavorite actions).
package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tomasWade/octl/internal/manage"
)

// ===========================================================================
// findSessionInProjects unit tests (task 2.2)
// ===========================================================================

func TestFindSessionInProjects_FoundInFirstProject(t *testing.T) {
	projects := []ViewProject{
		{
			ProjectID: "proj-1",
			Sessions: []ViewSession{
				{SessionID: "s1", Title: "Session One"},
				{SessionID: "s2", Title: "Session Two"},
			},
		},
	}
	got, ok := findSessionInProjects(projects, "s1")
	if !ok {
		t.Fatal("expected ViewSession for s1, not found")
	}
	if got.SessionID != "s1" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "s1")
	}
	if got.Title != "Session One" {
		t.Errorf("Title = %q, want %q", got.Title, "Session One")
	}
}

func TestFindSessionInProjects_FoundInSecondProject(t *testing.T) {
	projects := []ViewProject{
		{
			ProjectID: "proj-1",
			Sessions: []ViewSession{
				{SessionID: "s1"},
			},
		},
		{
			ProjectID: "proj-2",
			Sessions: []ViewSession{
				{SessionID: "s2"},
			},
		},
	}
	got, ok := findSessionInProjects(projects, "s2")
	if !ok {
		t.Fatal("expected ViewSession for s2 in second project, not found")
	}
	if got.SessionID != "s2" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "s2")
	}
}

func TestFindSessionInProjects_NotFound(t *testing.T) {
	projects := []ViewProject{
		{
			ProjectID: "proj-1",
			Sessions: []ViewSession{
				{SessionID: "s1"},
			},
		},
	}
	got, ok := findSessionInProjects(projects, "nonexistent")
	if ok {
		t.Errorf("expected not found for nonexistent session, got %+v", got)
	}
}

func TestFindSessionInProjects_EmptyProjectsSlice(t *testing.T) {
	got, ok := findSessionInProjects([]ViewProject{}, "s1")
	if ok {
		t.Errorf("expected not found for empty projects, got %+v", got)
	}
}

func TestFindSessionInProjects_NilProjects(t *testing.T) {
	got, ok := findSessionInProjects(nil, "s1")
	if ok {
		t.Errorf("expected not found for nil projects, got %+v", got)
	}
}

func TestFindSessionInProjects_MultipleProjectsWithSameSessionID(t *testing.T) {
	projects := []ViewProject{
		{
			ProjectID: "proj-A",
			Sessions: []ViewSession{
				{SessionID: "dup", Title: "First"},
			},
		},
		{
			ProjectID: "proj-B",
			Sessions: []ViewSession{
				{SessionID: "dup", Title: "Second"},
			},
		},
	}
	got, ok := findSessionInProjects(projects, "dup")
	if !ok {
		t.Fatal("expected found for duplicate session ID")
	}
	if got.Title != "First" {
		t.Errorf("Title = %q, want %q (first match wins)", got.Title, "First")
	}
}

func TestFindSessionInProjects_ProjectWithNoSessions(t *testing.T) {
	projects := []ViewProject{
		{ProjectID: "empty-proj", Sessions: []ViewSession{}},
		{
			ProjectID: "proj-1",
			Sessions:  []ViewSession{{SessionID: "target"}},
		},
	}
	got, ok := findSessionInProjects(projects, "target")
	if !ok {
		t.Fatal("expected found for target in second project")
	}
	if got.SessionID != "target" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "target")
	}
}

// ===========================================================================
// buildView: IsFavorite injection (task 2.2)
// ===========================================================================

func newBuildViewSM(t *testing.T, seedSessions bool) (*StateManager, func()) {
	t.Helper()

	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)

		if !seedSessions {
			return
		}

		for _, id := range []string{"A", "B", "C", "D"} {
			wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
				time_created, time_updated, time_archived)
				VALUES (?, 'global', '', ?, '/home/user', ?, '1.0',
				1, 1000, 0)`, id, id, "Session "+id)
		}
	})

	sm := NewStateManager(database)
	cleanup := func() {
		database.Close()
	}
	return sm, cleanup
}

func testSeedSessions(t *testing.T) *StateManager {
	t.Helper()
	sm, cleanup := newBuildViewSM(t, true)
	t.Cleanup(cleanup)
	return sm
}

func TestBuildView_IsFavoriteInjection(t *testing.T) {
	sm := testSeedSessions(t)

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	if len(view.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(view.Projects))
	}

	sessions := make(map[string]*ViewSession, len(view.Projects[0].Sessions))
	for i := range view.Projects[0].Sessions {
		s := &view.Projects[0].Sessions[i]
		sessions[s.SessionID] = s
	}

	if len(sessions) != 4 {
		t.Fatalf("expected 4 sessions, got %d", len(sessions))
	}

	for _, id := range []string{"A", "C"} {
		s, ok := sessions[id]
		if !ok {
			t.Fatalf("session %q not found in view", id)
		}
		if !s.IsFavorite {
			t.Errorf("session %q: IsFavorite = false, want true", id)
		}
	}

	for _, id := range []string{"B", "D"} {
		s, ok := sessions[id]
		if !ok {
			t.Fatalf("session %q not found in view", id)
		}
		if s.IsFavorite {
			t.Errorf("session %q: IsFavorite = true, want false", id)
		}
	}
}

func TestBuildView_FavoritesListInsertionOrder(t *testing.T) {
	sm := testSeedSessions(t)

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "C", "B")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["C"] = true
	sm.favoritesSet["B"] = true
	sm.mu.Unlock()

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	if len(view.Favorites) != 3 {
		t.Fatalf("expected 3 favorites, got %d", len(view.Favorites))
	}

	want := []string{"A", "C", "B"}
	for i, id := range want {
		if view.Favorites[i].SessionID != id {
			t.Errorf("Favorites[%d].SessionID = %q, want %q", i, view.Favorites[i].SessionID, id)
		}
		if !view.Favorites[i].IsFavorite {
			t.Errorf("Favorites[%d].IsFavorite = false for %q, want true", i, id)
		}
	}
}

func TestBuildView_FavoritesListOnlyContainsFavoritedSessions(t *testing.T) {
	sm := testSeedSessions(t)

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	if len(view.Favorites) != 2 {
		t.Fatalf("expected 2 favorites, got %d", len(view.Favorites))
	}

	ids := make(map[string]bool)
	for _, fav := range view.Favorites {
		ids[fav.SessionID] = true
	}
	if !ids["A"] || !ids["C"] {
		t.Errorf("favorites should contain A and C, got %v", ids)
	}
	if ids["B"] || ids["D"] {
		t.Errorf("favorites should NOT contain non-favorited sessions, got %v", ids)
	}
}

func TestBuildView_OrphanFavoriteSkipped(t *testing.T) {
	sm := testSeedSessions(t)

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "ORPHAN")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["ORPHAN"] = true
	sm.mu.Unlock()

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	if len(view.Favorites) != 1 {
		t.Fatalf("expected 1 favorite (orphan skipped), got %d: %+v", len(view.Favorites), view.Favorites)
	}
	if view.Favorites[0].SessionID != "A" {
		t.Errorf("Favorites[0].SessionID = %q, want %q", view.Favorites[0].SessionID, "A")
	}
}

func TestBuildView_EmptyFavorites(t *testing.T) {
	sm := testSeedSessions(t)

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	if len(view.Favorites) != 0 {
		t.Errorf("expected 0 favorites for empty favorites, got %d", len(view.Favorites))
	}

	for _, proj := range view.Projects {
		for _, s := range proj.Sessions {
			if s.IsFavorite {
				t.Errorf("session %q: IsFavorite = true with no favorites set, want false", s.SessionID)
			}
		}
	}
}

func TestBuildView_FavoritesDoesNotMutateStateManager(t *testing.T) {
	sm := testSeedSessions(t)

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	sm.mu.RLock()
	favsBefore := make([]string, len(sm.favorites))
	copy(favsBefore, sm.favorites)
	setSizeBefore := len(sm.favoritesSet)
	sm.mu.RUnlock()

	_, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	sm.mu.RLock()
	favsAfter := make([]string, len(sm.favorites))
	copy(favsAfter, sm.favorites)
	setSizeAfter := len(sm.favoritesSet)
	sm.mu.RUnlock()

	if len(favsAfter) != len(favsBefore) {
		t.Errorf("favorites length mutated: before=%d after=%d", len(favsBefore), len(favsAfter))
	}
	if setSizeAfter != setSizeBefore {
		t.Errorf("favoritesSet size mutated: before=%d after=%d", setSizeBefore, setSizeAfter)
	}
	for i, id := range favsBefore {
		if favsAfter[i] != id {
			t.Errorf("favorites[%d] mutated: before=%q after=%q", i, id, favsAfter[i])
		}
	}
}

// ===========================================================================
// buildView: JSON round-trip with omitempty (task 2.2)
// ===========================================================================

func TestBuildView_FavoritesJSONKeyPresentWhenPopulated(t *testing.T) {
	sm := testSeedSessions(t)

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A")
	sm.favoritesSet["A"] = true
	sm.mu.Unlock()

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal ViewMsg: %v", err)
	}

	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"favorites"`) {
		t.Errorf("JSON should contain 'favorites' key when populated, got: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"isFavorite":true`) {
		t.Errorf("JSON should contain 'isFavorite' for favorited session, got: %s", jsonStr)
	}
}

func TestBuildView_FavoritesJSONKeyAbsentWhenEmpty(t *testing.T) {
	sm := testSeedSessions(t)

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal ViewMsg: %v", err)
	}

	jsonStr := string(data)
	if strings.Contains(jsonStr, `"favorites"`) {
		t.Errorf("JSON should NOT contain 'favorites' key when empty (omitempty), got: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"isFavorite"`) {
		t.Errorf("JSON should contain 'isFavorite' on sessions even when favorites is empty, got: %s", jsonStr)
	}
}

func TestBuildView_ViewMsgJSONRoundTrip(t *testing.T) {
	sm := testSeedSessions(t)

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal ViewMsg: %v", err)
	}

	var decoded ViewMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal ViewMsg: %v", err)
	}

	if decoded.Type != "view" {
		t.Errorf("Type = %q, want %q", decoded.Type, "view")
	}
	if len(decoded.Favorites) != 2 {
		t.Errorf("Favorites len = %d, want 2", len(decoded.Favorites))
	}
	if decoded.Favorites[0].SessionID != "A" {
		t.Errorf("Favorites[0].SessionID = %q, want %q", decoded.Favorites[0].SessionID, "A")
	}
	if !decoded.Favorites[0].IsFavorite {
		t.Error("Favorites[0].IsFavorite = false, want true")
	}
	if decoded.Favorites[1].SessionID != "C" {
		t.Errorf("Favorites[1].SessionID = %q, want %q", decoded.Favorites[1].SessionID, "C")
	}
	if !decoded.Favorites[1].IsFavorite {
		t.Error("Favorites[1].IsFavorite = false, want true")
	}
}

// ===========================================================================
// buildView: integration with handleActionMsg (task 2.2)
// ===========================================================================

func TestBuildView_FavoriteActionThenBuildView(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('fav-int-1', 'global', '', 'fav-int-1', '/home/user', 'Fav Integration', '1.0',
			1, 1000, 0)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('fav-int-2', 'global', '', 'fav-int-2', '/home/user', 'Not Fav', '1.0',
			1, 1000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}
	sm.handleActionMsg(cl, ActionMsg{
		Type:       "action",
		Action:     "favorite",
		SessionIDs: []string{"fav-int-1"},
	})

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	if len(view.Projects) == 0 {
		t.Fatal("expected at least 1 project")
	}

	var fav1, fav2 *ViewSession
	for i := range view.Projects[0].Sessions {
		s := &view.Projects[0].Sessions[i]
		if s.SessionID == "fav-int-1" {
			fav1 = s
		} else if s.SessionID == "fav-int-2" {
			fav2 = s
		}
	}

	if fav1 == nil {
		t.Fatal("fav-int-1 not found in view")
	}
	if !fav1.IsFavorite {
		t.Error("fav-int-1: IsFavorite = false after favorite action, want true")
	}

	if fav2 == nil {
		t.Fatal("fav-int-2 not found in view")
	}
	if fav2.IsFavorite {
		t.Error("fav-int-2: IsFavorite = true, want false (not favorited)")
	}

	if len(view.Favorites) != 1 {
		t.Fatalf("expected 1 entry in Favorites, got %d", len(view.Favorites))
	}
	if view.Favorites[0].SessionID != "fav-int-1" {
		t.Errorf("Favorites[0].SessionID = %q, want %q", view.Favorites[0].SessionID, "fav-int-1")
	}
	if !view.Favorites[0].IsFavorite {
		t.Error("Favorites[0].IsFavorite = false, want true")
	}
}

func TestBuildView_UnfavoriteActionThenBuildView(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('unfav-1', 'global', '', 'unfav-1', '/home/user', 'To Unfav', '1.0',
			1, 1000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "unfav-1")
	sm.favoritesSet["unfav-1"] = true
	sm.mu.Unlock()

	viewBefore, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView before: %v", err)
	}
	if len(viewBefore.Favorites) != 1 || viewBefore.Favorites[0].SessionID != "unfav-1" {
		t.Fatalf("before unfavorite: expected 1 favorite 'unfav-1', got %+v", viewBefore.Favorites)
	}

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}
	sm.handleActionMsg(cl, ActionMsg{
		Type:       "action",
		Action:     "unfavorite",
		SessionIDs: []string{"unfav-1"},
	})

	viewAfter, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView after: %v", err)
	}

	if len(viewAfter.Favorites) != 0 {
		t.Errorf("expected 0 favorites after unfavorite, got %d", len(viewAfter.Favorites))
	}

	found := false
	for _, proj := range viewAfter.Projects {
		for _, s := range proj.Sessions {
			if s.SessionID == "unfav-1" {
				found = true
				if s.IsFavorite {
					t.Error("unfav-1: IsFavorite = true after unfavorite, want false")
				}
			}
		}
	}
	if !found {
		t.Fatal("unfav-1 not found in view after unfavorite")
	}
}

func TestBuildView_FavoriteAndUnfavoriteRoundTrip(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('round-1', 'global', '', 'round-1', '/home/user', 'Round Trip', '1.0',
			1, 1000, 0)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('round-2', 'global', '', 'round-2', '/home/user', 'Round Trip 2', '1.0',
			1, 1000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))
	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}

	sm.handleActionMsg(cl, ActionMsg{Type: "action", Action: "favorite", SessionIDs: []string{"round-1", "round-2"}})

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView step1: %v", err)
	}
	if len(view.Favorites) != 2 {
		t.Fatalf("step1: expected 2 favorites, got %d", len(view.Favorites))
	}

	sm.handleActionMsg(cl, ActionMsg{Type: "action", Action: "unfavorite", SessionIDs: []string{"round-1"}})

	view, err = sm.buildView()
	if err != nil {
		t.Fatalf("buildView step2: %v", err)
	}
	if len(view.Favorites) != 1 {
		t.Fatalf("step2: expected 1 favorite, got %d", len(view.Favorites))
	}
	if view.Favorites[0].SessionID != "round-2" {
		t.Errorf("step2: Favorites[0] = %q, want %q", view.Favorites[0].SessionID, "round-2")
	}

	sm.handleActionMsg(cl, ActionMsg{Type: "action", Action: "favorite", SessionIDs: []string{"round-1"}})

	view, err = sm.buildView()
	if err != nil {
		t.Fatalf("buildView step3: %v", err)
	}
	if len(view.Favorites) != 2 {
		t.Fatalf("step3: expected 2 favorites, got %d", len(view.Favorites))
	}
	if view.Favorites[0].SessionID != "round-2" {
		t.Errorf("step3: Favorites[0] = %q, want %q", view.Favorites[0].SessionID, "round-2")
	}
	if view.Favorites[1].SessionID != "round-1" {
		t.Errorf("step3: Favorites[1] = %q, want %q", view.Favorites[1].SessionID, "round-1")
	}
}

func TestBuildView_FavoritesPopulatedAfterFavoriteAction(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			agent, cost, tokens_input, tokens_output, time_created, time_updated, time_archived)
			VALUES ('fav-full-1', 'global', '', 'fav-full-1', '/home/user', 'Full Test', '1.0',
			'coder', 0.01, 10, 5, 1, 1000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))
	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}

	sm.handleActionMsg(cl, ActionMsg{Type: "action", Action: "favorite", SessionIDs: []string{"fav-full-1"}})

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	if view.Type != "view" {
		t.Errorf("Type = %q, want %q", view.Type, "view")
	}
	if len(view.Favorites) != 1 {
		t.Fatalf("Favorites len = %d, want 1", len(view.Favorites))
	}

	fav := view.Favorites[0]
	if fav.SessionID != "fav-full-1" {
		t.Errorf("Favorites[0].SessionID = %q, want %q", fav.SessionID, "fav-full-1")
	}
	if fav.Title != "Full Test" {
		t.Errorf("Favorites[0].Title = %q, want %q", fav.Title, "Full Test")
	}
	if fav.Cost != 0.01 {
		t.Errorf("Favorites[0].Cost = %v, want 0.01", fav.Cost)
	}
	if !fav.IsFavorite {
		t.Error("Favorites[0].IsFavorite = false, want true")
	}

	found := false
	for _, proj := range view.Projects {
		for _, s := range proj.Sessions {
			if s.SessionID == "fav-full-1" {
				found = true
				if !s.IsFavorite {
					t.Error("session in Projects: IsFavorite = false, want true")
				}
			}
		}
	}
	if !found {
		t.Fatal("fav-full-1 not found in Projects")
	}

	if view.Stats.TotalSessions != 1 {
		t.Errorf("TotalSessions = %d, want 1", view.Stats.TotalSessions)
	}
	if view.Stats.TotalCost != 0.01 {
		t.Errorf("TotalCost = %v, want 0.01", view.Stats.TotalCost)
	}
}

func TestBuildView_FavoritesOnViewMsgAfterMultipleActions(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		for _, id := range []string{"s1", "s2", "s3"} {
			wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
				time_created, time_updated, time_archived)
				VALUES (?, 'global', '', ?, '/home/user', ?, '1.0',
				1, 1000, 0)`, id, id, "Sess "+id)
		}
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))
	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}

	sm.handleActionMsg(cl, ActionMsg{Type: "action", Action: "favorite", SessionIDs: []string{"s1", "s2", "s3"}})
	sm.handleActionMsg(cl, ActionMsg{Type: "action", Action: "unfavorite", SessionIDs: []string{"s2"}})

	view, err := sm.buildView()
	if err != nil {
		t.Fatalf("buildView: %v", err)
	}

	if len(view.Favorites) != 2 {
		t.Fatalf("expected 2 favorites after removing s2, got %d", len(view.Favorites))
	}

	if view.Favorites[0].SessionID != "s1" {
		t.Errorf("Favorites[0].SessionID = %q, want %q", view.Favorites[0].SessionID, "s1")
	}
	if !view.Favorites[0].IsFavorite {
		t.Error("Favorites[0].IsFavorite = false, want true")
	}
	if view.Favorites[1].SessionID != "s3" {
		t.Errorf("Favorites[1].SessionID = %q, want %q", view.Favorites[1].SessionID, "s3")
	}
	if !view.Favorites[1].IsFavorite {
		t.Error("Favorites[1].IsFavorite = false, want true")
	}

	sessions := make(map[string]bool)
	for _, proj := range view.Projects {
		for _, s := range proj.Sessions {
			sessions[s.SessionID] = s.IsFavorite
		}
	}
	if !sessions["s1"] {
		t.Error("s1.IsFavorite should be true")
	}
	if sessions["s2"] {
		t.Error("s2.IsFavorite should be false (unfavorited)")
	}
	if !sessions["s3"] {
		t.Error("s3.IsFavorite should be true")
	}
}
