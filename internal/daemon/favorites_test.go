// Package daemon provides tests for the favorites store and favorite/unfavorite
// action handlers (task 2.1).
package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/tomasWade/octl/internal/manage"
)

// ---------------------------------------------------------------------------
// Favorites: handleFavoriteAction (task 2.1)
// ---------------------------------------------------------------------------

// newStateManagerWithFavorites creates a StateManager with favorites initialized
// for testing. The manager and db fields are nil — these tests only exercise
// the favorites store and action handlers, which don't require them.
func newStateManagerWithFavorites() *StateManager {
	return NewStateManagerWithSocket(nil, "")
}

func TestHandleFavoriteAction_AddsMultipleIDs(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.handleFavoriteAction(ActionMsg{
		Type:       "action",
		Action:     "favorite",
		SessionIDs: []string{"A", "B", "C"},
	})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 3 {
		t.Fatalf("expected 3 favorites, got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "B" || favs[2] != "C" {
		t.Errorf("favorites order mismatch: got %v, want [A B C]", favs)
	}

	sm.mu.RLock()
	for _, id := range []string{"A", "B", "C"} {
		if !sm.favoritesSet[id] {
			t.Errorf("%q should be in favoritesSet", id)
		}
	}
	sm.mu.RUnlock()
}

func TestHandleFavoriteAction_PreservesInsertionOrder(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"D", "A", "C", "B"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 4 {
		t.Fatalf("expected 4 favorites, got %d", len(favs))
	}
	if favs[0] != "D" || favs[1] != "A" || favs[2] != "C" || favs[3] != "B" {
		t.Errorf("favorites order mismatch: got %v, want [D A C B]", favs)
	}
}

func TestHandleFavoriteAction_DuplicateIDsIdempotent(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"A", "A", "A"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 1 {
		t.Fatalf("duplicate IDs should not multi-append: got %d favorites, want 1", len(favs))
	}
	if favs[0] != "A" {
		t.Errorf("expected [A], got %v", favs)
	}
}

func TestHandleFavoriteAction_AlreadyFavoritedNoopSuccess(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A")
	sm.favoritesSet["A"] = true
	sm.mu.Unlock()

	summary := sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"A"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 1 {
		t.Errorf("expected 1 favorite after no-op, got %d", len(favs))
	}
	if favs[0] != "A" {
		t.Errorf("expected [A], got %v", favs)
	}

	if summary.Total != 1 {
		t.Errorf("Summary.Total = %d, want 1", summary.Total)
	}
	if summary.Succeeded != 1 {
		t.Errorf("Summary.Succeeded = %d, want 1", summary.Succeeded)
	}
	if summary.Failed != 0 {
		t.Errorf("Summary.Failed = %d, want 0", summary.Failed)
	}
	if len(summary.Results) != 1 {
		t.Fatalf("expected 1 Result, got %d", len(summary.Results))
	}
	r := summary.Results[0]
	if r.SessionID != "A" {
		t.Errorf("Result.SessionID = %q, want %q", r.SessionID, "A")
	}
	if r.Action != "favorite" {
		t.Errorf("Result.Action = %q, want %q", r.Action, "favorite")
	}
	if !r.Success {
		t.Error("Result.Success should be true even for already-favorited")
	}
}

func TestHandleFavoriteAction_CorrectSummary(t *testing.T) {
	sm := newStateManagerWithFavorites()
	summary := sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"X", "Y", "Z"}})

	if summary.Total != 3 {
		t.Errorf("Summary.Total = %d, want 3", summary.Total)
	}
	if summary.Succeeded != 3 {
		t.Errorf("Summary.Succeeded = %d, want 3", summary.Succeeded)
	}
	if summary.Failed != 0 {
		t.Errorf("Summary.Failed = %d, want 0", summary.Failed)
	}
	if len(summary.Results) != 3 {
		t.Fatalf("expected 3 Results, got %d", len(summary.Results))
	}
	for i, r := range summary.Results {
		if r.SessionID != []string{"X", "Y", "Z"}[i] {
			t.Errorf("Results[%d].SessionID = %q, want %q", i, r.SessionID, []string{"X", "Y", "Z"}[i])
		}
		if r.Action != "favorite" {
			t.Errorf("Results[%d].Action = %q, want favorite", i, r.Action)
		}
		if !r.Success {
			t.Errorf("Results[%d].Success should be true", i)
		}
		if r.Error != "" {
			t.Errorf("Results[%d].Error should be empty, got %q", i, r.Error)
		}
	}
}

func TestHandleFavoriteAction_EmptySessionIDs(t *testing.T) {
	sm := newStateManagerWithFavorites()
	summary := sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{}})

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	sm.mu.RUnlock()

	if favsLen != 0 {
		t.Errorf("expected 0 favorites, got %d", favsLen)
	}
	if summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", summary.Total)
	}
	if summary.Succeeded != 0 {
		t.Errorf("Summary.Succeeded = %d, want 0", summary.Succeeded)
	}
	if len(summary.Results) != 0 {
		t.Errorf("expected 0 Results, got %d", len(summary.Results))
	}
}

func TestHandleFavoriteAction_NilSessionIDs(t *testing.T) {
	sm := newStateManagerWithFavorites()
	summary := sm.handleFavoriteAction(ActionMsg{Action: "favorite"})

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	sm.mu.RUnlock()

	if favsLen != 0 {
		t.Errorf("expected 0 favorites for nil SessionIDs, got %d", favsLen)
	}
	if summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", summary.Total)
	}
}

func TestHandleFavoriteAction_MixedNewAndExisting(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	summary := sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"A", "B", "C", "D"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 4 {
		t.Fatalf("expected 4 favorites, got %d: %v", len(favs), favs)
	}
	want := []string{"A", "C", "B", "D"}
	for i, id := range want {
		if favs[i] != id {
			t.Errorf("position %d: got %q, want %q", i, favs[i], id)
		}
	}

	if summary.Total != 4 {
		t.Errorf("Summary.Total = %d, want 4", summary.Total)
	}
	if summary.Succeeded != 4 {
		t.Errorf("Summary.Succeeded = %d, want 4", summary.Succeeded)
	}
	if len(summary.Results) != 4 {
		t.Errorf("expected 4 Results, got %d", len(summary.Results))
	}
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// Favorites: handleFavoriteAction empty/blank SID defense (task 3.2)
// ---------------------------------------------------------------------------

func TestHandleFavoriteAction_SkipsEmptyAndBlankSIDs(t *testing.T) {
	sm := newStateManagerWithFavorites()
	summary := sm.handleFavoriteAction(ActionMsg{
		Action:     "favorite",
		SessionIDs: []string{"", "  ", "\t", "A", "B"},
	})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites (blank SIDs skipped), got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "B" {
		t.Errorf("order mismatch: got %v, want [A B]", favs)
	}
	sm.mu.RLock()
	if sm.favoritesSet[""] {
		t.Error("empty string should NOT be in favoritesSet")
	}
	if sm.favoritesSet["  "] {
		t.Error("whitespace-only should NOT be in favoritesSet")
	}
	if !sm.favoritesSet["A"] || !sm.favoritesSet["B"] {
		t.Error("A and B should be in favoritesSet")
	}
	sm.mu.RUnlock()

	// Summary reflects only valid SIDs
	if summary.Total != 2 {
		t.Errorf("Summary.Total = %d, want 2 (blank SIDs excluded)", summary.Total)
	}
	if summary.Succeeded != 2 {
		t.Errorf("Summary.Succeeded = %d, want 2", summary.Succeeded)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("expected 2 Results, got %d", len(summary.Results))
	}
	for i, r := range summary.Results {
		if r.SessionID != []string{"A", "B"}[i] {
			t.Errorf("Results[%d].SessionID = %q, want %q", i, r.SessionID, []string{"A", "B"}[i])
		}
		if !r.Success {
			t.Errorf("Results[%d].Success should be true", i)
		}
	}
}

func TestHandleFavoriteAction_AllBlankSIDsAllSkipped(t *testing.T) {
	sm := newStateManagerWithFavorites()
	summary := sm.handleFavoriteAction(ActionMsg{
		Action:     "favorite",
		SessionIDs: []string{"", "   ", "\t\n"},
	})

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	setLen := len(sm.favoritesSet)
	sm.mu.RUnlock()

	if favsLen != 0 {
		t.Errorf("expected 0 favorites, got %d", favsLen)
	}
	if setLen != 0 {
		t.Errorf("favoritesSet should be empty, got size %d", setLen)
	}
	if summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", summary.Total)
	}
	if summary.Succeeded != 0 {
		t.Errorf("Summary.Succeeded = %d, want 0", summary.Succeeded)
	}
	if len(summary.Results) != 0 {
		t.Errorf("expected 0 Results, got %d", len(summary.Results))
	}
}

func TestHandleFavoriteAction_BlankSIDsWithAlreadyFavoritedNoop(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A")
	sm.favoritesSet["A"] = true
	sm.mu.Unlock()

	summary := sm.handleFavoriteAction(ActionMsg{
		Action:     "favorite",
		SessionIDs: []string{"", "  ", "A", "\t"},
	})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 1 || favs[0] != "A" {
		t.Fatalf("expected [A] unchanged, got %v", favs)
	}

	if summary.Total != 1 {
		t.Errorf("Summary.Total = %d, want 1 (only A is valid)", summary.Total)
	}
	if summary.Succeeded != 1 {
		t.Errorf("Summary.Succeeded = %d, want 1", summary.Succeeded)
	}
	r := summary.Results[0]
	if r.SessionID != "A" {
		t.Errorf("Result.SessionID = %q, want %q", r.SessionID, "A")
	}
	if !r.Success {
		t.Error("Result.Success should be true")
	}
}

// Favorites: handleUnfavoriteAction (task 2.1)
// ---------------------------------------------------------------------------

func TestHandleUnfavoriteAction_RemovesExistingIDs(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"B"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites after removing B, got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "C" {
		t.Errorf("order mismatch: got %v, want [A C]", favs)
	}
	sm.mu.RLock()
	if sm.favoritesSet["B"] {
		t.Error("B should have been removed from favoritesSet")
	}
	if !sm.favoritesSet["A"] || !sm.favoritesSet["C"] {
		t.Error("A and C should still be in favoritesSet")
	}
	sm.mu.RUnlock()
}

func TestHandleUnfavoriteAction_OrderPreservation(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C", "D")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.favoritesSet["D"] = true
	sm.mu.Unlock()

	sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"B", "D"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites, got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "C" {
		t.Errorf("order broken after removal: got %v, want [A C]", favs)
	}
}

func TestHandleUnfavoriteAction_NonFavoritedNoopSuccess(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A")
	sm.favoritesSet["A"] = true
	sm.mu.Unlock()

	summary := sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"B"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 1 || favs[0] != "A" {
		t.Errorf("A should still be favorited, got %v", favs)
	}

	if summary.Total != 1 {
		t.Errorf("Summary.Total = %d, want 1", summary.Total)
	}
	if summary.Succeeded != 1 {
		t.Errorf("Summary.Succeeded = %d, want 1", summary.Succeeded)
	}
	r := summary.Results[0]
	if r.SessionID != "B" {
		t.Errorf("Result.SessionID = %q, want %q", r.SessionID, "B")
	}
	if r.Action != "unfavorite" {
		t.Errorf("Result.Action = %q, want unfavorite", r.Action)
	}
	if !r.Success {
		t.Error("non-favorited unfavorite should still return Success=true")
	}
}

func TestHandleUnfavoriteAction_MixedSomeFavoritedSomeNot(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.mu.Unlock()

	summary := sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"B", "C", "D"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 1 || favs[0] != "A" {
		t.Errorf("expected [A], got %v", favs)
	}

	if summary.Total != 3 {
		t.Errorf("Summary.Total = %d, want 3", summary.Total)
	}
	if summary.Succeeded != 3 {
		t.Errorf("Summary.Succeeded = %d, want 3", summary.Succeeded)
	}
	if len(summary.Results) != 3 {
		t.Fatalf("expected 3 Results, got %d", len(summary.Results))
	}
	for i, id := range []string{"B", "C", "D"} {
		if summary.Results[i].SessionID != id {
			t.Errorf("Results[%d].SessionID = %q, want %q", i, summary.Results[i].SessionID, id)
		}
		if !summary.Results[i].Success {
			t.Errorf("Results[%d] for %q should have Success=true", i, id)
		}
		if summary.Results[i].Action != "unfavorite" {
			t.Errorf("Results[%d].Action = %q, want unfavorite", i, summary.Results[i].Action)
		}
	}
}

func TestHandleUnfavoriteAction_DuplicateIDsInOneMessage(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.mu.Unlock()

	summary := sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"B", "B", "B"}})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 1 || favs[0] != "A" {
		t.Errorf("expected [A], got %v", favs)
	}

	if summary.Total != 3 {
		t.Errorf("Summary.Total = %d, want 3", summary.Total)
	}
	if summary.Succeeded != 3 {
		t.Errorf("Summary.Succeeded = %d, want 3", summary.Succeeded)
	}
	if len(summary.Results) != 3 {
		t.Fatalf("expected 3 Results, got %d", len(summary.Results))
	}
	for _, r := range summary.Results {
		if r.SessionID != "B" {
			t.Errorf("Result.SessionID = %q, want B", r.SessionID)
		}
		if !r.Success {
			t.Error("all results should have Success=true")
		}
	}
}

func TestHandleUnfavoriteAction_EmptySessionIDs(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A")
	sm.favoritesSet["A"] = true
	sm.mu.Unlock()

	summary := sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{}})

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	sm.mu.RUnlock()

	if favsLen != 1 {
		t.Errorf("empty SessionIDs should not mutate favorites, got len %d", favsLen)
	}
	if summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", summary.Total)
	}
	if summary.Succeeded != 0 {
		t.Errorf("Summary.Succeeded = %d, want 0", summary.Succeeded)
	}
}

func TestHandleUnfavoriteAction_NilSessionIDs(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A")
	sm.favoritesSet["A"] = true
	sm.mu.Unlock()

	summary := sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite"})

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	sm.mu.RUnlock()

	if favsLen != 1 {
		t.Errorf("nil SessionIDs should not mutate favorites, got len %d", favsLen)
	}
	if summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", summary.Total)
	}
}

func TestHandleUnfavoriteAction_CorrectSummary(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "X", "Y")
	sm.favoritesSet["X"] = true
	sm.favoritesSet["Y"] = true
	sm.mu.Unlock()

	summary := sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"X", "Y"}})

	if summary.Total != 2 {
		t.Errorf("Summary.Total = %d, want 2", summary.Total)
	}
	if summary.Succeeded != 2 {
		t.Errorf("Summary.Succeeded = %d, want 2", summary.Succeeded)
	}
	if summary.Failed != 0 {
		t.Errorf("Summary.Failed = %d, want 0", summary.Failed)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("expected 2 Results, got %d", len(summary.Results))
	}
	for i, r := range summary.Results {
		if r.SessionID != []string{"X", "Y"}[i] {
			t.Errorf("Results[%d].SessionID = %q", i, r.SessionID)
		}
		if r.Action != "unfavorite" {
			t.Errorf("Results[%d].Action = %q, want unfavorite", i, r.Action)
		}
		if !r.Success {
			t.Errorf("Results[%d].Success should be true", i)
		}
		if r.Error != "" {
			t.Errorf("Results[%d].Error should be empty", i)
		}
	}
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// Favorites: handleUnfavoriteAction empty/blank SID defense (task 3.2)
// ---------------------------------------------------------------------------

func TestHandleUnfavoriteAction_SkipsEmptyAndBlankSIDs(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	summary := sm.handleUnfavoriteAction(ActionMsg{
		Action:     "unfavorite",
		SessionIDs: []string{"", "  ", "B", "\t", "D"},
	})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites (B removed, blank/D skipped), got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "C" {
		t.Errorf("order mismatch after removing B: got %v, want [A C]", favs)
	}

	// Summary reflects only valid non-blank SIDs: B and D
	if summary.Total != 2 {
		t.Errorf("Summary.Total = %d, want 2 (blank SIDs excluded)", summary.Total)
	}
	if summary.Succeeded != 2 {
		t.Errorf("Summary.Succeeded = %d, want 2", summary.Succeeded)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("expected 2 Results, got %d", len(summary.Results))
	}
	for i, id := range []string{"B", "D"} {
		if summary.Results[i].SessionID != id {
			t.Errorf("Results[%d].SessionID = %q, want %q", i, summary.Results[i].SessionID, id)
		}
		if !summary.Results[i].Success {
			t.Errorf("Results[%d] for %q should have Success=true", i, id)
		}
	}
}

func TestHandleUnfavoriteAction_AllBlankSIDsAllSkipped(t *testing.T) {
	sm := newStateManagerWithFavorites()
	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.mu.Unlock()

	summary := sm.handleUnfavoriteAction(ActionMsg{
		Action:     "unfavorite",
		SessionIDs: []string{"", "   ", "\t\n"},
	})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites unchanged, got %d: %v", len(favs), favs)
	}
	if favs[0] != "A" || favs[1] != "B" {
		t.Errorf("order should be unchanged: got %v, want [A B]", favs)
	}
	if summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", summary.Total)
	}
	if summary.Succeeded != 0 {
		t.Errorf("Summary.Succeeded = %d, want 0", summary.Succeeded)
	}
	if len(summary.Results) != 0 {
		t.Errorf("expected 0 Results, got %d", len(summary.Results))
	}
}

func TestHandleUnfavoriteAction_BlankSIDWithNonFavoritedNoop(t *testing.T) {
	sm := newStateManagerWithFavorites()
	summary := sm.handleUnfavoriteAction(ActionMsg{
		Action:     "unfavorite",
		SessionIDs: []string{"", "  ", "X"},
	})

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	sm.mu.RUnlock()

	if favsLen != 0 {
		t.Errorf("expected 0 favorites, got %d", favsLen)
	}

	if summary.Total != 1 {
		t.Errorf("Summary.Total = %d, want 1", summary.Total)
	}
	if summary.Succeeded != 1 {
		t.Errorf("Summary.Succeeded = %d, want 1", summary.Succeeded)
	}
	r := summary.Results[0]
	if r.SessionID != "X" {
		t.Errorf("Result.SessionID = %q, want %q", r.SessionID, "X")
	}
	if !r.Success {
		t.Error("Result.Success should be true")
	}
}

// Favorites: order invariants (task 2.1)
// ---------------------------------------------------------------------------

func checkFavoritesOrder(t *testing.T, sm *StateManager, want []string, contextMsg string) {
	t.Helper()
	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != len(want) {
		t.Fatalf("%s: expected %d favorites, got %d: %v", contextMsg, len(want), len(favs), favs)
	}
	for i, id := range want {
		if favs[i] != id {
			t.Errorf("%s: position %d: got %q, want %q (full slice: %v)", contextMsg, i, favs[i], id, favs)
		}
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, id := range want {
		if !sm.favoritesSet[id] {
			t.Errorf("%s: %q missing from favoritesSet", contextMsg, id)
		}
	}
	if len(sm.favoritesSet) != len(want) {
		t.Errorf("%s: favoritesSet size = %d, want %d", contextMsg, len(sm.favoritesSet), len(want))
	}
}

func TestFavorites_OrderInvariant_FavoriteThenUnfavoriteThenReFavorite(t *testing.T) {
	sm := newStateManagerWithFavorites()

	sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"A", "B", "C"}})
	checkFavoritesOrder(t, sm, []string{"A", "B", "C"}, "after favorite [A,B,C]")

	sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"B"}})
	checkFavoritesOrder(t, sm, []string{"A", "C"}, "after unfavorite B")

	sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"B"}})
	checkFavoritesOrder(t, sm, []string{"A", "C", "B"}, "after re-favorite B")
}

func TestFavorites_OrderInvariant_FavoriteThenUnfavoriteThenFavoriteNew(t *testing.T) {
	sm := newStateManagerWithFavorites()

	sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"X", "Y", "Z"}})
	sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"Y"}})
	checkFavoritesOrder(t, sm, []string{"X", "Z"}, "after unfavorite Y")

	sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"W"}})
	checkFavoritesOrder(t, sm, []string{"X", "Z", "W"}, "after favorite W")
}

func TestFavorites_OrderInvariant_UnfavoriteAllThenFavoriteAgain(t *testing.T) {
	sm := newStateManagerWithFavorites()

	sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"A", "B"}})
	sm.handleUnfavoriteAction(ActionMsg{Action: "unfavorite", SessionIDs: []string{"A", "B"}})
	checkFavoritesOrder(t, sm, []string{}, "after unfavorite all")

	sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"C", "D"}})
	checkFavoritesOrder(t, sm, []string{"C", "D"}, "after favorite new set")
}

// ---------------------------------------------------------------------------
// Favorites: integration via handleActionMsg (task 2.1)
// ---------------------------------------------------------------------------

func setupActionDB(t *testing.T, seedFn func(wdb *sql.DB)) *StateManager {
	t.Helper()
	database := setupDBWithData(t, seedFn)
	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))
	return sm
}

func TestHandleActionMsg_Favorite(t *testing.T) {
	sm := setupActionDB(t, nil)
	defer sm.db.Close()

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}

	sm.handleActionMsg(cl, ActionMsg{
		Type:       "action",
		Action:     "favorite",
		SessionIDs: []string{"fav-1", "fav-2"},
	})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 2 {
		t.Fatalf("expected 2 favorites, got %d: %v", len(favs), favs)
	}
	if favs[0] != "fav-1" || favs[1] != "fav-2" {
		t.Errorf("order mismatch: got %v, want [fav-1 fav-2]", favs)
	}

	output := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte(`"type":"progress"`)) {
		t.Errorf("expected progress message in output, got %q", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"type":"result"`)) {
		t.Errorf("expected result message in output, got %q", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"action":"favorite"`)) {
		t.Errorf("expected favorite action in result, got %q", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"total":2`)) {
		t.Errorf("expected total=2 in result, got %q", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"succeeded":2`)) {
		t.Errorf("expected succeeded=2 in result, got %q", output)
	}
}

func TestHandleActionMsg_Unfavorite(t *testing.T) {
	sm := setupActionDB(t, nil)
	defer sm.db.Close()

	sm.mu.Lock()
	sm.favorites = append(sm.favorites, "A", "B", "C")
	sm.favoritesSet["A"] = true
	sm.favoritesSet["B"] = true
	sm.favoritesSet["C"] = true
	sm.mu.Unlock()

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}

	sm.handleActionMsg(cl, ActionMsg{
		Type:       "action",
		Action:     "unfavorite",
		SessionIDs: []string{"B"},
	})

	sm.mu.RLock()
	favs := make([]string, len(sm.favorites))
	copy(favs, sm.favorites)
	sm.mu.RUnlock()

	if len(favs) != 2 || favs[0] != "A" || favs[1] != "C" {
		t.Errorf("expected [A C], got %v", favs)
	}

	output := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte(`"type":"progress"`)) {
		t.Errorf("expected progress message, got %q", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"type":"result"`)) {
		t.Errorf("expected result message, got %q", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"action":"unfavorite"`)) {
		t.Errorf("expected unfavorite action, got %q", output)
	}
}

func TestHandleActionMsg_FavoriteIdempotent(t *testing.T) {
	sm := setupActionDB(t, nil)
	defer sm.db.Close()

	sm.handleActionMsg((&clientConn{enc: json.NewEncoder(&bytes.Buffer{})}),
		ActionMsg{Type: "action", Action: "favorite", SessionIDs: []string{"X"}})

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}
	sm.handleActionMsg(cl, ActionMsg{Type: "action", Action: "favorite", SessionIDs: []string{"X"}})

	sm.mu.RLock()
	favsLen := len(sm.favorites)
	sm.mu.RUnlock()

	if favsLen != 1 {
		t.Errorf("expected 1 favorite after double-add, got %d", favsLen)
	}

	if !bytes.Contains(buf.Bytes(), []byte(`"succeeded":1`)) {
		t.Errorf("expected succeeded=1 for idempotent favorite, got %q", buf.String())
	}
}

func TestHandleActionMsg_UnknownActionStillErrors(t *testing.T) {
	sm := setupActionDB(t, nil)
	defer sm.db.Close()

	buf := &bytes.Buffer{}
	cl := &clientConn{enc: json.NewEncoder(buf)}

	sm.handleActionMsg(cl, ActionMsg{
		Type:   "action",
		Action: "nope",
	})

	if !bytes.Contains(buf.Bytes(), []byte(`"error":"unknown action: nope"`)) {
		t.Errorf("expected unknown action error, got %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Favorites: concurrency smoke test (task 2.1)
// ---------------------------------------------------------------------------

func TestFavorites_ConcurrentFavoritesNoCorruption(t *testing.T) {
	sm := newStateManagerWithFavorites()
	const goroutines = 10
	const favoritesPer = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(offset int) {
			defer wg.Done()
			ids := make([]string, favoritesPer)
			for i := 0; i < favoritesPer; i++ {
				ids[i] = fmt.Sprintf("goroutine-%d-id-%d", offset, i)
			}
			sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: ids})
		}(g)
	}
	wg.Wait()

	sm.mu.RLock()
	totalFavs := len(sm.favorites)
	setSize := len(sm.favoritesSet)
	sm.mu.RUnlock()

	expected := goroutines * favoritesPer
	if totalFavs != expected {
		t.Errorf("expected %d favorites, got %d", expected, totalFavs)
	}
	if setSize != expected {
		t.Errorf("favoritesSet size mismatch: %d vs %d", setSize, expected)
	}
}

// TestFavorites_SummaryJSONRoundTrip verifies the manage.Summary produced by
// favorite serializes correctly via manageSummaryToSummary.
func TestFavorites_SummaryJSONRoundTrip(t *testing.T) {
	sm := newStateManagerWithFavorites()
	summary := sm.handleFavoriteAction(ActionMsg{Action: "favorite", SessionIDs: []string{"s1", "s2"}})

	daemonSummary := manageSummaryToSummary(summary)

	data, err := json.Marshal(daemonSummary)
	if err != nil {
		t.Fatalf("marshal daemon.Summary: %v", err)
	}

	var decoded Summary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal Summary: %v", err)
	}
	if decoded.Total != 2 {
		t.Errorf("Total = %d, want 2", decoded.Total)
	}
	if decoded.Succeeded != 2 {
		t.Errorf("Succeeded = %d, want 2", decoded.Succeeded)
	}
	if len(decoded.Results) != 2 {
		t.Errorf("Results len = %d, want 2", len(decoded.Results))
	}
}

// TestFavorites_InitialState verifies that a fresh StateManager has empty
// favorites after construction via both public constructors.
func TestFavorites_InitialState(t *testing.T) {
	sm := NewStateManagerWithSocket(nil, "")
	sm.mu.RLock()
	favsLen := len(sm.favorites)
	setLen := len(sm.favoritesSet)
	sm.mu.RUnlock()

	if favsLen != 0 {
		t.Errorf("NewStateManagerWithSocket: expected 0 favorites, got %d", favsLen)
	}
	if setLen != 0 {
		t.Errorf("NewStateManagerWithSocket: expected empty favoritesSet, got %d", setLen)
	}
	if sm.favoritesSet == nil {
		t.Error("NewStateManagerWithSocket: favoritesSet should be non-nil")
	}
	if sm.favorites == nil {
		t.Error("NewStateManagerWithSocket: favorites should be non-nil")
	}

	sm2 := NewStateManager(nil)
	sm2.mu.RLock()
	favsLen2 := len(sm2.favorites)
	setLen2 := len(sm2.favoritesSet)
	sm2.mu.RUnlock()

	if favsLen2 != 0 {
		t.Errorf("NewStateManager: expected 0 favorites, got %d", favsLen2)
	}
	if setLen2 != 0 {
		t.Errorf("NewStateManager: expected empty favoritesSet, got %d", setLen2)
	}
	if sm2.favoritesSet == nil {
		t.Error("NewStateManager: favoritesSet should be non-nil")
	}
}
