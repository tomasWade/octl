package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSessionStatusDistinctValues verifies all 7 SessionStatus constants
// have distinct string values.
func TestSessionStatusDistinctValues(t *testing.T) {
	statuses := []SessionStatus{
		StatusIdle,
		StatusBusy,
		StatusPermission,
		StatusRetry,
		StatusError,
		StatusUnknown,
		StatusArchived,
	}

	if len(statuses) != 7 {
		t.Fatalf("expected 7 SessionStatus constants, got %d", len(statuses))
	}

	seen := make(map[string]bool)
	for _, s := range statuses {
		key := string(s)
		if seen[key] {
			t.Errorf("duplicate SessionStatus value: %q", key)
		}
		seen[key] = true
	}

	expected := map[SessionStatus]string{
		StatusIdle:       "IDLE",
		StatusBusy:       "BUSY",
		StatusPermission: "PERMISSION",
		StatusRetry:      "RETRY",
		StatusError:      "ERROR",
		StatusUnknown:    "UNKNOWN",
		StatusArchived:   "ARCHIVED",
	}

	if len(expected) != len(statuses) {
		t.Fatal("expected map size mismatch")
	}

	for s, want := range expected {
		if got := string(s); got != want {
			t.Errorf("SessionStatus %v has value %q, want %q", s, got, want)
		}
	}
}

// TestStateSourceDistinctValues verifies both StateSource constants
// have distinct string values.
func TestStateSourceDistinctValues(t *testing.T) {
	sources := []StateSource{
		SourceEvent,
		SourceDB,
	}

	if len(sources) != 2 {
		t.Fatalf("expected 2 StateSource constants, got %d", len(sources))
	}

	seen := make(map[string]bool)
	for _, s := range sources {
		key := string(s)
		if seen[key] {
			t.Errorf("duplicate StateSource value: %q", key)
		}
		seen[key] = true
	}

	expected := map[StateSource]string{
		SourceEvent: "EVENT",
		SourceDB:    "DB",
	}

	if len(expected) != len(sources) {
		t.Fatal("expected map size mismatch")
	}

	for s, want := range expected {
		if got := string(s); got != want {
			t.Errorf("StateSource %v has value %q, want %q", s, got, want)
		}
	}
}

// TestSessionStateJSONRoundTrip verifies SessionState marshals and
// unmarshals correctly, preserving all visible fields.
func TestSessionStateJSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Microsecond)

	original := SessionState{
		SessionID:   "sess-123",
		Status:      StatusBusy,
		Source:      SourceEvent,
		LastEventAt: now,
		LastSyncAt:  now,
		Title:       "Test Session",
		ProjectID:   "proj-456",
		ErrorMsg:    "",
		PermType:    "",
		PermTitle:   "",
		Tombstone:   true,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal SessionState: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal into map: %v", err)
	}

	if _, ok := raw["tombstone"]; ok {
		t.Error("Tombstone field should be excluded from JSON output but 'tombstone' key was found")
	}

	var restored SessionState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("failed to unmarshal SessionState: %v", err)
	}

	if restored.SessionID != original.SessionID {
		t.Errorf("SessionID: got %q, want %q", restored.SessionID, original.SessionID)
	}
	if restored.Status != original.Status {
		t.Errorf("Status: got %q, want %q", restored.Status, original.Status)
	}
	if restored.Source != original.Source {
		t.Errorf("Source: got %q, want %q", restored.Source, original.Source)
	}
	if !restored.LastEventAt.Equal(original.LastEventAt) {
		t.Errorf("LastEventAt: got %v, want %v", restored.LastEventAt, original.LastEventAt)
	}
	if !restored.LastSyncAt.Equal(original.LastSyncAt) {
		t.Errorf("LastSyncAt: got %v, want %v", restored.LastSyncAt, original.LastSyncAt)
	}
	if restored.Title != original.Title {
		t.Errorf("Title: got %q, want %q", restored.Title, original.Title)
	}
	if restored.ProjectID != original.ProjectID {
		t.Errorf("ProjectID: got %q, want %q", restored.ProjectID, original.ProjectID)
	}
	if restored.ErrorMsg != original.ErrorMsg {
		t.Errorf("ErrorMsg: got %q, want %q", restored.ErrorMsg, original.ErrorMsg)
	}
	if restored.PermType != original.PermType {
		t.Errorf("PermType: got %q, want %q", restored.PermType, original.PermType)
	}
	if restored.PermTitle != original.PermTitle {
		t.Errorf("PermTitle: got %q, want %q", restored.PermTitle, original.PermTitle)
	}

	if restored.Tombstone {
		t.Error("Tombstone should be false after round-trip (excluded from JSON)")
	}
}

// TestSessionStateJSONOmitEmpty verifies that empty strings with omitempty
// are excluded from JSON output.
func TestSessionStateJSONOmitEmpty(t *testing.T) {
	state := SessionState{
		SessionID:   "sess-1",
		Status:      StatusIdle,
		Source:      SourceDB,
		LastEventAt: time.Now(),
		LastSyncAt:  time.Now(),
		Title:       "Minimal",
		ProjectID:   "",
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal SessionState: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal into map: %v", err)
	}

	if _, ok := raw["errorMsg"]; ok {
		t.Error("errorMsg should be omitted when empty")
	}
	if _, ok := raw["permType"]; ok {
		t.Error("permType should be omitted when empty")
	}
	if _, ok := raw["permTitle"]; ok {
		t.Error("permTitle should be omitted when empty")
	}

	required := []string{"sessionId", "status", "source", "lastEventAt", "lastSyncAt", "title"}
	for _, key := range required {
		if _, ok := raw[key]; !ok {
			t.Errorf("required field %q should be present in JSON output", key)
		}
	}
}

// TestSessionStateJSONUnmarshalErrors verifies unmarshaling with invalid
// data produces errors.
func TestSessionStateJSONUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "invalid JSON",
			data: `{invalid}`,
		},
		{
			name: "wrong type for Status",
			data: `{"sessionId":"s1","status":42,"source":"EVENT","lastEventAt":"2024-01-01T00:00:00Z","lastSyncAt":"2024-01-01T00:00:00Z","title":"test"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var state SessionState
			if err := json.Unmarshal([]byte(tc.data), &state); err == nil {
				t.Error("expected unmarshal error, got nil")
			}
		})
	}
}

// TestSessionStatusIcon verifies Icon() returns the correct display string
// for every SessionStatus value, including the default case for unknown values.
func TestSessionStatusIcon(t *testing.T) {
	tests := []struct {
		status SessionStatus
		want   string
	}{
		{StatusPermission, "🟡 ASK"},
		{StatusBusy, "🔵 BUSY"},
		{StatusRetry, "🟠 RETRY"},
		{StatusError, "🔴 ERROR"},
		{StatusIdle, "⚪ IDLE"},
		{StatusUnknown, "◯ ???"},
		{StatusArchived, "  —"},
		{SessionStatus("MADE_UP"), "◯ ???"},
		{SessionStatus(""), "◯ ???"},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := tc.status.Icon()
			if got != tc.want {
				t.Errorf("SessionStatus(%q).Icon() = %q, want %q", string(tc.status), got, tc.want)
			}
		})
	}
}

// TestSessionStatusGlyph verifies Glyph() returns the compact icon (no text
// label) for every SessionStatus value, including the default case.
func TestSessionStatusGlyph(t *testing.T) {
	tests := []struct {
		status SessionStatus
		want   string
	}{
		{StatusPermission, "🟡"},
		{StatusBusy, "🔵"},
		{StatusRetry, "🟠"},
		{StatusError, "🔴"},
		{StatusIdle, "🟢"},
		{StatusUnknown, "❔"},
		{StatusArchived, "📦"},
		{SessionStatus("MADE_UP"), "❔"},
		{SessionStatus(""), "❔"},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := tc.status.Glyph()
			if got != tc.want {
				t.Errorf("SessionStatus(%q).Glyph() = %q, want %q", string(tc.status), got, tc.want)
			}
			// Glyph 不应包含文字标签（IDLE/BUSY 等）。
			for _, label := range []string{"IDLE", "BUSY", "ASK", "RETRY", "ERROR"} {
				if strings.Contains(got, label) {
					t.Errorf("SessionStatus(%q).Glyph() = %q should not contain text label %q", string(tc.status), got, label)
				}
			}
		})
	}
}
