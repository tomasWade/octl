package views

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tomasWade/octl/internal/daemon"
	"github.com/tomasWade/octl/internal/types"
)

// testView returns a fixture ViewMsg with two projects and three sessions.
func testView() daemon.ViewMsg {
	return daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Name:      "project-one",
				Worktree:  "/home/user/project-one",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "Implement login", Status: daemon.StatusIdle, Directory: "/home/user/project-one"},
					{SessionID: "sess-b", Title: "Fix bug #42", Status: daemon.StatusBusy, Directory: "/home/user/project-one"},
				},
			},
			{
				ProjectID: "proj-2",
				Name:      "project-two",
				Worktree:  "/home/user/project-two",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-c", Title: "Refactor API", Status: daemon.StatusUnknown, Directory: "/home/user/project-two"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Before ViewLoadedMsg the dashboard stays unloaded and empty
// ---------------------------------------------------------------------------

func TestDashboard_BeforeViewLoadedMsg_StaysEmpty(t *testing.T) {
	m := NewDashboardModel(0)

	if m.loaded {
		t.Error("loaded should be false before ViewLoadedMsg")
	}
	if len(m.roots) != 0 {
		t.Errorf("roots = %v, want empty before ViewLoadedMsg", m.roots)
	}
	if len(m.visible) != 0 {
		t.Errorf("visible = %v, want empty before ViewLoadedMsg", m.visible)
	}

	// Unrelated messages should not build the tree either.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	if updated.loaded {
		t.Error("loaded should remain false after WindowSizeMsg")
	}
	if len(updated.roots) != 0 {
		t.Errorf("roots = %v, want empty after WindowSizeMsg", updated.roots)
	}
}

// ---------------------------------------------------------------------------
// Cursor reset when selectedID is empty (existing behaviour)
// ---------------------------------------------------------------------------

func TestRebuildTree_EmptySelectedID_CursorResetsToZero(t *testing.T) {
	m := DashboardModel{
		loaded:     true,
		selectedID: "",
		view:       testView(),
	}
	result := m.rebuildTree()

	if result.cursor != 0 {
		t.Errorf("cursor = %d, want 0", result.cursor)
	}
	if len(result.visible) != 2 {
		t.Fatalf("visible length = %d, want 2", len(result.visible))
	}
	if result.visible[0].Type != NodeProject || result.visible[0].Project.ID != "proj-1" {
		t.Errorf("visible[0] = %+v, want project proj-1", result.visible[0])
	}
}

// ---------------------------------------------------------------------------
// Cursor restore to project node
// ---------------------------------------------------------------------------

func TestRebuildTree_CursorRestoresToProjectNode(t *testing.T) {
	m := DashboardModel{
		loaded:     true,
		selectedID: "proj-2",
		view:       testView(),
	}
	result := m.rebuildTree()

	if result.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (index of proj-2)", result.cursor)
	}
	if result.visible[result.cursor].Type != NodeProject {
		t.Error("restored node type is not NodeProject")
	}
	if result.visible[result.cursor].Project.ID != "proj-2" {
		t.Errorf("restored Project.ID = %q, want proj-2", result.visible[result.cursor].Project.ID)
	}
}

func TestRebuildTree_CursorRestoresToFirstProjectNode(t *testing.T) {
	m := DashboardModel{
		loaded:     true,
		selectedID: "proj-1",
		view:       testView(),
	}
	result := m.rebuildTree()

	if result.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (index of proj-1)", result.cursor)
	}
	if result.visible[result.cursor].Project.ID != "proj-1" {
		t.Errorf("restored Project.ID = %q, want proj-1", result.visible[result.cursor].Project.ID)
	}
}

// ---------------------------------------------------------------------------
// Cursor restore to session node
// ---------------------------------------------------------------------------

func TestRebuildTree_CursorRestoresToSessionNode(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "Implement login", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-b", Title: "Fix bug #42", Status: daemon.StatusBusy, Directory: "/home/user/alpha"},
				},
			},
		},
	}

	m := DashboardModel{
		loaded:     true,
		selectedID: "sess-b",
		view:       view,
		roots: []*TreeNode{{
			Type:     NodeProject,
			Project:  types.Project{ID: "proj-1", Worktree: "/home/user/alpha", Vcs: "git"},
			Expanded: true,
			Children: []*TreeNode{
				{Type: NodeSession, Session: types.Session{ID: "sess-a"}, Depth: 1},
				{Type: NodeSession, Session: types.Session{ID: "sess-b"}, Depth: 1},
			},
		}},
	}
	result := m.rebuildTree()

	if result.cursor != 2 {
		t.Errorf("cursor = %d, want 2 (index of sess-b)", result.cursor)
	}
	if result.visible[result.cursor].Type != NodeSession {
		t.Error("restored node type is not NodeSession")
	}
	if result.visible[result.cursor].Session.ID != "sess-b" {
		t.Errorf("restored Session.ID = %q, want sess-b", result.visible[result.cursor].Session.ID)
	}
}

// ---------------------------------------------------------------------------
// Multiple projects and sessions — cursor restores correctly in mixed scenarios
// ---------------------------------------------------------------------------

func TestRebuildTree_MultipleProjectsAndSessions_CursorRestoresCorrectly(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-1", Title: "Alpha session", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
				},
			},
			{
				ProjectID: "proj-2",
				Worktree:  "/home/user/beta",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-2", Title: "Beta session 1", Status: daemon.StatusBusy, Directory: "/home/user/beta"},
					{SessionID: "sess-3", Title: "Beta session 2", Status: daemon.StatusUnknown, Directory: "/home/user/beta"},
				},
			},
		},
	}

	t.Run("project node in first position", func(t *testing.T) {
		m := DashboardModel{
			loaded:     true,
			selectedID: "proj-1",
			view:       view,
		}
		result := m.rebuildTree()
		if result.cursor != 0 {
			t.Errorf("cursor = %d, want 0", result.cursor)
		}
		if result.visible[result.cursor].Project.ID != "proj-1" {
			t.Errorf("restored Project.ID = %q, want proj-1", result.visible[result.cursor].Project.ID)
		}
	})

	t.Run("project node in second position", func(t *testing.T) {
		m := DashboardModel{
			loaded:     true,
			selectedID: "proj-2",
			view:       view,
		}
		result := m.rebuildTree()
		if result.cursor != 1 {
			t.Errorf("cursor = %d, want 1", result.cursor)
		}
		if result.visible[result.cursor].Project.ID != "proj-2" {
			t.Errorf("restored Project.ID = %q, want proj-2", result.visible[result.cursor].Project.ID)
		}
	})

	t.Run("session node after expanded project", func(t *testing.T) {
		m := DashboardModel{
			loaded:     true,
			selectedID: "sess-3",
			view:       view,
			roots: []*TreeNode{
				{
					Type:     NodeProject,
					Project:  types.Project{ID: "proj-1"},
					Expanded: false,
				},
				{
					Type:     NodeProject,
					Project:  types.Project{ID: "proj-2"},
					Expanded: true,
				},
			},
		}
		result := m.rebuildTree()
		if result.cursor != 3 {
			t.Errorf("cursor = %d, want 3", result.cursor)
		}
		if result.visible[result.cursor].Session.ID != "sess-3" {
			t.Errorf("restored Session.ID = %q, want sess-3", result.visible[result.cursor].Session.ID)
		}
	})

	t.Run("non-existent selectedID keeps cursor at 0", func(t *testing.T) {
		m := DashboardModel{
			loaded:     true,
			selectedID: "does-not-exist",
			view:       view,
		}
		result := m.rebuildTree()
		if result.cursor != 0 {
			t.Errorf("cursor = %d, want 0 (selectedID not found)", result.cursor)
		}
	})

	t.Run("session hidden under collapsed project keeps cursor at 0", func(t *testing.T) {
		m := DashboardModel{
			loaded:     true,
			selectedID: "sess-1",
			view:       view,
			roots: []*TreeNode{
				{
					Type:     NodeProject,
					Project:  types.Project{ID: "proj-1"},
					Expanded: false,
				},
				{
					Type:     NodeProject,
					Project:  types.Project{ID: "proj-2"},
					Expanded: true,
				},
			},
		}
		result := m.rebuildTree()
		if result.cursor != 0 {
			t.Errorf("cursor = %d, want 0 (sess-1 not visible)", result.cursor)
		}
	})
}

// ---------------------------------------------------------------------------
// Expanded state preservation during tree rebuild
// ---------------------------------------------------------------------------

func TestRebuildTree_PreservesExpandedState(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-1", Title: "Session 1", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-2", Title: "Session 2", Status: daemon.StatusBusy, Directory: "/home/user/alpha"},
				},
			},
			{
				ProjectID: "proj-2",
				Worktree:  "/home/user/beta",
			},
		},
	}

	m := DashboardModel{
		loaded:     true,
		selectedID: "",
		view:       view,
		roots: []*TreeNode{
			{
				Type:     NodeProject,
				Project:  types.Project{ID: "proj-1", Worktree: "/home/user/alpha", Vcs: "git"},
				Expanded: true,
				Children: []*TreeNode{
					{Type: NodeSession, Session: types.Session{ID: "sess-1"}, Depth: 1},
					{Type: NodeSession, Session: types.Session{ID: "sess-2"}, Depth: 1},
				},
			},
			{
				Type:     NodeProject,
				Project:  types.Project{ID: "proj-2", Worktree: "/home/user/beta", Vcs: "git"},
				Expanded: false,
			},
		},
	}
	result := m.rebuildTree()

	if len(result.visible) != 4 {
		t.Fatalf("visible length = %d, want 4 (proj-1, sess-1, sess-2, proj-2)", len(result.visible))
	}
	if !result.visible[0].Expanded {
		t.Error("proj-1 should be expanded")
	}
	if result.visible[1].Session.ID != "sess-1" {
		t.Errorf("visible[1] Session.ID = %q, want sess-1", result.visible[1].Session.ID)
	}
	if result.visible[2].Session.ID != "sess-2" {
		t.Errorf("visible[2] Session.ID = %q, want sess-2", result.visible[2].Session.ID)
	}
	if result.visible[3].Expanded {
		t.Error("proj-2 should NOT be expanded")
	}
}

// ---------------------------------------------------------------------------
// confirmView tests for project deletion
// ---------------------------------------------------------------------------

func TestConfirmView_EmptySessionsNoProject_ReturnsEmpty(t *testing.T) {
	m := DashboardModel{
		width:             120,
		height:            24,
		pendingSessionIDs: nil,
		pendingProjectID:  "",
	}
	result := m.confirmView()
	if result != "" {
		t.Errorf("confirmView() = %q, want empty string when both pendingSessionIDs and pendingProjectID are empty", result)
	}
}

func TestConfirmView_ProjectWithZeroSessions_ShowsDialog(t *testing.T) {
	m := DashboardModel{
		width:             120,
		height:            24,
		action:            "delete",
		pendingProjectID:  "proj-1",
		pendingSessionIDs: nil,
	}
	result := m.confirmView()
	if result == "" {
		t.Fatal("confirmView() returned empty for project with 0 sessions — expected a dialog")
	}
	want := "Are you sure you want to delete this project and its 0 sessions?"
	if !strings.Contains(result, want) {
		t.Errorf("confirmView() missing expected text %q\nGot:\n%s", want, result)
	}
}

func TestConfirmView_GlobalProject_WithSessions_ShowsCustomMessage(t *testing.T) {
	m := DashboardModel{
		width:            120,
		height:           24,
		action:           "delete",
		pendingProjectID: "global",
		pendingSessionIDs: []string{
			"sess-1",
		},
		roots: []*TreeNode{{
			Type:    NodeProject,
			Project: types.Project{ID: "global"},
			Children: []*TreeNode{{
				Type:    NodeSession,
				Session: types.Session{ID: "sess-1", Title: "Test Session"},
			}},
		}},
	}
	result := m.confirmView()
	if result == "" {
		t.Fatal("confirmView() returned empty for global project with sessions")
	}
	if !strings.Contains(result, "The global project itself cannot be deleted.") {
		t.Errorf("confirmView() missing 'cannot be deleted' message\nGot:\n%s", result)
	}
	if !strings.Contains(result, "Remove 1 session(s) from the global project?") {
		t.Errorf("confirmView() missing session count text\nGot:\n%s", result)
	}
}

// ---------------------------------------------------------------------------
// statusColor tests
// ---------------------------------------------------------------------------

func TestStatusColor_ReturnsCorrectColorForEachStatus(t *testing.T) {
	tests := []struct {
		status daemon.SessionStatus
		want   lipgloss.Color
	}{
		{daemon.StatusError, lipgloss.Color("196")},
		{daemon.StatusPermission, lipgloss.Color("220")},
		{daemon.StatusBusy, lipgloss.Color("45")},
		{daemon.StatusRetry, lipgloss.Color("208")},
		{daemon.StatusUnknown, lipgloss.Color("240")},
		{daemon.StatusArchived, lipgloss.Color("243")},
		{daemon.StatusIdle, lipgloss.Color("120")},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := statusColor(tc.status)
			if got != tc.want {
				t.Errorf("statusColor(%q) = %v, want %v", string(tc.status), got, tc.want)
			}
		})
	}
}

func TestStatusColor_UnknownStatus_ReturnsDefaultColor(t *testing.T) {
	got := statusColor(daemon.SessionStatus("MADE_UP"))
	want := lipgloss.Color("250")
	if got != want {
		t.Errorf("statusColor(unknown) = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Icon tests for SessionStatus
// ---------------------------------------------------------------------------

func TestStatusIcon_ReturnsCorrectStringForEachStatus(t *testing.T) {
	tests := []struct {
		status daemon.SessionStatus
		want   string
	}{
		{daemon.StatusPermission, "🟡 ASK"},
		{daemon.StatusBusy, "🔵 BUSY"},
		{daemon.StatusRetry, "🟠 RETRY"},
		{daemon.StatusError, "🔴 ERROR"},
		{daemon.StatusIdle, "⚪ IDLE"},
		{daemon.StatusUnknown, "◯ ???"},
		{daemon.StatusArchived, "  —"},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := tc.status.Icon()
			if got != tc.want {
				t.Errorf("Icon() for %q = %q, want %q", string(tc.status), got, tc.want)
			}
		})
	}
}

func TestStatusIcon_UnknownStatus_ReturnsDefaultIcon(t *testing.T) {
	got := daemon.SessionStatus("MADE_UP").Icon()
	want := "◯ ???"
	if got != want {
		t.Errorf("Icon() for unknown status = %q, want %q", got, want)
	}
}

func TestStatusIcon_EmptyStatus_ReturnsDefaultIcon(t *testing.T) {
	got := daemon.SessionStatus("").Icon()
	want := "◯ ???"
	if got != want {
		t.Errorf("Icon() for empty status = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// buildTreeFromView tests
// ---------------------------------------------------------------------------

func TestBuildTreeFromView_MapsFieldsCorrectly(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID:   "proj-x",
				Name:        "project-x",
				Worktree:    "/home/user/x",
				TimeUpdated: 1234,
				RowStatus:   daemon.StatusBusy,
				Sessions: []daemon.ViewSession{
					{
						SessionID:    "sess-x",
						Title:        "title-x",
						Directory:    "/home/user/x",
						Agent:        "claude",
						Cost:         0.42,
						TimeUpdated:  5678,
						TimeArchived: 0,
						Status:       daemon.StatusIdle,
						RowStatus:    daemon.StatusIdle,
						ParentID:     "",
						HasChildren:  false,
						Depth:        1,
					},
				},
			},
		},
	}

	roots := buildTreeFromView(view)
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d, want 1", len(roots))
	}
	proj := roots[0]
	if proj.Project.ID != "proj-x" {
		t.Errorf("Project.ID = %q, want proj-x", proj.Project.ID)
	}
	if proj.Project.Worktree != "/home/user/x" {
		t.Errorf("Project.Worktree = %q, want /home/user/x", proj.Project.Worktree)
	}
	if len(proj.Children) != 1 {
		t.Fatalf("len(proj.Children) = %d, want 1", len(proj.Children))
	}
	sess := proj.Children[0]
	if sess.Session.ID != "sess-x" {
		t.Errorf("Session.ID = %q, want sess-x", sess.Session.ID)
	}
	if sess.Session.Title != "title-x" {
		t.Errorf("Session.Title = %q, want title-x", sess.Session.Title)
	}
	if sess.Session.Agent != "claude" {
		t.Errorf("Session.Agent = %q, want claude", sess.Session.Agent)
	}
	if sess.Session.Cost != 0.42 {
		t.Errorf("Session.Cost = %v, want 0.42", sess.Session.Cost)
	}
	if sess.Status != daemon.StatusIdle {
		t.Errorf("Status = %q, want IDLE", sess.Status)
	}
	if sess.Depth != 1 {
		t.Errorf("Depth = %d, want 1", sess.Depth)
	}
}

func TestBuildTreeFromView_ParentChildRelationship(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "parent", Title: "Parent", Status: daemon.StatusIdle, Directory: "/home/user/alpha", HasChildren: true},
					{SessionID: "child", Title: "Child", Status: daemon.StatusBusy, Directory: "/home/user/alpha", ParentID: "parent", Depth: 1},
				},
			},
		},
	}

	roots := buildTreeFromView(view)
	if len(roots) != 1 || len(roots[0].Children) != 1 {
		t.Fatalf("unexpected tree shape: %+v", roots)
	}
	parent := roots[0].Children[0]
	if parent.Session.ID != "parent" {
		t.Errorf("parent ID = %q, want parent", parent.Session.ID)
	}
	if len(parent.Children) != 1 {
		t.Fatalf("parent has %d children, want 1", len(parent.Children))
	}
	child := parent.Children[0]
	if child.Session.ID != "child" {
		t.Errorf("child ID = %q, want child", child.Session.ID)
	}
	if child.Depth != 1 {
		t.Errorf("child Depth = %d, want 1", child.Depth)
	}
}

// ---------------------------------------------------------------------------
// Action request tests
// ---------------------------------------------------------------------------

func TestDashboard_DeleteKey_SendsActionRequest(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "One", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
				},
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = view
	m = m.rebuildTree()

	// Press d to delete the session under the cursor.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if updated.mode != modeConfirm {
		t.Fatalf("mode = %d, want modeConfirm", updated.mode)
	}
	if updated.action != "delete" {
		t.Errorf("action = %q, want delete", updated.action)
	}
	if len(updated.pendingSessionIDs) != 1 || updated.pendingSessionIDs[0] != "sess-a" {
		t.Errorf("pendingSessionIDs = %v, want [sess-a]", updated.pendingSessionIDs)
	}
	if cmd != nil {
		t.Error("delete key should not return a command until confirmed")
	}

	// Confirm with Enter.
	confirmed, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if confirmed.mode != modeProgress {
		t.Fatalf("mode = %d, want modeProgress after confirm", confirmed.mode)
	}
	if cmd == nil {
		t.Fatal("confirm delete should return a command")
	}
	msg := cmd()
	actionMsg, ok := msg.(ManageActionMsg)
	if !ok {
		t.Fatalf("command returned %T, want ManageActionMsg", msg)
	}
	if actionMsg.Action != "delete" {
		t.Errorf("action = %q, want delete", actionMsg.Action)
	}
	if len(actionMsg.SessionIDs) != 1 || actionMsg.SessionIDs[0] != "sess-a" {
		t.Errorf("sessionIDs = %v, want [sess-a]", actionMsg.SessionIDs)
	}
}

func TestDashboard_CreateKey_SendsActionRequest(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = view
	m = m.rebuildTree()

	// Press n on a project to start creating a session.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if updated.mode != modeInput {
		t.Fatalf("mode = %d, want modeInput", updated.mode)
	}
	if updated.inputDir != "/home/user/alpha" {
		t.Errorf("inputDir = %q, want /home/user/alpha", updated.inputDir)
	}

	// Type a message and confirm.
	updated.inputBuf = "hello"
	confirmed, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if confirmed.mode != modeProgress {
		t.Fatalf("mode = %d, want modeProgress after confirm", confirmed.mode)
	}
	if cmd == nil {
		t.Fatal("confirm create should return a command")
	}
	msg := cmd()
	actionMsg, ok := msg.(ManageActionMsg)
	if !ok {
		t.Fatalf("command returned %T, want ManageActionMsg", msg)
	}
	if actionMsg.Action != "create" {
		t.Errorf("action = %q, want create", actionMsg.Action)
	}
	if actionMsg.Directory != "/home/user/alpha" {
		t.Errorf("directory = %q, want /home/user/alpha", actionMsg.Directory)
	}
	if actionMsg.Message != "hello" {
		t.Errorf("message = %q, want hello", actionMsg.Message)
	}
}

func TestDashboard_MessageKey_SendsRequest(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "One", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
				},
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = view
	m = m.rebuildTree()
	// Expand the project so the session becomes visible and selectable.
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if cmd == nil {
		t.Fatal("m key should return a command")
	}
	msg := cmd()
	req, ok := msg.(MessageRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want MessageRequestMsg", msg)
	}
	if req.SessionID != "sess-a" {
		t.Errorf("sessionID = %q, want sess-a", req.SessionID)
	}
	if updated.mode != modeBrowse {
		t.Errorf("mode = %d, want modeBrowse while waiting for response", updated.mode)
	}
}

func TestDashboard_ProgressAndResultHandled(t *testing.T) {
	m := NewDashboardModel(0)
	m.mode = modeProgress
	m.action = "delete"

	progress, _ := m.Update(daemon.ProgressMsg{Type: "progress", Action: "delete", Done: false})
	if progress.mode != modeProgress {
		t.Errorf("mode = %d, want modeProgress after progress", progress.mode)
	}
	if progress.done {
		t.Error("done should be false after progress")
	}

	result, _ := m.Update(ManageResultMsg{Result: daemon.ResultMsg{
		Type:   "result",
		Action: "delete",
		Summary: daemon.Summary{
			Total:     2,
			Succeeded: 2,
			Failed:    0,
		},
	}})
	if result.mode != modeProgress {
		t.Errorf("mode = %d, want modeProgress after result", result.mode)
	}
	if !result.done {
		t.Error("done should be true after result")
	}
	if result.progress.Succeeded != 2 {
		t.Errorf("succeeded = %d, want 2", result.progress.Succeeded)
	}
}

func TestDashboard_ResponseMsg_LoadsMessages(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "One", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
				},
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = view
	m = m.rebuildTree()
	// Expand the project and move cursor to the session so m mode can load.
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1

	updated, _ := m.Update(daemon.ResponseMsg{
		Type: "response",
		Ok:   true,
		Messages: []daemon.MessagePart{
			{MessageID: "m1", Role: "user", Text: "hello", TimeCreated: 1000},
			{MessageID: "m2", Role: "assistant", Text: "world", TimeCreated: 2000},
		},
	})
	if updated.mode != modeMessage {
		t.Errorf("mode = %d, want modeMessage", updated.mode)
	}
	if len(updated.convMsgs) != 2 {
		t.Fatalf("convMsgs length = %d, want 2", len(updated.convMsgs))
	}
	if updated.convMsgs[0].text != "hello" {
		t.Errorf("convMsgs[0].text = %q, want hello", updated.convMsgs[0].text)
	}
	if updated.convMsgs[1].text != "world" {
		t.Errorf("convMsgs[1].text = %q, want world", updated.convMsgs[1].text)
	}
}

// ---------------------------------------------------------------------------
// Space multiselect tests
// ---------------------------------------------------------------------------

func TestDashboard_Space_TogglesSessionSelection(t *testing.T) {
	spaceMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}

	t.Run("plain session", func(t *testing.T) {
		m := NewDashboardModel(0)
		m.width = 100
		m.height = 24
		m.loaded = true
		m.view = testView()
		m = m.rebuildTree()

		// Expand the first project and move cursor to sess-a.
		m.roots[0].Expanded = true
		m.visible = flattenTree(m.roots)
		m.cursor = 1

		updated, _ := m.Update(spaceMsg)
		if !updated.selected["sess-a"] {
			t.Errorf("sess-a should be selected after Space")
		}
		if updated.cursor != 1 {
			t.Errorf("cursor = %d, want 1 (Space should not move cursor)", updated.cursor)
		}

		updated2, _ := updated.Update(spaceMsg)
		if updated2.selected["sess-a"] {
			t.Errorf("sess-a should be deselected after second Space")
		}
	})

	t.Run("session with children", func(t *testing.T) {
		view := daemon.ViewMsg{
			Type: "view",
			Projects: []daemon.ViewProject{
				{
					ProjectID: "proj-1",
					Worktree:  "/home/user/alpha",
					Sessions: []daemon.ViewSession{
						{SessionID: "parent", Title: "Parent", Status: daemon.StatusIdle, Directory: "/home/user/alpha", HasChildren: true},
						{SessionID: "child", Title: "Child", Status: daemon.StatusBusy, Directory: "/home/user/alpha", ParentID: "parent", Depth: 1},
					},
				},
			},
		}

		m := NewDashboardModel(0)
		m.width = 100
		m.height = 24
		m.loaded = true
		m.view = view
		m = m.rebuildTree()

		// Expand project and parent session so the child is visible.
		m.roots[0].Expanded = true
		m.roots[0].Children[0].Expanded = true
		m.visible = flattenTree(m.roots)
		m.cursor = 1

		updated, _ := m.Update(spaceMsg)
		if !updated.selected["parent"] || !updated.selected["child"] {
			t.Errorf("Space on parent session should select it and all subsessions, got %v", updated.selected)
		}
		if updated.cursor != 1 {
			t.Errorf("cursor = %d, want 1 (Space should not move cursor)", updated.cursor)
		}

		updated2, _ := updated.Update(spaceMsg)
		if updated2.selected["parent"] || updated2.selected["child"] {
			t.Errorf("second Space should deselect parent session and all subsessions, got %v", updated2.selected)
		}
	})
}

func TestDashboard_Space_ProjectSelectsAllDescendants(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()

	// Cursor on the first project.
	m.cursor = 0

	spaceMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}

	updated, _ := m.Update(spaceMsg)
	if !updated.selected["sess-a"] || !updated.selected["sess-b"] {
		t.Errorf("project selection should select all descendant sessions, got %v", updated.selected)
	}

	// Pressing Space again deselects all descendants.
	updated2, _ := updated.Update(spaceMsg)
	if updated2.selected["sess-a"] || updated2.selected["sess-b"] {
		t.Errorf("project selection should deselect all descendant sessions on second Space, got %v", updated2.selected)
	}
}

func TestDashboard_Space_EmptyProjectIsNoOp(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-empty",
				Name:      "empty-project",
				Worktree:  "/home/user/empty",
				Sessions:  []daemon.ViewSession{},
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = view
	m = m.rebuildTree()
	m.cursor = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if len(updated.selected) != 0 {
		t.Errorf("selected = %v, want empty for empty project", updated.selected)
	}
}

func TestDashboard_Space_DoesNotToggleExpand(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.cursor = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if updated.roots[0].Expanded {
		t.Error("Space on a project should not toggle expansion anymore")
	}
}

// ---------------------------------------------------------------------------
// d key batch delete with multiselect
// ---------------------------------------------------------------------------

func TestDashboard_DeleteKey_WithSelection_SendsBatchAction(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()

	// Expand both projects so all sessions are visible.
	m.roots[0].Expanded = true
	m.roots[1].Expanded = true
	m.visible = flattenTree(m.roots)

	// Select two sessions across different projects.
	m.selected = map[string]bool{
		"sess-a": true,
		"sess-c": true,
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if updated.mode != modeConfirm {
		t.Fatalf("mode = %d, want modeConfirm", updated.mode)
	}
	if updated.action != "delete" {
		t.Errorf("action = %q, want delete", updated.action)
	}
	if updated.pendingProjectID != "" {
		t.Errorf("pendingProjectID = %q, want empty for cross-project batch delete", updated.pendingProjectID)
	}
	wantIDs := []string{"sess-a", "sess-c"}
	if len(updated.pendingSessionIDs) != len(wantIDs) {
		t.Fatalf("pendingSessionIDs = %v, want %v", updated.pendingSessionIDs, wantIDs)
	}
	for i, id := range wantIDs {
		if updated.pendingSessionIDs[i] != id {
			t.Errorf("pendingSessionIDs[%d] = %q, want %q", i, updated.pendingSessionIDs[i], id)
		}
	}

	confirmed, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if confirmed.mode != modeProgress {
		t.Fatalf("mode = %d, want modeProgress after confirm", confirmed.mode)
	}
	if cmd == nil {
		t.Fatal("confirm batch delete should return a command")
	}
	msg := cmd()
	actionMsg, ok := msg.(ManageActionMsg)
	if !ok {
		t.Fatalf("command returned %T, want ManageActionMsg", msg)
	}
	if actionMsg.ProjectID != "" {
		t.Errorf("actionMsg.ProjectID = %q, want empty", actionMsg.ProjectID)
	}
	if len(actionMsg.SessionIDs) != 2 || actionMsg.SessionIDs[0] != "sess-a" || actionMsg.SessionIDs[1] != "sess-c" {
		t.Errorf("actionMsg.SessionIDs = %v, want [sess-a sess-c]", actionMsg.SessionIDs)
	}
}

func TestDashboard_DeleteKey_WithSelection_CollectsHiddenSelections(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()

	// Expand proj-1 so sess-a is visible; keep proj-2 collapsed so sess-c is hidden.
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)

	// Select one visible session and one hidden session.
	m.selected = map[string]bool{
		"sess-a": true,
		"sess-c": true,
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if updated.mode != modeConfirm {
		t.Fatalf("mode = %d, want modeConfirm", updated.mode)
	}
	if updated.action != "delete" {
		t.Errorf("action = %q, want delete", updated.action)
	}
	if updated.pendingProjectID != "" {
		t.Errorf("pendingProjectID = %q, want empty for cross-project batch delete", updated.pendingProjectID)
	}

	got := updated.pendingSessionIDs
	if len(got) != 2 {
		t.Fatalf("pendingSessionIDs = %v, want 2 IDs", got)
	}
	wantSet := map[string]bool{"sess-a": true, "sess-c": true}
	for _, id := range got {
		if !wantSet[id] {
			t.Errorf("unexpected pendingSessionID %q", id)
		}
	}
	// Visible selected sessions should appear in tree order before hidden selections.
	if got[0] != "sess-a" {
		t.Errorf("pendingSessionIDs[0] = %q, want sess-a (visible first)", got[0])
	}
}

func TestDashboard_ResultMsg_ClearsSuccessfulSelections(t *testing.T) {
	m := NewDashboardModel(0)
	m.selected = map[string]bool{
		"sess-x": true,
		"sess-y": true,
	}
	m.mode = modeProgress
	m.action = "delete"

	updated, _ := m.Update(ManageResultMsg{Result: daemon.ResultMsg{
		Type:   "result",
		Action: "delete",
		Summary: daemon.Summary{
			Total:     2,
			Succeeded: 1,
			Failed:    1,
			Results: []daemon.Result{
				{SessionID: "sess-x", Action: "delete", Success: true},
				{SessionID: "sess-y", Action: "delete", Success: false, Error: "boom"},
			},
		},
	}})
	if updated.selected["sess-x"] {
		t.Error("successful sess-x should be removed from selected")
	}
	if !updated.selected["sess-y"] {
		t.Error("failed sess-y should remain selected")
	}
}

func TestDashboard_RenderSessionRow_ShowsCheckmarkWhenSelected(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "One", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
				},
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = view
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.selected = map[string]bool{"sess-a": true}

	rendered := m.View()
	if !strings.Contains(rendered, "✓") {
		t.Errorf("rendered view should contain checkmark for selected session\nGot:\n%s", rendered)
	}
}

func TestDashboard_RenderSessionRow_CursorMarker(t *testing.T) {
	m := DashboardModel{width: 100}
	node := &TreeNode{
		Type:    NodeSession,
		Depth:   1,
		Session: types.Session{ID: "sess-a", Title: "One", Agent: "claude", Cost: 0.12, TimeUpdated: 1234},
	}
	m.selected = map[string]bool{"sess-a": true}

	const (
		colTitle   = 62
		colAgent   = 8
		colCost    = 8
		colUpdated = 10
		colStatus  = 8
	)

	tests := []struct {
		name            string
		isCursor        bool
		multiSelected   bool
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:            "selected without cursor",
			isCursor:        false,
			multiSelected:   true,
			wantContains:    []string{"✓"},
			wantNotContains: []string{cursorMarker},
		},
		{
			name:          "selected with cursor",
			isCursor:      true,
			multiSelected: true,
			wantContains:  []string{"✓", cursorMarker},
		},
		{
			name:            "cursor on unselected row",
			isCursor:        true,
			multiSelected:   false,
			wantContains:    []string{cursorMarker},
			wantNotContains: []string{"✓"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rendered := m.renderSessionRow(node, tc.isCursor, tc.multiSelected, colTitle, colAgent, colCost, colUpdated, colStatus)
			for _, s := range tc.wantContains {
				if !strings.Contains(rendered, s) {
					t.Errorf("rendered should contain %q\nGot:\n%s", s, rendered)
				}
			}
			for _, s := range tc.wantNotContains {
				if strings.Contains(rendered, s) {
					t.Errorf("rendered should NOT contain %q\nGot:\n%s", s, rendered)
				}
			}
		})
	}
}

func TestDashboard_RenderProjectRow_CursorMarker(t *testing.T) {
	m := DashboardModel{width: 100}
	node := &TreeNode{
		Type:      NodeProject,
		Project:   types.Project{ID: "proj-1", Worktree: "/home/user/alpha", Vcs: "git"},
		RowStatus: daemon.StatusIdle,
		Children: []*TreeNode{
			{Type: NodeSession, Session: types.Session{ID: "sess-a"}},
		},
	}
	m.selected = map[string]bool{"sess-a": true}

	const (
		colTitle   = 62
		colAgent   = 8
		colCost    = 8
		colUpdated = 10
		colStatus  = 8
	)

	rendered := m.renderProjectRow(node, true, true, colTitle, colAgent, colCost, colUpdated, colStatus)
	if !strings.Contains(rendered, "✓") {
		t.Errorf("rendered project row should contain checkmark for selected descendant\nGot:\n%s", rendered)
	}
	if !strings.Contains(rendered, cursorMarker) {
		t.Errorf("rendered project row should contain cursor marker when cursor is on it\nGot:\n%s", rendered)
	}
}

// ---------------------------------------------------------------------------
// Favorite marker + child count in session rows
// ---------------------------------------------------------------------------

func TestDashboard_RenderSessionRow_ShowsFavoriteMarker(t *testing.T) {
	m := DashboardModel{width: 100}
	m.favorites = map[string]bool{"sess-a": true}
	node := &TreeNode{
		Type:    NodeSession,
		Depth:   1,
		Session: types.Session{ID: "sess-a", Title: "One", Agent: "claude", Cost: 0.12, TimeUpdated: 1234},
	}

	const (
		colTitle   = 62
		colAgent   = 8
		colCost    = 8
		colUpdated = 10
		colStatus  = 8
	)

	rendered := m.renderSessionRow(node, false, false, colTitle, colAgent, colCost, colUpdated, colStatus)
	if !strings.Contains(rendered, "⭐") {
		t.Errorf("favorited session row should contain ⭐ in the status column\nGot:\n%s", rendered)
	}
	if !strings.Contains(rendered, " ⭐") {
		t.Errorf("favorited session row should put a space between the status icon and ⭐\nGot:\n%s", rendered)
	}
	if !strings.Contains(rendered, "One") {
		t.Errorf("favorited session row should still contain the title\nGot:\n%s", rendered)
	}
	if strings.Contains(rendered, "⭐ One") {
		t.Errorf("⭐ should NOT be prefixed to the title anymore (moved to status column)\nGot:\n%s", rendered)
	}
}

func TestDashboard_RenderSessionRow_NonFavorite_NoStar(t *testing.T) {
	m := DashboardModel{width: 100} // favorites 为空
	node := &TreeNode{
		Type:    NodeSession,
		Depth:   1,
		Session: types.Session{ID: "sess-a", Title: "One", Agent: "claude", Cost: 0.12, TimeUpdated: 1234},
	}

	const (
		colTitle   = 62
		colAgent   = 8
		colCost    = 8
		colUpdated = 10
		colStatus  = 8
	)

	rendered := m.renderSessionRow(node, false, false, colTitle, colAgent, colCost, colUpdated, colStatus)
	if strings.Contains(rendered, "⭐") {
		t.Errorf("non-favorited session row should NOT contain ⭐\nGot:\n%s", rendered)
	}
}

func TestDashboard_RenderSessionRow_ShowsChildCount(t *testing.T) {
	m := DashboardModel{width: 100}
	node := &TreeNode{
		Type:    NodeSession,
		Depth:   1,
		Session: types.Session{ID: "parent", Title: "Parent", Agent: "claude"},
		Children: []*TreeNode{
			{Type: NodeSession, Session: types.Session{ID: "child-1"}},
			{Type: NodeSession, Session: types.Session{ID: "child-2"}},
			{Type: NodeSession, Session: types.Session{ID: "child-3"}},
		},
	}

	const (
		colTitle   = 62
		colAgent   = 8
		colCost    = 8
		colUpdated = 10
		colStatus  = 8
	)

	rendered := m.renderSessionRow(node, false, false, colTitle, colAgent, colCost, colUpdated, colStatus)
	if !strings.Contains(rendered, "Parent (3)") {
		t.Errorf("session row with 3 children should contain 'Parent (3)'\nGot:\n%s", rendered)
	}
}

func TestDashboard_RenderSessionRow_LeafNode_NoChildCount(t *testing.T) {
	m := DashboardModel{width: 100}
	node := &TreeNode{
		Type:    NodeSession,
		Depth:   1,
		Session: types.Session{ID: "leaf", Title: "Leaf", Agent: "claude"},
	}

	const (
		colTitle   = 62
		colAgent   = 8
		colCost    = 8
		colUpdated = 10
		colStatus  = 8
	)

	rendered := m.renderSessionRow(node, false, false, colTitle, colAgent, colCost, colUpdated, colStatus)
	if strings.Contains(rendered, "(") {
		t.Errorf("leaf session row should NOT contain a child count\nGot:\n%s", rendered)
	}
}

func TestDashboard_RenderSessionRow_FavoriteWithChildren_ShowsBoth(t *testing.T) {
	m := DashboardModel{width: 100}
	m.favorites = map[string]bool{"parent": true}
	node := &TreeNode{
		Type:    NodeSession,
		Depth:   1,
		Session: types.Session{ID: "parent", Title: "Parent", Agent: "claude"},
		Children: []*TreeNode{
			{Type: NodeSession, Session: types.Session{ID: "child-1"}},
			{Type: NodeSession, Session: types.Session{ID: "child-2"}},
		},
	}

	const (
		colTitle   = 62
		colAgent   = 8
		colCost    = 8
		colUpdated = 10
		colStatus  = 8
	)

	rendered := m.renderSessionRow(node, false, false, colTitle, colAgent, colCost, colUpdated, colStatus)
	if !strings.Contains(rendered, "Parent (2)") {
		t.Errorf("favorited session with 2 children should contain 'Parent (2)'\nGot:\n%s", rendered)
	}
	if !strings.Contains(rendered, "⭐") {
		t.Errorf("favorited session with 2 children should contain ⭐ in the status column\nGot:\n%s", rendered)
	}
	if strings.Contains(rendered, "⭐ Parent") {
		t.Errorf("⭐ should NOT be prefixed to the title (moved to status column)\nGot:\n%s", rendered)
	}
}

// ---------------------------------------------------------------------------
// Tree scrolling tests
// ---------------------------------------------------------------------------

func TestEnsureCursorVisible_ScrollsUpWhenCursorAboveViewport(t *testing.T) {
	m := DashboardModel{width: 100, height: 10, scrollOffset: 5, cursor: 2}
	m.ensureCursorVisible()
	if m.scrollOffset != 2 {
		t.Errorf("scrollOffset = %d, want 2", m.scrollOffset)
	}
}

func TestEnsureCursorVisible_ScrollsDownWhenCursorBelowViewport(t *testing.T) {
	m := DashboardModel{width: 100, height: 10, scrollOffset: 0, cursor: 8}
	m.ensureCursorVisible()
	// treeHeight = 10 - 4 = 6; cursor 8 >= 0+6, so scrollOffset = 8 - 6 + 1 = 3
	if m.scrollOffset != 3 {
		t.Errorf("scrollOffset = %d, want 3", m.scrollOffset)
	}
}

func TestEnsureCursorVisible_KeepsCursorInsideViewport(t *testing.T) {
	m := DashboardModel{width: 100, height: 10, scrollOffset: 2, cursor: 5}
	m.ensureCursorVisible()
	if m.scrollOffset != 2 {
		t.Errorf("scrollOffset = %d, want 2", m.scrollOffset)
	}
}

func TestEnsureCursorVisible_ClampsToZero(t *testing.T) {
	m := DashboardModel{width: 100, height: 10, scrollOffset: -3, cursor: 0}
	m.ensureCursorVisible()
	if m.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0", m.scrollOffset)
	}
}

func TestClampScrollOffset_AfterVisibleShrinks(t *testing.T) {
	m := DashboardModel{width: 100, height: 10, scrollOffset: 10, visible: make([]*TreeNode, 5)}
	m.clampScrollOffset()
	// treeHeight = 6, maxOffset = max(0, 5-6) = 0
	if m.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0", m.scrollOffset)
	}
}

func TestRebuildTree_ClampScrollOffset(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-a", Title: "One", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
				},
			},
		},
	}
	m := DashboardModel{
		loaded:       true,
		width:        100,
		height:       10,
		scrollOffset: 5,
		view:         view,
	}
	result := m.rebuildTree()
	if result.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 after rebuildTree clamp", result.scrollOffset)
	}
}

func TestManageResultMsg_ClampScrollOffset(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 10
	m.mode = modeProgress
	m.action = "delete"
	m.scrollOffset = 5
	m.visible = make([]*TreeNode, 3)

	updated, _ := m.Update(ManageResultMsg{Result: daemon.ResultMsg{
		Type:   "result",
		Action: "delete",
		Summary: daemon.Summary{
			Total:     0,
			Succeeded: 0,
			Failed:    0,
		},
	}})
	if updated.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 after ManageResultMsg clamp", updated.scrollOffset)
	}
}

func TestTreeScrollBar_ShortTreeHasNoScrollbar(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 10
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()

	rendered := m.View()
	if strings.Contains(rendered, "█") || strings.Contains(rendered, "░") {
		t.Errorf("short tree should not render scrollbar\nGot:\n%s", rendered)
	}
}

func TestTreeScrollBar_LongTreeRendersScrollbar(t *testing.T) {
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions: []daemon.ViewSession{
					{SessionID: "sess-1", Title: "One", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-2", Title: "Two", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-3", Title: "Three", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-4", Title: "Four", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-5", Title: "Five", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-6", Title: "Six", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-7", Title: "Seven", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
					{SessionID: "sess-8", Title: "Eight", Status: daemon.StatusIdle, Directory: "/home/user/alpha"},
				},
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 10 // treeHeight = 6, visible = 9 (project + 8 sessions)
	m.loaded = true
	m.view = view
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)

	rendered := m.View()
	if !strings.Contains(rendered, "█") && !strings.Contains(rendered, "░") {
		t.Errorf("long tree should render scrollbar\nGot:\n%s", rendered)
	}
}

func TestTreeScrollBar_WindowRendersOnlyVisibleRows(t *testing.T) {
	sessions := make([]daemon.ViewSession, 10)
	for i := range sessions {
		sessions[i] = daemon.ViewSession{
			SessionID: fmt.Sprintf("sess-%02d", i),
			Title:     fmt.Sprintf("Session %d", i),
			Status:    daemon.StatusIdle,
			Directory: "/home/user/alpha",
		}
	}
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions:  sessions,
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 10 // treeHeight = 6, visible = 11
	m.loaded = true
	m.view = view
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.scrollOffset = 5

	rendered := m.View()
	// 窗口为 [5, 11)，应包含 Session 4 到 Session 9，不应包含 Session 0 到 Session 3
	for _, title := range []string{"Session 0", "Session 1", "Session 2", "Session 3"} {
		if strings.Contains(rendered, title) {
			t.Errorf("windowed view should not contain %s\nGot:\n%s", title, rendered)
		}
	}
	for _, title := range []string{"Session 4", "Session 5", "Session 6", "Session 7", "Session 8", "Session 9"} {
		if !strings.Contains(rendered, title) {
			t.Errorf("windowed view should contain %s\nGot:\n%s", title, rendered)
		}
	}
}

func TestDashboard_NavDown_ScrollsViewport(t *testing.T) {
	sessions := make([]daemon.ViewSession, 10)
	for i := range sessions {
		sessions[i] = daemon.ViewSession{
			SessionID: fmt.Sprintf("sess-%02d", i),
			Title:     fmt.Sprintf("Session %d", i),
			Status:    daemon.StatusIdle,
			Directory: "/home/user/alpha",
		}
	}
	view := daemon.ViewMsg{
		Type: "view",
		Projects: []daemon.ViewProject{
			{
				ProjectID: "proj-1",
				Worktree:  "/home/user/alpha",
				Sessions:  sessions,
			},
		},
	}
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 10 // treeHeight = 6
	m.loaded = true
	m.view = view
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	// 光标在可视窗口最后一个位置（index 6，即第 6 行 sess-05）
	m.cursor = 6

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if updated.cursor != 7 {
		t.Errorf("cursor = %d, want 7", updated.cursor)
	}
	// 光标超出 viewport，scrollOffset 应滚动到 2
	if updated.scrollOffset != 2 {
		t.Errorf("scrollOffset = %d, want 2", updated.scrollOffset)
	}
}

func TestBuildTreeScrollBar_ThumbWithinTrack(t *testing.T) {
	// totalRows=20, treeHeight=10, start=5
	// thumbStart = 5*10/20 = 2, thumbEnd = 15*10/20 = 7
	glyphs := buildTreeScrollBar(10, 20, 5)
	if len(glyphs) != 10 {
		t.Fatalf("len(glyphs) = %d, want 10", len(glyphs))
	}
	for i := 2; i < 7; i++ {
		if glyphs[i] != "█" {
			t.Errorf("glyphs[%d] = %q, want █", i, glyphs[i])
		}
	}
	for _, i := range []int{0, 1, 7, 8, 9} {
		if glyphs[i] != "░" {
			t.Errorf("glyphs[%d] = %q, want ░", i, glyphs[i])
		}
	}
}

// ============================================================================
// f key favorites tests (FR-009 / task 1.7)
// ============================================================================

// fKeyFn creates a tea.KeyMsg for the 'f' key (lowercase).
func fKeyFn() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}
}

// TestDashboard_FKey_WithMultiSelect_SendsFavoritesToggleRequestMsg verifies
// that pressing f with Space multi-selected sessions collects ALL selected
// session IDs (not just the cursor one) into a FavoritesToggleRequestMsg.
func TestDashboard_FKey_WithMultiSelect_SendsFavoritesToggleRequestMsg(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1
	m.selected = map[string]bool{
		"sess-a": true,
		"sess-c": true,
	}

	updated, cmd := m.Update(fKeyFn())
	if cmd == nil {
		t.Fatal("f key with multi-select should return a command")
	}

	msg := cmd()
	toggleMsg, ok := msg.(FavoritesToggleRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want FavoritesToggleRequestMsg", msg)
	}

	got := toggleMsg.SessionIDs
	if len(got) != 2 {
		t.Fatalf("SessionIDs = %v, want 2 entries", got)
	}
	gotSet := make(map[string]bool)
	for _, id := range got {
		gotSet[id] = true
	}
	if !gotSet["sess-a"] {
		t.Error("SessionIDs missing sess-a")
	}
	if !gotSet["sess-c"] {
		t.Error("SessionIDs missing sess-c (cross-project)")
	}

	// f 键不再设置文字反馈（收藏状态由第一列 ★ 呈现）。
	if updated.feedback != "" {
		t.Errorf("feedback = %q, want empty (no text feedback for favorite)", updated.feedback)
	}
}

// TestDashboard_FKey_OnSessionNode_NoMultiSelect_SendsFavoritesToggleRequestMsg
func TestDashboard_FKey_OnSessionNode_NoMultiSelect_SendsFavoritesToggleRequestMsg(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 2
	m.selected = nil

	updated, cmd := m.Update(fKeyFn())
	if cmd == nil {
		t.Fatal("f key on session node should return a command")
	}

	msg := cmd()
	toggleMsg, ok := msg.(FavoritesToggleRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want FavoritesToggleRequestMsg", msg)
	}

	if len(toggleMsg.SessionIDs) != 1 {
		t.Fatalf("SessionIDs = %v, want [sess-b]", toggleMsg.SessionIDs)
	}
	if toggleMsg.SessionIDs[0] != "sess-b" {
		t.Errorf("SessionIDs[0] = %q, want sess-b", toggleMsg.SessionIDs[0])
	}

	// f 键不再设置文字反馈（收藏状态由第一列 ★ 呈现）。
	if updated.feedback != "" {
		t.Errorf("feedback = %q, want empty (no text feedback for favorite)", updated.feedback)
	}
}

// TestDashboard_FKey_OnProjectNode_NoMultiSelect_NoOp
func TestDashboard_FKey_OnProjectNode_NoMultiSelect_NoOp(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.cursor = 0
	m.selected = nil

	updated, cmd := m.Update(fKeyFn())
	if cmd != nil {
		t.Errorf("f key on project node (no multi-select) should return nil cmd, got %T", cmd)
	}
	if updated.feedback != "" {
		t.Errorf("f key on project node should not set feedback, got %q", updated.feedback)
	}
}

// TestDashboard_FKey_EmptySelection_OnProject_NoOp
func TestDashboard_FKey_EmptySelection_OnProject_NoOp(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.cursor = 0
	m.selected = make(map[string]bool)

	updated, cmd := m.Update(fKeyFn())
	if cmd != nil {
		t.Errorf("f key with empty selected on project should return nil cmd, got %T", cmd)
	}
	if updated.feedback != "" {
		t.Errorf("f key with empty selected should not set feedback, got %q", updated.feedback)
	}
}

// TestDashboard_FKey_WithMultiSelect_PreservesCursorAfterFavorites
func TestDashboard_FKey_WithMultiSelect_PreservesCursorAfterFavorites(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1
	m.selected = map[string]bool{"sess-a": true, "sess-b": true}

	updated, _ := m.Update(fKeyFn())
	if updated.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (f key should not move cursor)", updated.cursor)
	}
}

// TestDashboard_FKey_UpperCaseF_AlsoWorks
func TestDashboard_FKey_UpperCaseF_AlsoWorks(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1
	m.selected = nil

	capitalF := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")}
	_, cmd := m.Update(capitalF)
	if cmd == nil {
		t.Fatal("capital F key on session node should return a command")
	}

	msg := cmd()
	toggleMsg, ok := msg.(FavoritesToggleRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want FavoritesToggleRequestMsg", msg)
	}
	if len(toggleMsg.SessionIDs) != 1 || toggleMsg.SessionIDs[0] != "sess-a" {
		t.Errorf("SessionIDs = %v, want [sess-a]", toggleMsg.SessionIDs)
	}
}

// ============================================================================
// FavoritesChangedMsg handling tests (FR-013)
// ============================================================================

// TestDashboard_FavoritesChangedMsg_StoresMap
func TestDashboard_FavoritesChangedMsg_StoresMap(t *testing.T) {
	m := NewDashboardModel(0)
	favs := map[string]bool{"sess-a": true, "sess-c": true}

	updated, _ := m.Update(FavoritesChangedMsg{Favorites: favs})

	if len(updated.favorites) != 2 {
		t.Fatalf("favorites = %v, want 2 entries", updated.favorites)
	}
	if !updated.favorites["sess-a"] {
		t.Error("favorites missing sess-a")
	}
	if !updated.favorites["sess-c"] {
		t.Error("favorites missing sess-c")
	}
}

// TestDashboard_FavoritesChangedMsg_OverwritesPrevious
func TestDashboard_FavoritesChangedMsg_OverwritesPrevious(t *testing.T) {
	m := NewDashboardModel(0)
	m.favorites = map[string]bool{"old-sess": true}

	favs := map[string]bool{"new-sess": true}
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: favs})

	if updated.favorites["old-sess"] {
		t.Error("old-sess should NOT be in favorites after overwrite")
	}
	if len(updated.favorites) != 1 {
		t.Fatalf("favorites = %v, want exactly 1 entry", updated.favorites)
	}
	if !updated.favorites["new-sess"] {
		t.Error("favorites missing new-sess")
	}
}

// TestDashboard_FavoritesChangedMsg_EmptyMapClearsFavorites
func TestDashboard_FavoritesChangedMsg_EmptyMapClearsFavorites(t *testing.T) {
	m := NewDashboardModel(0)
	m.favorites = map[string]bool{"sess-a": true, "sess-b": true}

	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{}})

	if len(updated.favorites) != 0 {
		t.Errorf("favorites = %v, want empty after empty FavoritesChangedMsg", updated.favorites)
	}
}

// TestDashboard_FavoritesChangedMsg_SurvivesRebuildTree
func TestDashboard_FavoritesChangedMsg_SurvivesRebuildTree(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()

	favs := map[string]bool{"sess-a": true}
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: favs})

	if !updated.favorites["sess-a"] {
		t.Fatal("sess-a should be in favorites before rebuild")
	}

	updated, _ = updated.Update(ViewLoadedMsg{View: testView()})

	if len(updated.favorites) != 1 {
		t.Fatalf("favorites = %v, want 1 entry after rebuildTree", updated.favorites)
	}
	if !updated.favorites["sess-a"] {
		t.Error("sess-a should remain in favorites after rebuildTree")
	}
}

// TestDashboard_FavoritesChangedMsg_SurvivesViewLoadedMsg
func TestDashboard_FavoritesChangedMsg_SurvivesViewLoadedMsg(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.favorites = map[string]bool{"sess-b": true}

	updated, _ := m.Update(ViewLoadedMsg{View: testView()})

	if !updated.favorites["sess-b"] {
		t.Error("sess-b should remain in favorites after ViewLoadedMsg")
	}
	if len(updated.favorites) != 1 {
		t.Errorf("favorites = %v, want 1 entry", updated.favorites)
	}
}

// TestDashboard_FavoritesChangedMsg_DoesNotEmitFavoritesToggleRequestMsg
func TestDashboard_FavoritesChangedMsg_DoesNotEmitFavoritesToggleRequestMsg(t *testing.T) {
	m := NewDashboardModel(0)
	favs := map[string]bool{"sess-a": true}

	_, cmd := m.Update(FavoritesChangedMsg{Favorites: favs})
	if cmd != nil {
		t.Errorf("FavoritesChangedMsg handler should return nil cmd (no re-emission), got %T", cmd)
	}
}

// TestDashboard_FavoritesChangedMsg_NilMap_NoPanic
func TestDashboard_FavoritesChangedMsg_NilMap_NoPanic(t *testing.T) {
	m := NewDashboardModel(0)

	updated, _ := m.Update(FavoritesChangedMsg{Favorites: nil})

	if updated.favorites != nil {
		t.Errorf("favorites = %v, want nil", updated.favorites)
	}
}

// ============================================================================
// Mode gating: f key must NOT fire in input/confirm/progress modes
// ============================================================================

func TestDashboard_FKey_NotFiredInConfirmMode(t *testing.T) {
	m := NewDashboardModel(0)
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.selected = map[string]bool{"sess-a": true}
	m.mode = modeConfirm
	m.action = "delete"
	m.pendingSessionIDs = []string{"sess-a"}

	updated, cmd := m.Update(fKeyFn())
	if cmd != nil {
		t.Errorf("f key in modeConfirm should return nil cmd, got %T", cmd)
	}
	if updated.mode != modeConfirm {
		t.Errorf("mode = %d, want modeConfirm (unchanged)", updated.mode)
	}
}

func TestDashboard_FKey_NotFiredInInputMode(t *testing.T) {
	m := NewDashboardModel(0)
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1
	m.selected = map[string]bool{"sess-a": true}
	m.mode = modeInput
	m.inputBuf = "hello"

	updated, cmd := m.Update(fKeyFn())
	if cmd != nil {
		t.Errorf("f key in modeInput should return nil cmd, got %T", cmd)
	}
	if updated.mode != modeInput {
		t.Errorf("mode = %d, want modeInput (unchanged)", updated.mode)
	}
}

func TestDashboard_FKey_NotFiredInProgressMode(t *testing.T) {
	m := NewDashboardModel(0)
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.mode = modeProgress
	m.selected = map[string]bool{"sess-a": true}

	updated, cmd := m.Update(fKeyFn())
	if cmd != nil {
		t.Errorf("f key in modeProgress should return nil cmd, got %T", cmd)
	}
	// modeProgress dismisses to modeBrowse on any key press.
	if updated.mode != modeBrowse {
		t.Errorf("mode = %d, want modeBrowse (progress dialog dismissed by any key)", updated.mode)
	}
}

func TestDashboard_FKey_NotFiredNotLoaded(t *testing.T) {
	m := NewDashboardModel(0)
	m.height = 24
	m.selected = map[string]bool{"sess-a": true}

	updated, cmd := m.Update(fKeyFn())
	if cmd != nil {
		t.Errorf("f key when not loaded should return nil cmd, got %T", cmd)
	}
	if updated.loaded {
		t.Error("loaded should remain false")
	}
}

// TestDashboard_FKey_EmptyVisibleNodes_NoOp
func TestDashboard_FKey_EmptyVisibleNodes_NoOp(t *testing.T) {
	m := NewDashboardModel(0)
	m.height = 24
	m.loaded = true
	m.visible = nil
	m.selected = map[string]bool{"sess-a": true}

	updated, cmd := m.Update(fKeyFn())
	if cmd != nil {
		t.Errorf("f key with empty visible nodes should return nil cmd, got %T", cmd)
	}
	if updated.mode != modeBrowse {
		t.Errorf("mode = %d, want modeBrowse", updated.mode)
	}
}

// ============================================================================
// Integration: Favorites + Multi-select + Tree rebuild round-trip
// ============================================================================

// TestDashboard_FKey_ThenRebuildTree_FavoritesMapPreserved
func TestDashboard_FKey_ThenRebuildTree_FavoritesMapPreserved(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1
	m.selected = nil

	_, cmd := m.Update(fKeyFn())
	if cmd == nil {
		t.Fatal("f key should return a command")
	}

	favs := map[string]bool{"sess-a": true}
	updated, _ := m.Update(FavoritesChangedMsg{Favorites: favs})
	updated, _ = updated.Update(ViewLoadedMsg{View: testView()})

	if !updated.favorites["sess-a"] {
		t.Error("sess-a should remain in favorites after ViewLoadedMsg rebuild")
	}
}

// TestDashboard_FKey_ThenFavoritesChangedMsg_FeedbackCleared
func TestDashboard_FKey_ThenFavoritesChangedMsg_FeedbackCleared(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1
	m.selected = nil

	_, _ = m.Update(fKeyFn())

	updated, _ := m.Update(FavoritesChangedMsg{Favorites: map[string]bool{"sess-b": true}})

	if updated.favorites["sess-a"] {
		t.Error("sess-a should NOT be in favorites — FavoritesChangedMsg replaced the map")
	}
	if !updated.favorites["sess-b"] {
		t.Error("sess-b should be in favorites after FavoritesChangedMsg update")
	}
}

// TestDashboard_FKey_SingleSessionWithMultiSelect_CollectsThatSession
func TestDashboard_FKey_SingleSessionWithMultiSelect_CollectsThatSession(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 1
	m.selected = map[string]bool{"sess-b": true}

	updated, cmd := m.Update(fKeyFn())
	if cmd == nil {
		t.Fatal("f key with single-selected session should return a command")
	}

	msg := cmd()
	toggleMsg, ok := msg.(FavoritesToggleRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want FavoritesToggleRequestMsg", msg)
	}

	if len(toggleMsg.SessionIDs) != 1 || toggleMsg.SessionIDs[0] != "sess-b" {
		t.Errorf("SessionIDs = %v, want [sess-b]", toggleMsg.SessionIDs)
	}
	// f 键不再设置文字反馈（收藏状态由第一列 ★ 呈现）。
	if updated.feedback != "" {
		t.Errorf("feedback = %q, want empty (no text feedback for favorite)", updated.feedback)
	}
}

// TestDashboard_FKey_LargeMultiSelect_AllCollected
func TestDashboard_FKey_LargeMultiSelect_AllCollected(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)

	m.selected = make(map[string]bool)
	manyIDs := make(map[string]bool)
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("sess-%d", i)
		m.selected[id] = true
		manyIDs[id] = true
	}

	_, cmd := m.Update(fKeyFn())
	if cmd == nil {
		t.Fatal("f key with many selected sessions should return a command")
	}

	msg := cmd()
	toggleMsg, ok := msg.(FavoritesToggleRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want FavoritesToggleRequestMsg", msg)
	}

	if len(toggleMsg.SessionIDs) != 50 {
		t.Fatalf("SessionIDs length = %d, want 50", len(toggleMsg.SessionIDs))
	}
	for _, id := range toggleMsg.SessionIDs {
		if !manyIDs[id] {
			t.Errorf("SessionIDs contains unexpected ID %q", id)
		}
	}
}

// TestDashboard_FKey_MultiSelectOnProjectNode_CollectsSelectedNotCursor
func TestDashboard_FKey_MultiSelectOnProjectNode_CollectsSelectedNotCursor(t *testing.T) {
	m := NewDashboardModel(0)
	m.width = 100
	m.height = 24
	m.loaded = true
	m.view = testView()
	m = m.rebuildTree()
	m.roots[0].Expanded = true
	m.visible = flattenTree(m.roots)
	m.cursor = 0
	m.selected = map[string]bool{"sess-b": true}

	_, cmd := m.Update(fKeyFn())
	if cmd == nil {
		t.Fatal("f key with multi-select should return a command even if cursor is on project")
	}

	msg := cmd()
	toggleMsg, ok := msg.(FavoritesToggleRequestMsg)
	if !ok {
		t.Fatalf("command returned %T, want FavoritesToggleRequestMsg", msg)
	}
	if len(toggleMsg.SessionIDs) != 1 || toggleMsg.SessionIDs[0] != "sess-b" {
		t.Errorf("SessionIDs = %v, want [sess-b]", toggleMsg.SessionIDs)
	}
}
