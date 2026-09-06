// Package daemon provides tests for the delete-linked favorites pruning (task 2.3).
// Tests cover: removeFavorite unit tests, handleDeleteAction pruning,
// session.deleted event pruning, and syncFromDB pruning simulation.
package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/tomasWade/octl/internal/manage"
)

// ---------------------------------------------------------------------------
// Task 2.3: removeFavorite helper unit tests
// ---------------------------------------------------------------------------

func TestRemoveFavorite_RemovesExistingID(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	sm.mu.Lock()
	sm.removeFavorite("B")
	sm.mu.Unlock()

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	setSize := len(sm.favoritesSet)
	hasA := sm.favoritesSet["A"]
	hasB := sm.favoritesSet["B"]
	hasC := sm.favoritesSet["C"]
	sm.mu.RUnlock()

	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites after removing B, got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "C" {
		t.Errorf("order mismatch: got %v, want [A C]", favs)
	}
	if !hasA || hasB || !hasC {
		t.Errorf("favoritesSet incorrect: A=%v B=%v C=%v (want A=true B=false C=true)", hasA, hasB, hasC)
	}
	if setSize != 2 {
		t.Errorf("favoritesSet size = %d, want 2", setSize)
	}
}

func TestRemoveFavorite_IdempotentForNonFavorited(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.mu.Unlock()

	sm.mu.Lock()
	sm.removeFavorite("C")
	sm.removeFavorite("D")
	sm.removeFavorite("")
	sm.mu.Unlock()

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	setSize := len(sm.favoritesSet)
	sm.mu.RUnlock()

	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites, got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "B" {
		t.Errorf("order mismatch: got %v, want [A B]", favs)
	}
	if setSize != 2 {
		t.Errorf("favoritesSet size = %d, want 2", setSize)
	}
}

func TestRemoveFavorite_RemovingLastItem(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "only-one")
	sm.favoritesSet["only-one"] = true
	sm.mu.Unlock()

	sm.mu.Lock()
	sm.removeFavorite("only-one")
	sm.mu.Unlock()

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	setSize := len(sm.favoritesSet)
	sm.mu.RUnlock()

	if favsLen != 0 {
		t.Errorf("expected 0 favorites after removing last item, got %d", favsLen)
	}
	if setSize != 0 {
		t.Errorf("favoritesSet should be empty, got size %d", setSize)
	}
}

func TestRemoveFavorite_EmptyFavorites(t *testing.T) {
	sm := newStateManagerWithFavorites()

	sm.mu.Lock()
	sm.removeFavorite("ghost")
	sm.removeFavorite("")
	sm.mu.Unlock()

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	setSize := len(sm.favoritesSet)
	sm.mu.RUnlock()

	if favsLen != 0 {
		t.Errorf("expected 0 favorites, got %d", favsLen)
	}
	if setSize != 0 {
		t.Errorf("favoritesSet should be empty, got size %d", setSize)
	}
}

func TestRemoveFavorite_OrderPreservationAfterRemoval(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C", "D", "E")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.favoritesSet["D"] = true
	sm.favoritesSet["E"] = true
	sm.mu.Unlock()

	sm.mu.Lock()
	sm.removeFavorite("B")
	sm.removeFavorite("D")
	sm.mu.Unlock()

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 3 {
		t.Fatalf("expected 3 favorites, got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "C" || favs[2] != "E" {
		t.Errorf("order mismatch: got %v, want [A C E]", favs)
	}
}

// ---------------------------------------------------------------------------
// Task 2.3: handleDeleteAction integration — favorites pruning
// ---------------------------------------------------------------------------

func TestHandleDeleteAction_FailedResultsKeepFavorites(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('del-fav-1', 'global', '', 'del-fav-1', '/home/user', 'Fav Target 1', '1.0',
			1, 2000, 0)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('del-fav-2', 'global', '', 'del-fav-2', '/home/user', 'Fav Target 2', '1.0',
			1, 2000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "del-fav-1", "del-fav-2", "other-fav")
	sm.favoritesSet["del-fav-1"] = true
	sm.favoritesSet["del-fav-2"] = true
	sm.favoritesSet["other-fav"] = true
	sm.mu.Unlock()

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}

	sm.handleActionMsg(cl, ActionMsg{
		Type:       "action",
		Action:     "delete",
		SessionIDs: []string{"del-fav-1", "del-fav-2"},
	})

	checkFavoritesOrder(t, sm, []string{"del-fav-1", "del-fav-2", "other-fav"},
		"after failed delete: favorites should be preserved")

	output := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte(`"type":"result"`)) {
		t.Errorf("expected result message in output, got %q", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"action":"delete"`)) {
		t.Errorf("expected delete action in result, got %q", output)
	}
}

func TestHandleDeleteAction_MixedResultsPruneOnlySuccess(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C", "D")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.favoritesSet["D"] = true
	sm.mu.Unlock()

	summary := manage.Summary{
		Total:     4,
		Succeeded: 2,
		Failed:    2,
		Results: []manage.OpResult{
			{SessionID: "A", Action: "delete", Success: true},
			{SessionID: "B", Action: "delete", Success: false, Error: "deletion failed"},
			{SessionID: "C", Action: "delete", Success: true},
			{SessionID: "D", Action: "delete", Success: false, Error: "timeout"},
		},
	}

	// Apply the same pruning loop used in handleDeleteAction (lines 538-545).
	sm.mu.Lock()
	for _, r := range summary.Results {
		if r.Success {
			sm.removeFavorite(r.SessionID)
		}
	}
	sm.mu.Unlock()

	checkFavoritesOrder(t, sm, []string{"B", "D"},
		"after mixed delete: only success entries pruned")
}

func TestHandleDeleteAction_AllSuccessPrunesAll(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "X", "Y", "Z")
	sm.favoritesSet["X"] = true
	sm.favoritesSet["Y"] = true
	sm.favoritesSet["Z"] = true
	sm.mu.Unlock()

	summary := manage.Summary{
		Total:     3,
		Succeeded: 3,
		Failed:    0,
		Results: []manage.OpResult{
			{SessionID: "X", Action: "delete", Success: true},
			{SessionID: "Y", Action: "delete", Success: true},
			{SessionID: "Z", Action: "delete", Success: true},
		},
	}

	sm.mu.Lock()
	for _, r := range summary.Results {
		if r.Success {
			sm.removeFavorite(r.SessionID)
		}
	}
	sm.mu.Unlock()

	checkFavoritesOrder(t, sm, []string{}, "after all-success delete: all pruned")
}

func TestHandleDeleteAction_NoFavoritesPruningNoop(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/home/user', '', 'global', 1, 1000)`)
		wdb.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
			time_created, time_updated, time_archived)
			VALUES ('del-no-fav', 'global', '', 'del-no-fav', '/home/user', 'No Fav', '1.0',
			1, 2000, 0)`)
	})
	defer database.Close()

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}

	sm.handleActionMsg(cl, ActionMsg{
		Type:       "action",
		Action:     "delete",
		SessionIDs: []string{"del-no-fav"},
	})

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	setSize := len(sm.favoritesSet)
	sm.mu.RUnlock()

	if favsLen != 0 {
		t.Errorf("expected 0 favorites, got %d", favsLen)
	}
	if setSize != 0 {
		t.Errorf("favoritesSet should be empty, got size %d", setSize)
	}
}

func TestHandleDeleteAction_PruneThenVerifySliceAndSetConsistent(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	summary := manage.Summary{
		Total:     1,
		Succeeded: 1,
		Results:   []manage.OpResult{{SessionID: "A", Action: "delete", Success: true}},
	}

	sm.mu.Lock()
	for _, r := range summary.Results {
		if r.Success {
			sm.removeFavorite(r.SessionID)
		}
	}
	sm.mu.Unlock()

	checkFavoritesOrder(t, sm, []string{"B", "C"}, "prune A from [A,B,C] -> [B,C]")
}

// ---------------------------------------------------------------------------
// Task 2.3: session.deleted event → favorites pruning
// ---------------------------------------------------------------------------

func TestSessionDeletedEvent_RemovesFavoritedSession(t *testing.T) {
	sm := &StateManager{
		stateMap:     make(map[string]*SessionState),
		favorites:    make([]string, 0),
		favoritesSet: make(map[string]bool),
	}

	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{SessionID: "s1", Status: StatusBusy}
	sm.stateMap["s2"] = &SessionState{SessionID: "s2", Status: StatusIdle}
	sm.favorites = append(sm.favorites, "s1", "s2", "s3")
	sm.favoritesSet["s1"] = true
	sm.favoritesSet["s2"] = true
	sm.favoritesSet["s3"] = true
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.deleted", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	_, s1InMap := sm.stateMap["s1"]
	_, s2InMap := sm.stateMap["s2"]
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	setSize := len(sm.favoritesSet)
	hasS1 := sm.favoritesSet["s1"]
	hasS2 := sm.favoritesSet["s2"]
	hasS3 := sm.favoritesSet["s3"]
	sm.mu.RUnlock()

	if s1InMap {
		t.Error("s1 should have been removed from stateMap")
	}
	if !s2InMap {
		t.Error("s2 should still be in stateMap")
	}
	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites, got %d: %v", len(favs), favs)
	}
	if favs[0] != "s2" || favs[1] != "s3" {
		t.Errorf("order mismatch after s1 removal: got %v, want [s2 s3]", favs)
	}
	if hasS1 {
		t.Error("s1 should have been removed from favoritesSet")
	}
	if !hasS2 || !hasS3 {
		t.Errorf("s2=%v s3=%v should still be in favoritesSet", hasS2, hasS3)
	}
	if setSize != 2 {
		t.Errorf("favoritesSet size = %d, want 2", setSize)
	}
}

func TestSessionDeletedEvent_NonFavoritedSessionNoop(t *testing.T) {
	sm := &StateManager{
		stateMap:     make(map[string]*SessionState),
		favorites:    make([]string, 0),
		favoritesSet: make(map[string]bool),
	}

	sm.mu.Lock()
	sm.stateMap["s1"] = &SessionState{SessionID: "s1", Status: StatusBusy}
	sm.favorites = append(sm.favorites, "A", "B")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.deleted", map[string]interface{}{
		"sessionID": "s1",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	_, s1InMap := sm.stateMap["s1"]
	favsLen := len(sm.favorites)
	setSize := len(sm.favoritesSet)
	sm.mu.RUnlock()

	if s1InMap {
		t.Error("s1 should be removed from stateMap")
	}
	if favsLen != 2 {
		t.Errorf("expected 2 favorites, got %d", favsLen)
	}
	if setSize != 2 {
		t.Errorf("favoritesSet size = %d, want 2", setSize)
	}
}

func TestSessionDeletedEvent_NonExistentSessionNoPanic(t *testing.T) {
	sm := &StateManager{
		stateMap:     make(map[string]*SessionState),
		favorites:    make([]string, 0),
		favoritesSet: make(map[string]bool),
	}

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A")
	sm.favoritesSet["A"] = true
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.deleted", map[string]interface{}{
		"sessionID": "ghost",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	setSize := len(sm.favoritesSet)
	hasA := sm.favoritesSet["A"]
	sm.mu.RUnlock()

	if favsLen != 1 || !hasA {
		t.Errorf("favorites should be unchanged: len=%d, hasA=%v", favsLen, hasA)
	}
	if setSize != 1 {
		t.Errorf("favoritesSet size = %d, want 1", setSize)
	}
}

func TestSessionDeletedEvent_EmptySessionIDIgnored(t *testing.T) {
	sm := &StateManager{
		stateMap:     make(map[string]*SessionState),
		favorites:    make([]string, 0),
		favoritesSet: make(map[string]bool),
	}

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A")
	sm.favoritesSet["A"] = true
	sm.stateMap["A"] = &SessionState{SessionID: "A"}
	sm.mu.Unlock()

	evt := marshalEvent(t, "session.deleted", map[string]interface{}{
		"sessionID": "",
	})
	sm.processEvent(evt)

	sm.mu.RLock()
	_, aInMap := sm.stateMap["A"]
	favsLen := len(sm.favorites)
	sm.mu.RUnlock()

	if !aInMap {
		t.Error("A should still be in stateMap")
	}
	if favsLen != 1 {
		t.Errorf("favorites should be unchanged: got %d, want 1", favsLen)
	}
}

// ---------------------------------------------------------------------------
// Task 2.3: syncFromDB favorites pruning (simulated)
// ---------------------------------------------------------------------------

func TestSyncFromDBPruning_FavoritesAbsentFromDBRemove(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "in-db-1", "gone-1", "in-db-2", "gone-2")
	sm.favoritesSet["in-db-1"] = true
	sm.favoritesSet["gone-1"] = true
	sm.favoritesSet["in-db-2"] = true
	sm.favoritesSet["gone-2"] = true
	sm.mu.Unlock()

	dbIDs := map[string]bool{"in-db-1": true, "in-db-2": true}

	toRemove := make([]string, 0)
	sm.mu.RLock()
	for _, sid := range sm.favorites {
		if !dbIDs[sid] {
			toRemove = append(toRemove, sid)
		}
	}
	sm.mu.RUnlock()

	sm.mu.Lock()
	for _, sid := range toRemove {
		sm.removeFavorite(sid)
	}
	sm.mu.Unlock()

	checkFavoritesOrder(t, sm, []string{"in-db-1", "in-db-2"},
		"after syncFromDB pruning: absent IDs removed")
}

func TestSyncFromDBPruning_AllPresentInDBKept(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	dbIDs := map[string]bool{"A": true, "B": true, "C": true}

	toRemove := make([]string, 0)
	sm.mu.RLock()
	for _, sid := range sm.favorites {
		if !dbIDs[sid] {
			toRemove = append(toRemove, sid)
		}
	}
	sm.mu.RUnlock()

	sm.mu.Lock()
	for _, sid := range toRemove {
		sm.removeFavorite(sid)
	}
	sm.mu.Unlock()

	checkFavoritesOrder(t, sm, []string{"A", "B", "C"}, "all in DB: all favorites kept")
}

func TestSyncFromDBPruning_EmptyFavoritesNoop(t *testing.T) {
	sm := newStateManagerWithFavorites()

	dbIDs := map[string]bool{"X": true, "Y": true}

	toRemove := make([]string, 0)
	sm.mu.RLock()
	for _, sid := range sm.favorites {
		if !dbIDs[sid] {
			toRemove = append(toRemove, sid)
		}
	}
	sm.mu.RUnlock()

	sm.mu.Lock()
	for _, sid := range toRemove {
		sm.removeFavorite(sid)
	}
	sm.mu.Unlock()

	checkFavoritesOrder(t, sm, []string{}, "empty favorites: no-op")
}

func TestSyncFromDBPruning_AllAbsentAllPruned(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "gone-1", "gone-2")
	sm.favoritesSet["gone-1"] = true
	sm.favoritesSet["gone-2"] = true
	sm.mu.Unlock()

	dbIDs := map[string]bool{"other-1": true, "other-2": true}

	toRemove := make([]string, 0)
	sm.mu.RLock()
	for _, sid := range sm.favorites {
		if !dbIDs[sid] {
			toRemove = append(toRemove, sid)
		}
	}
	sm.mu.RUnlock()

	sm.mu.Lock()
	for _, sid := range toRemove {
		sm.removeFavorite(sid)
	}
	sm.mu.Unlock()

	checkFavoritesOrder(t, sm, []string{}, "all absent: all pruned")
}

func TestSyncFromDBPruning_RemoveFavoriteIdempotentInLoop(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "dup", "keep")
	sm.favoritesSet["dup"] = true
	sm.favoritesSet["keep"] = true
	sm.mu.Unlock()

	toRemove := []string{"dup", "dup"}

	sm.mu.Lock()
	for _, sid := range toRemove {
		sm.removeFavorite(sid)
	}
	sm.mu.Unlock()

	checkFavoritesOrder(t, sm, []string{"keep"}, "idempotent remove: duplicate in toRemove")
}

// ---------------------------------------------------------------------------
// Task 2.3: Combined end-to-end test
// ---------------------------------------------------------------------------

func TestTask23_CombinedDeleteEventSyncPrune(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	sm.mu.Lock()
	sm.removeFavorite("A")
	sm.mu.Unlock()
	checkFavoritesOrder(t, sm, []string{"B", "C"}, "step 1: A deleted")

	sm.processEvent(marshalEvent(t, "session.deleted", map[string]interface{}{
		"sessionID": "B",
	}))
	checkFavoritesOrder(t, sm, []string{"C"}, "step 2: B removed via event")

	dbIDs := map[string]bool{"C": true}
	toRemove := make([]string, 0)
	sm.mu.RLock()
	for _, sid := range sm.favorites {
		if !dbIDs[sid] {
			toRemove = append(toRemove, sid)
		}
	}
	sm.mu.RUnlock()
	sm.mu.Lock()
	for _, sid := range toRemove {
		sm.removeFavorite(sid)
	}
	sm.mu.Unlock()
	checkFavoritesOrder(t, sm, []string{"C"}, "step 3: DB sync, C still present")
}
