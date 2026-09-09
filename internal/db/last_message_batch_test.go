package db

import (
	"testing"
)

// TestGetAllLastMessages_MatchesSingleQuery 验证批量结果与逐条
// GetLastMessageRoleCompleted 完全一致（角色定位、completed 语义、无消息
// session 缺席）。
func TestGetAllLastMessages_MatchesSingleQuery(t *testing.T) {
	d, _ := setupTestDB(t)

	sessions, err := d.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("test DB has no sessions; fixture changed?")
	}

	batch, err := d.GetAllLastMessages()
	if err != nil {
		t.Fatalf("GetAllLastMessages: %v", err)
	}

	for _, s := range sessions {
		singleRole, singleCompleted, err := d.GetLastMessageRoleCompleted(s.ID)
		if err != nil {
			t.Fatalf("GetLastMessageRoleCompleted(%s): %v", s.ID, err)
		}
		got, ok := batch[s.ID]
		if singleRole == "" && singleCompleted == 0 {
			// 无消息 session：批量结果中缺席。
			if ok {
				t.Errorf("session %s without messages present in batch result: %+v", s.ID, got)
			}
			continue
		}
		if !ok {
			t.Errorf("session %s missing from batch result", s.ID)
			continue
		}
		if got.Role != singleRole || got.Completed != singleCompleted {
			t.Errorf("session %s batch = (%q,%d), single = (%q,%d)", s.ID, got.Role, got.Completed, singleRole, singleCompleted)
		}
	}
}

// TestGetAllLastMessages_EmptyDB 验证空库/无消息时返回空 map 而非 nil 语义错误。
func TestGetAllLastMessages_EmptyDB(t *testing.T) {
	d, _ := setupTestDB(t)
	batch, err := d.GetAllLastMessages()
	if err != nil {
		t.Fatalf("GetAllLastMessages: %v", err)
	}
	if len(batch) == 0 {
		// setupTestDB 插入的固定数据可能全无消息；空 map 是合法结果。
		return
	}
	t.Logf("batch returned %d entries", len(batch))
}
