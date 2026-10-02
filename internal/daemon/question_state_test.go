package daemon

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"
)

// newSMForTest 临时库 + 空 StateManager（对齐既有测试夹具用法）。
func newSMForTest(t *testing.T) *StateManager {
	t.Helper()
	return NewStateManager(setupDBWithData(t, func(wdb *sql.DB) {}))
}

// questionAskProps 构造 question.asked 的事件属性（对齐 opencode schema
// v1/question.ts 的 Request：id + sessionID + questions[] + tool?）。
func questionAskProps(sid string, questions []interface{}) map[string]interface{} {
	return map[string]interface{}{
		"id":        "que_test123",
		"sessionID": sid,
		"questions": questions,
	}
}

func q(text, header string, labels ...string) map[string]interface{} {
	opts := make([]interface{}, 0, len(labels))
	for _, l := range labels {
		opts = append(opts, map[string]interface{}{"label": l, "description": "d"})
	}
	return map[string]interface{}{
		"question": text,
		"header":   header,
		"options":  opts,
	}
}

// TestQuestionAsked_CapturesContent 多问 payload：文本与选项按 Qn 前缀拼接，
// 状态与权限确认同义复用 PERMISSION。
func TestQuestionAsked_CapturesContent(t *testing.T) {
	sm := newSMForTest(t)
	defer sm.db.Close()

	sm.processEvent(marshalEvent(t, "question.asked", questionAskProps("ses_q1", []interface{}{
		q("继续用 tmux 吗", "终端", "继续", "换掉"),
		q("要不要写测试", "测试", "要"),
	})))

	sm.mu.RLock()
	defer sm.mu.RUnlock()
	st := sm.stateMap["ses_q1"]
	if st == nil {
		t.Fatal("stateMap 缺少 ses_q1")
	}
	if st.Status != StatusPermission {
		t.Errorf("Status = %s, want PERMISSION", st.Status)
	}
	if st.QuestionID != "que_test123" {
		t.Errorf("QuestionID = %q, want que_test123", st.QuestionID)
	}
	wantText := "Q1: 继续用 tmux 吗；Q2: 要不要写测试"
	if st.QuestionText != wantText {
		t.Errorf("QuestionText = %q, want %q", st.QuestionText, wantText)
	}
	wantOpts := []string{"Q1[继续/换掉]", "Q2[要]"}
	if len(st.QuestionOptions) != 2 || st.QuestionOptions[0] != wantOpts[0] || st.QuestionOptions[1] != wantOpts[1] {
		t.Errorf("QuestionOptions = %v, want %v", st.QuestionOptions, wantOpts)
	}
}

// TestQuestionAsked_SingleQuestion 单问：文本不加前缀，选项平铺 label。
func TestQuestionAsked_SingleQuestion(t *testing.T) {
	sm := newSMForTest(t)
	defer sm.db.Close()

	sm.processEvent(marshalEvent(t, "question.asked", questionAskProps("ses_q2", []interface{}{
		q("选哪个方案", "方案", "A方案", "B方案", "C方案"),
	})))

	sm.mu.RLock()
	defer sm.mu.RUnlock()
	st := sm.stateMap["ses_q2"]
	if st.QuestionText != "选哪个方案" {
		t.Errorf("QuestionText = %q, want 选哪个方案", st.QuestionText)
	}
	if len(st.QuestionOptions) != 3 || st.QuestionOptions[0] != "A方案" {
		t.Errorf("QuestionOptions = %v, want 平铺 label", st.QuestionOptions)
	}
}

// TestQuestionAsked_MalformedPayload questions 缺失/损坏：字段留空，状态仍
// PERMISSION——"问了个不知道"比"假装没问"强。
func TestQuestionAsked_MalformedPayload(t *testing.T) {
	sm := newSMForTest(t)
	defer sm.db.Close()

	sm.processEvent(marshalEvent(t, "question.asked", map[string]interface{}{
		"id":        "que_broken",
		"sessionID": "ses_q3",
	}))
	sm.processEvent(marshalEvent(t, "question.asked", map[string]interface{}{
		"sessionID": "ses_q4",
		"questions": "not-an-array",
	}))

	for _, sid := range []string{"ses_q3", "ses_q4"} {
		sm.mu.RLock()
		st := sm.stateMap[sid]
		sm.mu.RUnlock()
		if st == nil || st.Status != StatusPermission {
			t.Fatalf("%s 状态应为 PERMISSION，got %+v", sid, st)
		}
		if st.QuestionText != "" || st.QuestionOptions != nil {
			t.Errorf("%s 损坏 payload 不应产出内容字段: %+v", sid, st)
		}
	}
}

// TestQuestionReplied_ClearsQuestionFields 回答/忽略后 Question* 与 Perm* 一并
// 幂等清空，状态转 BUSY。
func TestQuestionReplied_ClearsQuestionFields(t *testing.T) {
	sm := newSMForTest(t)
	defer sm.db.Close()

	sm.processEvent(marshalEvent(t, "question.asked", questionAskProps("ses_q5", []interface{}{
		q("在吗", "确认", "在", "不在"),
	})))
	sm.processEvent(marshalEvent(t, "question.replied", map[string]interface{}{
		"sessionID": "ses_q5",
		"requestID": "que_test123",
	}))

	sm.mu.RLock()
	defer sm.mu.RUnlock()
	st := sm.stateMap["ses_q5"]
	if st.Status != StatusBusy {
		t.Errorf("Status = %s, want BUSY", st.Status)
	}
	if st.QuestionID != "" || st.QuestionText != "" || st.QuestionOptions != nil {
		t.Errorf("回答后 Question* 应清空: %+v", st)
	}
	if st.PermType != "" || st.PermTitle != "" {
		t.Errorf("回答后 Perm* 应清空: %+v", st)
	}
}

// TestStuckPersistence_QuestionFieldsRoundtrip 提问卡住期间落盘 state.json，
// daemon 重启后恢复，Question* 字段随 stuck 条目往返不丢。
func TestStuckPersistence_QuestionFieldsRoundtrip(t *testing.T) {
	withStatePath(t, func(path string) {
		sm := newSMForTest(t)
		sm.processEvent(marshalEvent(t, "question.asked", questionAskProps("ses_q6", []interface{}{
			q("恢复后还在吗", "持久化", "在"),
		})))
		sm.saveState()
		sm.db.Close()

		if b, err := os.ReadFile(path); err != nil {
			t.Fatalf("read state.json: %v", err)
		} else {
			var st daemonState
			if err := json.Unmarshal(b, &st); err != nil {
				t.Fatalf("parse state.json: %v", err)
			}
			found := false
			for _, r := range st.Stuck {
				if r.SessionID == "ses_q6" {
					found = true
					if r.QuestionText != "恢复后还在吗" || r.QuestionID != "que_test123" {
						t.Errorf("落盘字段不完整: %+v", r)
					}
				}
			}
			if !found {
				t.Fatalf("state.json 缺少 ses_q6 的 stuck 条目")
			}
		}

		// 重启语义：新 StateManager + 内存里已有该会话 → restoreState 灌回。
		sm2 := newSMForTest(t)
		defer sm2.db.Close()
		sm2.mu.Lock()
		sm2.stateMap["ses_q6"] = &SessionState{SessionID: "ses_q6", Status: StatusIdle}
		sm2.mu.Unlock()
		sm2.restoreState()

		sm2.mu.RLock()
		defer sm2.mu.RUnlock()
		st := sm2.stateMap["ses_q6"]
		if st.Status != StatusPermission {
			t.Errorf("重启后 Status = %s, want PERMISSION", st.Status)
		}
		if st.QuestionText != "恢复后还在吗" || st.QuestionID != "que_test123" {
			t.Errorf("重启后 Question* 丢失: %+v", st)
		}
		if len(st.QuestionOptions) != 1 || st.QuestionOptions[0] != "在" {
			t.Errorf("重启后 QuestionOptions 丢失: %v", st.QuestionOptions)
		}
	})
}
