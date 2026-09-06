package views

import (
	"strings"
	"unicode/utf8"
	"testing"

	"github.com/tomasWade/octl/internal/types"
)

// ============================================================================
// truncateRunes unit tests (taskId=3.3)
// ============================================================================

func TestTruncateRunes_ShortASCII_NoTruncation(t *testing.T) {
	input := "hello"
	result := truncateRunes(input, 47)
	if result != "hello" {
		t.Errorf("truncateRunes(%q, 47) = %q, want %q", input, result, input)
	}
}

func TestTruncateRunes_ExactLengthASCII_NoTruncation(t *testing.T) {
	input := strings.Repeat("A", 47)
	if len([]rune(input)) != 47 {
		t.Fatalf("test setup: expected 47 runes, got %d", len([]rune(input)))
	}
	result := truncateRunes(input, 47)
	if result != input {
		t.Errorf("truncateRunes(47-char string, 47) = %q, want unchanged %q", result, input)
	}
}

func TestTruncateRunes_LongASCII_TruncatedWithEllipsis(t *testing.T) {
	input := strings.Repeat("A", 80)
	result := truncateRunes(input, 47)
	runes := []rune(result)
	if len(runes) != 48 { // 47 runes + 1 ellipsis
		t.Errorf("truncateRunes(80xA, 47) rune count = %d, want 48 (47 + …)", len(runes))
	}
	if !strings.HasPrefix(result, strings.Repeat("A", 47)) {
		t.Errorf("truncateRunes(80xA, 47) should start with 47 A's, got %q", result)
	}
	if !strings.HasSuffix(result, "…") {
		t.Errorf("truncateRunes(80xA, 47) should end with …, got %q", result)
	}
}

func TestTruncateRunes_ChineseMultiByte_NoByteSliceSplit(t *testing.T) {
	// Chinese characters are 3 bytes each in UTF-8.
	// A byte-slice title[:47] would split mid-character, producing invalid UTF-8.
	// truncateRunes must truncate by rune count, not byte count.
	chineseChar := "中"
	input := strings.Repeat(chineseChar, 60) // 60 Chinese chars = 180 bytes
	result := truncateRunes(input, 47)

	runes := []rune(result)
	if len(runes) != 48 { // 47 Chinese chars + …
		t.Errorf("truncateRunes(60×中, 47) rune count = %d, want 48 (47 + …)", len(runes))
	}

	// Verify no U+FFFD replacement character (would indicate broken encoding)
	if strings.ContainsRune(result, '\uFFFD') {
		t.Errorf("truncateRunes produced U+FFFD replacement char — byte-slice split suspected\nGot: %q", result)
	}

	// Verify the result is valid UTF-8
	if !utf8.ValidString(result) {
		t.Errorf("truncateRunes produced invalid UTF-8, got %q", result)
	}

	// Verify the first 47 chars are all the original Chinese char
	prefix := string(runes[:47])
	expectedPrefix := strings.Repeat(chineseChar, 47)
	if prefix != expectedPrefix {
		t.Errorf("first 47 runes = %q, want %q", prefix, expectedPrefix)
	}

	// Verify it ends with …
	if !strings.HasSuffix(result, "…") {
		t.Errorf("truncateRunes should end with …, got %q", result)
	}
}

func TestTruncateRunes_EmojiMultiByte_NoByteSliceSplit(t *testing.T) {
	// Emoji like 🔥 is 4 bytes in UTF-8 (1 rune).
	// A byte-slice title[:47] would almost certainly split this incorrectly.
	emoji := "🔥"
	input := strings.Repeat(emoji, 50) // 50 emoji = 200 bytes
	result := truncateRunes(input, 47)

	runes := []rune(result)
	if len(runes) != 48 { // 47 emoji + …
		t.Errorf("truncateRunes(50×🔥, 47) rune count = %d, want 48 (47 + …)", len(runes))
	}

	if strings.ContainsRune(result, '\uFFFD') {
		t.Errorf("truncateRunes produced U+FFFD replacement char — byte-slice split suspected\nGot: %q", result)
	}

	if !utf8.ValidString(result) {
		t.Errorf("truncateRunes produced invalid UTF-8, got %q", result)
	}

	if !strings.HasSuffix(result, "…") {
		t.Errorf("truncateRunes should end with …, got %q", result)
	}
}

func TestTruncateRunes_MixedASCIIAndChinese_TruncatedAtRuneBoundary(t *testing.T) {
	// Mix of 1-byte (ASCII), 3-byte (Chinese) characters
	input := "Hello世界" + strings.Repeat("测试", 30) // well over 47 runes
	result := truncateRunes(input, 47)

	if !utf8.ValidString(result) {
		t.Errorf("truncateRunes mixed produced invalid UTF-8, got %q", result)
	}

	if strings.ContainsRune(result, '\uFFFD') {
		t.Errorf("truncateRunes mixed produced U+FFFD, got %q", result)
	}

	runes := []rune(result)
	if len(runes) != 48 {
		t.Errorf("truncateRunes mixed rune count = %d, want 48", len(runes))
	}

	if !strings.HasSuffix(result, "…") {
		t.Errorf("truncateRunes mixed should end with …, got %q", result)
	}
}

func TestTruncateRunes_EmptyString_ReturnsEmpty(t *testing.T) {
	result := truncateRunes("", 47)
	if result != "" {
		t.Errorf("truncateRunes('', 47) = %q, want ''", result)
	}
}

func TestTruncateRunes_ZeroMaxRunes_ReturnsJustEllipsis(t *testing.T) {
	result := truncateRunes("hello", 0)
	if result != "…" {
		t.Errorf("truncateRunes('hello', 0) = %q, want '…'", result)
	}
}

func TestTruncateRunes_OneRuneAtLimit_NoTruncation(t *testing.T) {
	result := truncateRunes("A", 1)
	if result != "A" {
		t.Errorf("truncateRunes('A', 1) = %q, want 'A'", result)
	}
}

func TestTruncateRunes_TwoRunesWithLimitOne_Truncated(t *testing.T) {
	result := truncateRunes("AB", 1)
	if result != "A…" {
		t.Errorf("truncateRunes('AB', 1) = %q, want 'A…'", result)
	}
}

func TestTruncateRunes_UnicodeNullByte_HandledCorrectly(t *testing.T) {
	// Null byte embedded in string — should not cause issues since we count runes
	input := "hello\x00world" + strings.Repeat("x", 50)
	result := truncateRunes(input, 47)

	if !utf8.ValidString(result) {
		t.Errorf("truncateRunes with null byte produced invalid UTF-8")
	}

	runes := []rune(result)
	if len(runes) != 48 {
		t.Errorf("truncateRunes with null byte rune count = %d, want 48", len(runes))
	}
	if !strings.HasSuffix(result, "…") {
		t.Errorf("truncateRunes with null byte should end with …, got %q", result)
	}
}

// ============================================================================
// confirmView integration tests: truncateRunes used correctly (taskId=3.3)
// ============================================================================

func TestConfirmView_SingleSession_ChineseLongTitle_NoBrokenUTF8(t *testing.T) {
	chineseTitle := strings.Repeat("中", 60)
	m := DashboardModel{
		width:             120,
		height:            24,
		action:            "delete",
		pendingSessionIDs: []string{"sess-cn"},
		roots: []*TreeNode{{
			Type:    NodeProject,
			Project: types.Project{ID: "proj-1", Worktree: "/tmp"},
			Children: []*TreeNode{{
				Type:    NodeSession,
				Session: types.Session{ID: "sess-cn", Title: chineseTitle},
			}},
		}},
	}

	result := m.confirmView()

	if strings.ContainsRune(result, '\uFFFD') {
		t.Errorf("confirmView with Chinese title produced U+FFFD replacement character\nGot:\n%s", result)
	}

	if !utf8.ValidString(result) {
		t.Errorf("confirmView with Chinese title produced invalid UTF-8")
	}

	if !strings.Contains(result, "…") {
		t.Errorf("confirmView with 60-char Chinese title should contain '…'\nGot:\n%s", result)
	}

	if strings.Contains(result, chineseTitle) {
		t.Errorf("confirmView should NOT contain the full 60-char Chinese title\nGot:\n%s", result)
	}

	if !strings.Contains(result, strings.Repeat("中", 10)) {
		t.Errorf("confirmView should contain the truncated Chinese prefix\nGot:\n%s", result)
	}
}

func TestConfirmView_SingleSession_EmojiTitle_NoBrokenUTF8(t *testing.T) {
	emojiTitle := strings.Repeat("🔥", 55)
	m := DashboardModel{
		width:             120,
		height:            24,
		action:            "delete",
		pendingSessionIDs: []string{"sess-emoji"},
		roots: []*TreeNode{{
			Type:    NodeProject,
			Project: types.Project{ID: "proj-1", Worktree: "/tmp"},
			Children: []*TreeNode{{
				Type:    NodeSession,
				Session: types.Session{ID: "sess-emoji", Title: emojiTitle},
			}},
		}},
	}

	result := m.confirmView()

	if strings.ContainsRune(result, '\uFFFD') {
		t.Errorf("confirmView with emoji title produced U+FFFD\nGot:\n%s", result)
	}

	if !utf8.ValidString(result) {
		t.Errorf("confirmView with emoji title produced invalid UTF-8")
	}

	if !strings.Contains(result, "…") {
		t.Errorf("confirmView with 55-emoji title should contain '…'\nGot:\n%s", result)
	}

	if strings.Contains(result, emojiTitle) {
		t.Errorf("confirmView should NOT contain full emoji title\nGot:\n%s", result)
	}
}

func TestConfirmView_SingleSession_ASCIILongTitle_Truncated(t *testing.T) {
	asciiTitle := strings.Repeat("X", 80)
	m := DashboardModel{
		width:             120,
		height:            24,
		action:            "delete",
		pendingSessionIDs: []string{"sess-ascii"},
		roots: []*TreeNode{{
			Type:    NodeProject,
			Project: types.Project{ID: "proj-1", Worktree: "/tmp"},
			Children: []*TreeNode{{
				Type:    NodeSession,
				Session: types.Session{ID: "sess-ascii", Title: asciiTitle},
			}},
		}},
	}

	result := m.confirmView()

	if !strings.Contains(result, "…") {
		t.Errorf("confirmView with 80-char ASCII title should contain '…'\nGot:\n%s", result)
	}

	if strings.Contains(result, asciiTitle) {
		t.Errorf("confirmView should NOT contain full 80-char title\nGot:\n%s", result)
	}

	if !strings.Contains(result, strings.Repeat("X", 47)) {
		t.Errorf("confirmView should contain 47-char truncated prefix\nGot:\n%s", result)
	}
}

func TestConfirmView_SingleSession_ShortTitle_NoTruncation(t *testing.T) {
	shortTitle := "Fix login bug"
	m := DashboardModel{
		width:             120,
		height:            24,
		action:            "delete",
		pendingSessionIDs: []string{"sess-short"},
		roots: []*TreeNode{{
			Type:    NodeProject,
			Project: types.Project{ID: "proj-1", Worktree: "/tmp"},
			Children: []*TreeNode{{
				Type:    NodeSession,
				Session: types.Session{ID: "sess-short", Title: shortTitle},
			}},
		}},
	}

	result := m.confirmView()

	if !strings.Contains(result, shortTitle) {
		t.Errorf("confirmView should contain short title unchanged\nGot:\n%s", result)
	}

	if strings.Contains(result, "…") {
		t.Errorf("confirmView should NOT contain '…' for short title\nGot:\n%s", result)
	}
}

func TestConfirmView_SingleSession_Exactly47Runes_NoTruncation(t *testing.T) {
	exactTitle := strings.Repeat("A", 47)
	if len([]rune(exactTitle)) != 47 {
		t.Fatalf("test setup: expected 47 runes, got %d", len([]rune(exactTitle)))
	}
	m := DashboardModel{
		width:             120,
		height:            24,
		action:            "delete",
		pendingSessionIDs: []string{"sess-exact"},
		roots: []*TreeNode{{
			Type:    NodeProject,
			Project: types.Project{ID: "proj-1", Worktree: "/tmp"},
			Children: []*TreeNode{{
				Type:    NodeSession,
				Session: types.Session{ID: "sess-exact", Title: exactTitle},
			}},
		}},
	}

	result := m.confirmView()

	if !strings.Contains(result, exactTitle) {
		t.Errorf("confirmView should contain full title of exactly 47 runes\nGot:\n%s", result)
	}
}

func TestConfirmView_MultipleSessions_MixedChineseAndASCII_AllValid(t *testing.T) {
	chineseTitle := strings.Repeat("测试", 30) // 60 runes
	asciiTitle := strings.Repeat("X", 80)
	shortTitle := "short"

	m := DashboardModel{
		width:             120,
		height:            40,
		action:            "delete",
		pendingSessionIDs: []string{"sess-cn", "sess-ascii", "sess-short"},
		roots: []*TreeNode{{
			Type:    NodeProject,
			Project: types.Project{ID: "proj-1", Worktree: "/tmp"},
			Children: []*TreeNode{
				{Type: NodeSession, Session: types.Session{ID: "sess-cn", Title: chineseTitle}},
				{Type: NodeSession, Session: types.Session{ID: "sess-ascii", Title: asciiTitle}},
				{Type: NodeSession, Session: types.Session{ID: "sess-short", Title: shortTitle}},
			},
		}},
	}

	result := m.confirmView()

	if !utf8.ValidString(result) {
		t.Errorf("confirmView multi-session produced invalid UTF-8")
	}

	if strings.ContainsRune(result, '\uFFFD') {
		t.Errorf("confirmView multi-session produced U+FFFD replacement char")
	}

	if !strings.Contains(result, "…") {
		t.Errorf("confirmView multi-session with long titles should contain '…'\nGot:\n%s", result)
	}

	if strings.Contains(result, chineseTitle) {
		t.Errorf("confirmView should NOT contain full 60-rune Chinese title\nGot:\n%s", result)
	}
	if strings.Contains(result, asciiTitle) {
		t.Errorf("confirmView should NOT contain full 80-char ASCII title\nGot:\n%s", result)
	}

	if !strings.Contains(result, shortTitle) {
		t.Errorf("confirmView should contain short title unchanged\nGot:\n%s", result)
	}
}
