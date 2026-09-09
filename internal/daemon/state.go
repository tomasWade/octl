// state.go：daemon 内存态持久化——收藏、卡住态（PERMISSION/ERROR）与
// pid/tmux 进程映射写 ~/.local/share/octl/state.json，daemon 重启后恢复。
// 旧版曾落在 opencode 目录（octl-state.json），已随自有数据目录收编迁出。
//
// 写入时点（全部收敛到 saveState）：
//   - 收藏变更即写（handleFavoriteAction / removeFavorite 调用点）
//   - 30s ticker 顺带全量快照（幂等）
//   - SIGTERM/SIGINT 退出前 flush
//
// 恢复语义：syncFromDB 之后 restoreState——favorites 整表灌入（孤儿交给
// 既有剪枝机制）；stuck 仅恢复 DB 仍存在的 session（source=event，豁免
// 接管语义天然保住），停机期间的解除事件由 hook 的 FIFO 缓冲补发自愈；
// 进程映射（schema v2）只恢复停机期间大概率仍存活的条目（LastSeenAt
// 5 分钟内 + kill(pid,0) 验活），让 daemon 重启不打断 ↗ tmux 跳转。
package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tomasWade/octl/internal/paths"
)

// daemonState 是 state.json 的落盘结构。
type daemonState struct {
	Version   int             `json:"version"`
	SavedAt   int64           `json:"savedAt"` // unix 毫秒
	Favorites []string        `json:"favorites"`
	Stuck     []stuckRecord   `json:"stuck"`
	Processes []processRecord `json:"processes,omitempty"` // v2 起；空时省略保持输出紧凑
}

// stuckRecord 是单个卡住态 session 的持久化条目。
type stuckRecord struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"` // "PERMISSION" | "ERROR"
	ErrorMsg  string `json:"errorMsg,omitempty"`
	PermType  string `json:"permType,omitempty"`
	PermTitle string `json:"permTitle,omitempty"`
}

// processRecord 是单个 session 的进程附着信息持久化条目。
type processRecord struct {
	SessionID   string `json:"sessionId"`
	PID         int64  `json:"pid"`
	TmuxPane    string `json:"tmuxPane,omitempty"`
	TmuxSession string `json:"tmuxSession,omitempty"`
	LastSeenAt  int64  `json:"lastSeenAt"` // unix 毫秒
}

// stateVersion 是当前 schema 版本；v2 增加进程映射段。restore 同时接受
// v1（无进程段，正常加载）与 v2；更早/更新版本静默跳过。
const stateVersion = 2

// processRetention 是进程映射的持久化窗口：只保存最近活跃（LastSeenAt
// 在窗口内）的条目——更早的条目对应的 opencode 进程大概率已退出。
const processRetention = 5 * time.Minute

// statePathOverride 供测试注入 state.json 路径；空时用真实默认路径。
var statePathOverride string

// setStatePathForTest 注入/恢复 state 路径（空串恢复默认）。
func setStatePathForTest(path string) { statePathOverride = path }

// stateFilePath 返回 state.json 的路径。
func stateFilePath() string {
	if statePathOverride != "" {
		return statePathOverride
	}
	p, err := paths.StatePath()
	if err != nil {
		return ""
	}
	return p
}

// saveState 把当前内存态快照写入 state.json（临时文件 + rename
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
	// 进程映射：只保存 LastSeenAt 在保留窗口内的条目。收集来源是
	// sessionProcess（权威映射，与 buildView 注入同源；运行时它与
	// stateMap[].ProcessInfo 是同一指针的双写）。
	now := time.Now()
	for sid, info := range sm.sessionProcess {
		if info == nil {
			continue
		}
		if now.Sub(time.UnixMilli(info.LastSeenAt)) > processRetention {
			continue
		}
		st.Processes = append(st.Processes, processRecord{
			SessionID:   sid,
			PID:         info.PID,
			TmuxPane:    info.TMUXPane,
			TmuxSession: info.TMUXSession,
			LastSeenAt:  info.LastSeenAt,
		})
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
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		log.Printf("[daemon] state: write: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		log.Printf("[daemon] state: close: %v", err)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		log.Printf("[daemon] state: rename: %v", err)
	}
}

// restoreState 从 state.json 恢复收藏与卡住态。必须在 syncFromDB
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
	// v1（无进程段）与 v2（含进程段）都接受；其余版本静默跳过。
	if st.Version != 1 && st.Version != stateVersion {
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

	// 进程映射（v2）：kill(pid, 0) 验活后灌回 sessionProcess / pidIndex，
	// daemon 重启后 ↗ tmux 跳转对安静 session 依然可用；新事件到达时被
	// 覆盖重建。不要求 session 仍在 stateMap——映射独立于会话状态，孤儿
	// 映射无害（buildView 只为存在的 session 注入）。
	procRestored := 0
	for _, r := range st.Processes {
		if r.SessionID == "" || r.PID <= 0 {
			continue
		}
		if err := syscall.Kill(int(r.PID), 0); err != nil {
			// ESRCH=进程不存在；EPERM=存在但不可信号（视为活）。其余错误
			// 保守跳过。
			if err != syscall.EPERM {
				continue
			}
		}
		if _, exists := sm.sessionProcess[r.SessionID]; exists {
			continue // 运行期已重建的不覆盖
		}
		info := &ProcessInfo{
			PID:         r.PID,
			TMUXPane:    r.TmuxPane,
			TMUXSession: r.TmuxSession,
			LastSeenAt:  r.LastSeenAt,
		}
		sm.sessionProcess[r.SessionID] = info
		if sm.pidIndex[r.PID] == nil {
			sm.pidIndex[r.PID] = make(map[string]struct{})
		}
		sm.pidIndex[r.PID][r.SessionID] = struct{}{}
		// 与运行时 updateProcessInfo 对齐：session 在 stateMap 中时挂上
		// 同一指针（snapshot 路径会读 SessionState.ProcessInfo）。
		if e, ok := sm.stateMap[r.SessionID]; ok && e.ProcessInfo == nil {
			e.ProcessInfo = info
		}
		procRestored++
	}
	if favRestored > 0 || stuckRestored > 0 || procRestored > 0 {
		log.Printf("[daemon] state: restored %d favorites, %d stuck entries, %d process entries", favRestored, stuckRestored, procRestored)
	}
}
