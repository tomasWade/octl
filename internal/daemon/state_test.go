// state_test.go 覆盖 daemon 内存态持久化：save/restore 往返、孤儿 stuck
// 丢弃、坏 JSON 容错、路径注入防污染真实 home。
package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// withStatePath 把 state.json 路径注入到临时目录执行 fn，结束后恢复。
func withStatePath(t *testing.T, fn func(path string)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "octl-state.json")
	orig := stateFilePath()
	setStatePathForTest(path)
	defer setStatePathForTest(orig)
	fn(path)
}

// TestSaveRestoreState_RoundTrip 收藏顺序与 stuck 字段的往返完整性。
func TestSaveRestoreState_RoundTrip(t *testing.T) {
	withStatePath(t, func(path string) {
		database := setupDBWithData(t, func(wdb *sql.DB) {})
		defer database.Close()

		sm := NewStateManager(database)
		sm.mu.Lock()
		sm.favorites = []string{"ses_a", "ses_b", "ses_c"}
		sm.favoritesSet = map[string]bool{"ses_a": true, "ses_b": true, "ses_c": true}
		// 一条 PERMISSION（带权限详情）+ 一条 ERROR（带错误信息）。
		sm.stateMap["ses_a"] = &SessionState{SessionID: "ses_a", Status: StatusPermission, PermType: "bash", PermTitle: "Run bash: npm test"}
		sm.stateMap["ses_b"] = &SessionState{SessionID: "ses_b", Status: StatusError, ErrorMsg: "boom"}
		sm.stateMap["ses_idle"] = &SessionState{SessionID: "ses_idle", Status: StatusIdle}
		sm.mu.Unlock()
		sm.saveState()

		// 新 StateManager 模拟重启恢复。
		sm2 := NewStateManager(database)
		// 预置 stateMap（模拟 syncFromDB 已灌入 DB session）。
		sm2.mu.Lock()
		sm2.stateMap["ses_a"] = &SessionState{SessionID: "ses_a", Status: StatusIdle}
		sm2.stateMap["ses_b"] = &SessionState{SessionID: "ses_b", Status: StatusIdle}
		sm2.stateMap["ses_idle"] = &SessionState{SessionID: "ses_idle", Status: StatusIdle}
		sm2.mu.Unlock()
		sm2.restoreState()

		sm2.mu.Lock()
		defer sm2.mu.Unlock()
		if len(sm2.favorites) != 3 || sm2.favorites[0] != "ses_a" || sm2.favorites[2] != "ses_c" {
			t.Errorf("favorites 恢复失败: %v", sm2.favorites)
		}
		if !sm2.favoritesSet["ses_b"] {
			t.Errorf("favoritesSet 恢复失败")
		}
		if sm2.stateMap["ses_a"].Status != StatusPermission || sm2.stateMap["ses_a"].PermType != "bash" || sm2.stateMap["ses_a"].PermTitle != "Run bash: npm test" {
			t.Errorf("stuck PERMISSION 恢复失败: %+v", sm2.stateMap["ses_a"])
		}
		if sm2.stateMap["ses_b"].Status != StatusError || sm2.stateMap["ses_b"].ErrorMsg != "boom" {
			t.Errorf("stuck ERROR 恢复失败: %+v", sm2.stateMap["ses_b"])
		}
		if sm2.stateMap["ses_a"].Source != SourceEvent {
			t.Errorf("恢复后 source 应为 EVENT: %+v", sm2.stateMap["ses_a"].Source)
		}
		// 非 stuck 的 session 不受影响。
		if sm2.stateMap["ses_idle"].Status != StatusIdle {
			t.Errorf("IDLE session 被误改: %+v", sm2.stateMap["ses_idle"])
		}
	})
}

// TestRestoreState_OrphanStuckDropped DB 已不存在的 stuck 条目丢弃；
// 收藏孤儿保留（交给既有剪枝机制）。
func TestRestoreState_OrphanStuckDropped(t *testing.T) {
	withStatePath(t, func(path string) {
		st := daemonState{
			Version:   stateVersion,
			Favorites: []string{"ses_gone_fav", "ses_alive"},
			Stuck: []stuckRecord{
				{SessionID: "ses_gone", Status: string(StatusPermission)},
				{SessionID: "ses_alive", Status: string(StatusError), ErrorMsg: "x"},
			},
		}
		b, _ := json.Marshal(&st)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		database := setupDBWithData(t, func(wdb *sql.DB) {})
		defer database.Close()
		sm := NewStateManager(database)
		sm.mu.Lock()
		sm.stateMap["ses_alive"] = &SessionState{SessionID: "ses_alive", Status: StatusIdle}
		sm.mu.Unlock()

		sm.restoreState()
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if _, ok := sm.stateMap["ses_gone"]; ok {
			t.Errorf("孤儿 stuck 不应被恢复")
		}
		if sm.stateMap["ses_alive"].Status != StatusError {
			t.Errorf("存活的 stuck 未恢复: %+v", sm.stateMap["ses_alive"])
		}
		if !sm.favoritesSet["ses_gone_fav"] {
			t.Errorf("收藏孤儿应保留（交给既有剪枝机制）")
		}
	})
}

// TestRestoreState_BadInput 坏 JSON / version 不符 / 文件缺失 → 静默跳过。
func TestRestoreState_BadInput(t *testing.T) {
	withStatePath(t, func(path string) {
		database := setupDBWithData(t, func(wdb *sql.DB) {})
		defer database.Close()

		for name, content := range map[string]string{
			"坏JSON":   `{"version": 1, "favorites": [`,
			"版本不符":    `{"version": 99, "favorites": ["ses_x"]}`,
			"非JSON文本": `not json at all`,
		} {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			sm := NewStateManager(database)
			sm.restoreState() // 不应 panic
			sm.mu.Lock()
			if len(sm.favorites) != 0 {
				t.Errorf("%s: 不应恢复任何收藏, got %v", name, sm.favorites)
			}
			sm.mu.Unlock()
		}

		// 文件缺失：静默跳过。
		os.Remove(path)
		sm := NewStateManager(database)
		sm.restoreState()
	})
}

// TestSaveState_NoTmpResidue 原子写不残留 .tmp。
func TestSaveState_NoTmpResidue(t *testing.T) {
	withStatePath(t, func(path string) {
		database := setupDBWithData(t, func(wdb *sql.DB) {})
		defer database.Close()
		sm := NewStateManager(database)
		sm.saveState()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("state 文件未生成: %v", err)
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Errorf("tmp 残留: %v", err)
		}
		// 空 state 的 JSON 也应是合法结构，且列表字段不发 null
		//（消费方 len() 不设防，与 wire 契约同规）。
		b, _ := os.ReadFile(path)
		var st daemonState
		if err := json.Unmarshal(b, &st); err != nil || st.Version != stateVersion {
			t.Errorf("空 state 序列化异常: %s", b)
		}
		if strings.Contains(string(b), ": null") {
			t.Errorf("空 state 含 null 字段: %s", b)
		}
	})
}

// TestSaveState_Concurrent 并发 saveState（异步挂点场景）不 panic 且
// 最后写入合法。go test -race 下验证锁正确性。校验必须是**完整合法
// JSON 且字段完备**——曾经的 tmp 写交错 bug 产出混合内容，子串校验
// 抓不住（review 发现）。
func TestSaveState_Concurrent(t *testing.T) {
	withStatePath(t, func(path string) {
		database := setupDBWithData(t, func(wdb *sql.DB) {})
		defer database.Close()
		sm := NewStateManager(database)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sm.mu.Lock()
				sm.favorites = []string{fmt.Sprintf("ses_%d", i)}
				sm.favoritesSet = map[string]bool{fmt.Sprintf("ses_%d", i): true}
				sm.mu.Unlock()
				sm.saveState()
			}(i)
		}
		wg.Wait()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var st daemonState
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatalf("并发写后 JSON 不完整（写交错）: %v\n%s", err, b)
		}
		if st.Version != stateVersion || len(st.Favorites) != 1 || len(st.Stuck) != 0 {
			t.Errorf("并发写后字段异常: %+v", st)
		}
		// 无残留 tmp 文件（CreateTemp 唯一名 + rename 成功消费）。
		entries, _ := os.ReadDir(filepath.Dir(path))
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp-") {
				t.Errorf("tmp 残留: %s", e.Name())
			}
		}
	})
}

// TestRestoreDeferredUntilSync 初次 sync 失败时 restore 不丢 stuck：
// 恢复逻辑本身与触发时机解耦（Run 的 ticker 补跑由代码审查保证，此处
// 锁定 restoreState 在 stateMap 就绪后调用即可恢复完整语义）。
func TestRestoreDeferredUntilSync(t *testing.T) {
	withStatePath(t, func(path string) {
		// 先写一份带 stuck 的 state。
		database := setupDBWithData(t, func(wdb *sql.DB) {})
		defer database.Close()
		sm := NewStateManager(database)
		sm.mu.Lock()
		sm.stateMap["ses_k"] = &SessionState{SessionID: "ses_k", Status: StatusPermission, PermType: "bash"}
		sm.mu.Unlock()
		sm.saveState()

		// 模拟重启 + 初次 sync 失败（stateMap 空）→ 此时 restore 会丢。
		sm2 := NewStateManager(database)
		sm2.restoreState() // stateMap 空：stuck 全部按孤儿丢弃（预期）
		sm2.mu.Lock()
		if len(sm2.favorites) != 0 {
			t.Errorf("意外恢复收藏: %v", sm2.favorites)
		}
		sm2.mu.Unlock()

		// ticker 补跑：sync 成功（stateMap 灌入）后再 restore——完整恢复。
		sm2.mu.Lock()
		sm2.stateMap["ses_k"] = &SessionState{SessionID: "ses_k", Status: StatusIdle}
		sm2.mu.Unlock()
		sm2.restoreState()
		sm2.mu.Lock()
		defer sm2.mu.Unlock()
		if sm2.stateMap["ses_k"].Status != StatusPermission || sm2.stateMap["ses_k"].PermType != "bash" {
			t.Errorf("延迟恢复失败: %+v", sm2.stateMap["ses_k"])
		}
	})
}
