package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tomasWade/octl/internal/types"
)

// ---------------------------------------------------------------------------
// 1-line scroll for modeMessage
//
// j / down  => msgScroll += 1
// k / up    => msgScroll -= 1 (no guard — can go negative, display clamps)
// h / left  => msgIndex--, msgScroll = 0
// l / right => msgIndex++, msgScroll = 0
// ---------------------------------------------------------------------------

func TestMsgScroll_J_IncrementsByOne(t *testing.T) {
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 0,
	}
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if result.msgScroll != 1 {
		t.Errorf("after j: msgScroll = %d, want 1", result.msgScroll)
	}
}

func TestMsgScroll_K_DecrementsByOne(t *testing.T) {
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 10,
	}
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if result.msgScroll != 9 {
		t.Errorf("after k: msgScroll = %d, want 9", result.msgScroll)
	}
}

func TestMsgScroll_KAtZero_GoesNegative(t *testing.T) {
	// k at top: msgScroll goes to -1. The display layer clamps
	// the visible range start to 0 so there is no crash; the scroll
	// position bar shows "At top" as feedback.
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 0,
	}
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if result.msgScroll != -1 {
		t.Errorf("after k at 0: msgScroll = %d, want -1", result.msgScroll)
	}
}

func TestMsgScroll_RepeatedK_StaysAtMinusOne(t *testing.T) {
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 0,
	}
	for i := 0; i < 5; i++ {
		result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		m = result
	}
	if m.msgScroll != -1 {
		t.Errorf("after 5×k at 0: msgScroll = %d, want -1 (clamped at boundary)", m.msgScroll)
	}
}

func TestMsgScroll_JThenK_ReturnsToOriginal(t *testing.T) {
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 0,
	}
	// j then k should return to 0
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	result, _ = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if result.msgScroll != 0 {
		t.Errorf("j then k: msgScroll = %d, want 0", result.msgScroll)
	}

	// Three j then one k = 2 ahead
	result, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	result, _ = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	result, _ = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	result, _ = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if result.msgScroll != 2 {
		t.Errorf("3j then k: msgScroll = %d, want 2", result.msgScroll)
	}
}

func TestMsgScroll_DownArrow_Works(t *testing.T) {
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 5,
	}
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if result.msgScroll != 6 {
		t.Errorf("after down arrow: msgScroll = %d, want 6", result.msgScroll)
	}
}

func TestMsgScroll_UpArrow_Works(t *testing.T) {
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 5,
	}
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if result.msgScroll != 4 {
		t.Errorf("after up arrow: msgScroll = %d, want 4", result.msgScroll)
	}
}

func TestMsgScroll_HLResetsScrollToZero(t *testing.T) {
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 42,
		msgIndex:  1,
		convMsgs:  []conversationMessage{{}, {}},
	}
	// h (left) — decrements msgIndex and resets scroll
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if result.msgScroll != 0 {
		t.Errorf("after h: msgScroll = %d, want 0", result.msgScroll)
	}
	if result.msgIndex != 0 {
		t.Errorf("after h: msgIndex = %d, want 0", result.msgIndex)
	}
	// l (right) — increments msgIndex and resets scroll
	result, _ = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if result.msgScroll != 0 {
		t.Errorf("after l: msgScroll = %d, want 0", result.msgScroll)
	}
	if result.msgIndex != 1 {
		t.Errorf("after l: msgIndex = %d, want 1", result.msgIndex)
	}
}

func TestMsgScroll_FromNegativeTop_JScrollsImmediately(t *testing.T) {
	// msgScroll = -10 (from pressing k many times at top)
	// Pressing j should snap to 0 then increment to 1 → content scrolls down by 1 immediately
	m := DashboardModel{
		loaded:    true,
		mode:      modeMessage,
		msgScroll: -10,
	}
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if result.msgScroll != 1 {
		t.Errorf("j from -10: msgScroll = %d, want 1 (snapped to 0 then ++)", result.msgScroll)
	}
}

func TestMsgScroll_FromPastBottom_KScrollsImmediately(t *testing.T) {
	// Simulate: user is in message mode with a long message (200 lines),
	// scrolled far past the bottom, then presses k.
	longText := ""
	for i := 0; i < 200; i++ {
		longText += "some reasonably long line of text that wraps at contentWidth\n"
	}
	m := DashboardModel{
		height:    24,
		width:     80,
		loaded:    true,
		mode:      modeMessage,
		msgScroll: 200,
		cursor:    0,
		visible: []*TreeNode{
			{Type: NodeSession, Session: types.Session{ID: "s1", Title: "test", Directory: "/tmp"}},
		},
		convMsgs: []conversationMessage{
			{role: "user", text: longText},
		},
		msgIndex: 0,
	}
	m.rebuildMsgCache() // populate cachedLines for k handler
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if result.msgScroll >= 200 {
		t.Errorf("k from far past bottom: msgScroll = %d, want < 200 (snap should have triggered)", result.msgScroll)
	}
	if result.msgScroll < 0 {
		t.Errorf("k from far past bottom: msgScroll = %d, want >= 0", result.msgScroll)
	}
	if result.msgScroll >= 199 {
		t.Errorf("k should snap msgScroll down significantly, got %d", result.msgScroll)
	}
}

func TestMsgScroll_NotLoaded_IgnoresKeys(t *testing.T) {
	m := DashboardModel{
		loaded:    false,
		mode:      modeMessage,
		msgScroll: 0,
	}
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if result.msgScroll != 0 {
		t.Errorf("not loaded, j: msgScroll = %d, want 0", result.msgScroll)
	}
}
