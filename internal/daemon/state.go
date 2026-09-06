// state.go：daemon 内存态持久化——收藏与卡住态（PERMISSION/ERROR）写
// ~/.local/share/opencode/octl-state.json，daemon 重启后恢复。
//
// 写入时点（全部收敛到 saveState）：
//   - 收藏变更即写（handleFavoriteAction / removeFavorite 调用点）
//   - 30s ticker 顺带全量快照（幂等）
//   - SIGTERM/SIGINT 退出前 flush
//
// 恢复语义：syncFromDB 之后 restoreState——favorites 整表灌入（孤儿交给
// 既有剪枝机制）；stuck 仅恢复 DB 仍存在的 session（source=event，豁免
// 接管语义天然保住），停机期间的解除事件由 hook 的 FIFO 缓冲补发自愈。
//
// 不恢复：pid/tmux 映射（进程生死未知，新事件自动重建）。
package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// daemonState 是 octl-state.json 的落盘结构。
type daemonState struct {
	Version   int           `json:"version"`
	SavedAt   int64         `json:"savedAt"` // unix 毫秒
	Favorites []string      `json:"favorites"`
	Stuck     []stuckRecord `json:"stuck"`
}

// stuckRecord 是单个卡住态 session 的持久化条目。
type stuckRecord struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"` // "PERMISSION" | "ERROR"
	ErrorMsg  string `json:"errorMsg,omitempty"`
	PermType  string `json:"permType,omitempty"`
	PermTitle string `json:"permTitle,omitempty"`
}

// stateVersion 是当前 schema 版本；不符时 restore 静默跳过。
const stateVersion = 1

// statePathOverride 供测试注入 state.json 路径；空时用真实默认路径。
var statePathOverride string

// setStatePathForTest 注入/恢复 state 路径（空串恢复默认）。
func setStatePathForTest(path string) { statePathOverride = path }

// stateFilePath 返回 octl-state.json 的路径。
func stateFilePath() string {
	if statePathOverride != "" {
		return statePathOverride
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local/share/opencode/octl-state.json")
}

// saveState 把当前内存态快照写入 octl-state.json（临时文件 + rename
// 原子替换）。调用方负责持锁快照数据（本函数内部不再加锁，避免写文件
// 慢操作持锁）。失败只记日志。
func (sm *StateManager) saveState() {
	path := stateFilePath()
	if path == "" {
		return
	}

	sm.mu.Lock()
	st := daemonState{
		Version:   stateVersion,
		SavedAt:   time.Now().UnixMilli(),
		Favorites: append([]string(nil), sm.favorites...),
	}
	if st.Favorites == nil {
		st.Favorites = []string{}
	}
	for _, e := range sm.stateMap {
		if e.Status == StatusPermission || e.Status == StatusError {
			st.Stuck = append(st.Stuck, stuckRecord{
				SessionID: e.SessionID,
				Status:    string(e.Status),
				ErrorMsg:  e.ErrorMsg,
				PermType:  e.PermType,
				PermTitle: e.PermTitle,
			})
		}
	}
	if st.Stuck == nil {
		st.Stuck = []stuckRecord{}
	}
	sm.mu.Unlock()

	b, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		log.Printf("[daemon] state: marshal: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[daemon] state: mkdir: %v", err)
		return
	}
	// 唯一临时文件名：saveState 会被多个异步 goroutine 并发调用（收藏
	// 变更即写、周期快照、退出 flush），固定 tmp 名在 O_TRUNC 下会产生
	// 写交错的混合内容并被 rename 固化——CreateTemp 保证每次写入独立。
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		log.Printf("[daemon] state: createtemp: %v", err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		log.Printf("[daemon] state: write: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		log.Printf("[daemon] state: close: %v", err)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		log.Printf("[daemon] state: rename: %v", err)
	}
}

// restoreState 从 octl-state.json 恢复收藏与卡住态。必须在 syncFromDB
// 初次同步之后调用（依赖 stateMap 已含 DB 中的 session）。文件缺失、
// 解析失败或 version 不符时静默跳过（全新开始）。
func (sm *StateManager) restoreState() {
	path := stateFilePath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return // 文件不存在：首次运行，正常
	}
	var st daemonState
	if err := json.Unmarshal(b, &st); err != nil {
		log.Printf("[daemon] state: parse failed, starting fresh: %v", err)
		return
	}
	if st.Version != stateVersion {
		log.Printf("[daemon] state: version %d != %d, starting fresh", st.Version, stateVersion)
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 收藏：整表灌入，保序；孤儿不在此剪（既有 tombstone 扫描与删除
	// 联动剪枝会清理）。
	favRestored := 0
	for _, sid := range st.Favorites {
		if sid == "" || sm.favoritesSet[sid] {
			continue
		}
		sm.favorites = append(sm.favorites, sid)
		sm.favoritesSet[sid] = true
		favRestored++
	}

	// 卡住态：仅恢复 stateMap 中仍存在的 session（DB 已删的丢弃）。
	// stateMap 是指针 map，就地覆盖字段即可。
	stuckRestored := 0
	savedAt := st.SavedAt
	for _, r := range st.Stuck {
		e, ok := sm.stateMap[r.SessionID]
		if !ok || r.SessionID == "" {
			continue
		}
		status := SessionStatus(r.Status)
		if status != StatusPermission && status != StatusError {
			continue
		}
		e.Status = status
		e.ErrorMsg = r.ErrorMsg
		e.PermType = r.PermType
		e.PermTitle = r.PermTitle
		e.Source = SourceEvent
		e.LastEventAt = time.UnixMilli(savedAt)
		stuckRestored++
	}
	if favRestored > 0 || stuckRestored > 0 {
		log.Printf("[daemon] state: restored %d favorites, %d stuck entries", favRestored, stuckRestored)
	}
}
