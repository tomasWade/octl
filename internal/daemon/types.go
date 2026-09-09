// Package daemon provides the octl notification daemon that tracks session states.
package daemon

import (
	"time"
)

// SessionStatus represents the runtime status of an opencode session.
type SessionStatus string

const (
	// StatusIdle indicates the session is waiting for user input.
	StatusIdle SessionStatus = "IDLE"
	// StatusBusy indicates the AI is processing / thinking.
	StatusBusy SessionStatus = "BUSY"
	// StatusPermission indicates the session is waiting for tool approval.
	StatusPermission SessionStatus = "PERMISSION"
	// StatusRetry indicates an error retry is in progress.
	StatusRetry SessionStatus = "RETRY"
	// StatusError indicates the session is in an error state.
	StatusError SessionStatus = "ERROR"
	// StatusUnknown indicates the state is uncertain (DB-derived).
	StatusUnknown SessionStatus = "UNKNOWN"
	// StatusArchived indicates the session has been archived.
	StatusArchived SessionStatus = "ARCHIVED"
)

// StateSource indicates how the session state was last updated.
type StateSource string

const (
	// SourceEvent indicates the state was updated via a real-time event.
	SourceEvent StateSource = "EVENT"
	// SourceDB indicates the state was updated via a database sync.
	SourceDB StateSource = "DB"
)

// ProcessInfo captures the OS / tmux context of an opencode process that is
// driving a session. It is populated from live events and cleared when the
// process disconnects from the daemon.
type ProcessInfo struct {
	PID         int64  `json:"pid"`
	TMUXPane    string `json:"tmuxPane"`
	TMUXSession string `json:"tmuxSession"`
	LastSeenAt  int64  `json:"lastSeenAt"`
}

// SessionState represents the runtime state of a single session tracked by the daemon.
type SessionState struct {
	SessionID   string        `json:"sessionId"`
	Status      SessionStatus `json:"status"`
	Source      StateSource   `json:"source"`
	LastEventAt time.Time     `json:"lastEventAt"`
	LastSyncAt  time.Time     `json:"lastSyncAt"`
	Title       string        `json:"title"`
	ProjectID   string        `json:"projectId"`
	ProcessInfo *ProcessInfo  `json:"processInfo,omitempty"`
	ErrorMsg    string        `json:"errorMsg,omitempty"`
	PermType    string        `json:"permType,omitempty"`  // "bash" | "file_write" | ...
	PermTitle   string        `json:"permTitle,omitempty"` // e.g. "Run bash: npm test"
	Tombstone   bool          `json:"-"`                   // Internal: marked for deletion
}

// SidebarSession represents a single session shown in the OpenCode sidebar.
type SidebarSession struct {
	SessionID   string                `json:"sessionId"`
	Title       string                `json:"title"`
	TimeUpdated int64                 `json:"timeUpdated"`
	Statuses    map[SessionStatus]int `json:"statuses"`
	RowStatus   SessionStatus         `json:"rowStatus"`
}

// SidebarProject represents a project group shown in the OpenCode sidebar.
type SidebarProject struct {
	ProjectID   string           `json:"projectId"`
	Name        string           `json:"name"`
	Worktree    string           `json:"worktree"`
	TimeUpdated int64            `json:"timeUpdated"`
	Sessions    []SidebarSession `json:"sessions"`
}

// ---------------------------------------------------------------------------
// Wire protocol messages for the Unix socket API.
// ---------------------------------------------------------------------------

// VersionMismatchError 是 daemon 拒绝版本不一致的订阅时返回给客户端的错误文案。
// TUI（app.go）与 sidebar 插件都按此文案匹配「版本不一致」状态，修改时需同步更新。
// 注意：opencode 对 TUI 插件没有热重载（1.18.30 实测，SIGUSR2/reload 均不重建
// TUI 插件实例），重启实例是加载新插件的唯一途径；sidebar 收到本错误后停止
// 重连并弹出「重启」按钮交由用户决策。
const VersionMismatchError = `octl version mismatch — 插件已更新，请重启 opencode 实例加载（sidebar 可点击「重启」按钮）`

// BaseMsg is used to peek at the message type before full decoding.
type BaseMsg struct {
	Type string `json:"type"`
}

// SubscribeMsg is sent by a TUI client to receive snapshot updates.
type SubscribeMsg struct {
	Type     string   `json:"type"`
	Channels []string `json:"channels"`
	// Version is the protocol MD5 carried by the client (provided by internal/plugins.ProtocolMD5()).
	// The daemon compares it to decide whether to accept the subscription, eliminating version drift
	// between plugins and the daemon.
	Version string `json:"version,omitempty"`
}

// SubscribedMsg confirms a subscription.
type SubscribedMsg struct {
	Type     string   `json:"type"`
	Channels []string `json:"channels"`
}

// SnapshotMsg broadcasts the current stateMap to subscribers.
type SnapshotMsg struct {
	Type   string         `json:"type"`
	States []SessionState `json:"states"`
}

// PingMsg / PongMsg are optional keep-alive messages.
type PingMsg struct {
	Type string `json:"type"`
}

// PongMsg is the response to a PingMsg.
type PongMsg struct {
	Type string `json:"type"`
}

// RequestMsg is sent by a client to request data from the daemon.
type RequestMsg struct {
	Type      string `json:"type"`
	Method    string `json:"method"`
	ID        string `json:"id"`
	SessionID string `json:"sessionId,omitempty"`
	// From / To 仅对 "daily" / "report" 方法有效：时间窗口 [From, To)，unix 毫秒。
	From int64 `json:"from,omitempty"`
	To   int64 `json:"to,omitempty"`
	// Dir 仅对 "report" 方法有效：底片输出目录覆盖（空 = 默认目录）。
	Dir string `json:"dir,omitempty"`
}

// ResponseMsg is sent by the daemon in response to a RequestMsg.
type ResponseMsg struct {
	Type     string              `json:"type"`
	ID       string              `json:"id"`
	Ok       bool                `json:"ok"`
	Error    string              `json:"error,omitempty"`
	States   []SessionState      `json:"states,omitempty"`
	Projects []SidebarProject    `json:"projects,omitempty"`
	Messages []MessagePart       `json:"messages,omitempty"`
	Daily    *DailyDigest        `json:"daily,omitempty"`
	Report   *ReportResult       `json:"report,omitempty"`
	Archive  []ArchiveSessionRef `json:"archive,omitempty"` // "archiveSessions"：影子库全量引用（含已删线）
}

// ReportResult 是 "report" 请求的应答体：底片落盘结果（路径 + 统计）。
type ReportResult struct {
	Path     string `json:"path"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Active   int    `json:"active"`
	New      int    `json:"new"`
	Archived int    `json:"archived"`
	Sessions int    `json:"sessions"`
}

// ---------------------------------------------------------------------------
// Daily digest: 时间窗口内的聚合事实，供 CLI / 日报 skill 消费。
// octl 只输出事实（计数、时间、截断摘录），叙事总结由消费方完成。
// ---------------------------------------------------------------------------

// DailyDigest 是 "daily" 请求的应答体：一个时间窗口内全部活动事实。
type DailyDigest struct {
	From int64 `json:"from"` // 窗口起点，unix 毫秒（含）
	To   int64 `json:"to"`   // 窗口终点，unix 毫秒（不含）
	// TotalActive 是窗口内有消息的 session 总数；Excerpted 是其中带文本
	// 摘录的数量。Excerpted < TotalActive 表示有截断，消费方如需个别
	// session 的完整内容可再调 "messages" 请求补料。
	TotalActive int               `json:"totalActive"`
	Excerpted   int               `json:"excerpted"`
	Projects    []DailyProject    `json:"projects"`
	StuckStates []SessionState    `json:"stuckStates,omitempty"` // daemon 内存态中 PERMISSION / ERROR 的 session
	Zombies     []DailySessionRef `json:"zombies,omitempty"`     // 闲置超过阈值且近期有过活动的未归档 session
}

// DailyProject 是单个 project 在窗口内的聚合。
type DailyProject struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Worktree  string `json:"worktree"`
	// NewSessions：窗口内新建的 session（含 subsession），仅引用信息。
	NewSessions []DailySessionRef `json:"newSessions"`
	// ActiveSessions：窗口内有消息的 session，按窗口内消息数降序。
	ActiveSessions []DailySession `json:"activeSessions"`
	// ArchivedSessions：窗口内归档的 session（闭环信号），仅引用信息。
	ArchivedSessions []DailySessionRef `json:"archivedSessions"`
	// DeletedSessions：窗口内被删除的 session（影子库口径，仅引用信息；
	// LastActivity 承载删除时刻）。删除即"用户判定的完结"，日报据此
	// 补记被删线的当日贡献。
	DeletedSessions []DailySessionRef `json:"deletedSessions"`
	// SessionCostSum 是活跃 session 的累计 cost 之和（session 级累计，
	// 非窗口口径——message 表无费用列，窗口内费用不可得）。
	SessionCostSum float64 `json:"sessionCostSum"`
}

// DailySession 是窗口内单个活跃 session 的完整事实。
type DailySession struct {
	SessionID     string `json:"sessionId"`
	Title         string `json:"title"`
	MsgCount      int    `json:"msgCount"`      // 窗口内消息数
	FirstActivity int64  `json:"firstActivity"` // 窗口内首条消息时间
	LastActivity  int64  `json:"lastActivity"`  // 窗口内末条消息时间
	// HasExcerpt 为 false 时摘录字段为空：该 session 未进入摘录名额
	// （全局按窗口内消息数取前 N），消费方只有计数信息。
	HasExcerpt           bool   `json:"hasExcerpt"`
	FirstUserExcerpt     string `json:"firstUserExcerpt,omitempty"`     // 窗口内首条用户消息开头（已截断）
	LastAssistantExcerpt string `json:"lastAssistantExcerpt,omitempty"` // 截至窗口结束最新 assistant 消息开头（已截断）
	// Deleted：影子库口径下该 session 已被删除（含窗口内删除与更早删除
	// 但窗口内仍有消息两种情形）。DeletedAtMs 是删除时刻（unix ms）。
	Deleted     bool  `json:"deleted,omitempty"`
	DeletedAtMs int64 `json:"deletedAtMs,omitempty"`
}

// DailySessionRef 是摘要中不需要文本素材的 session 引用（新增/归档/僵尸）。
type DailySessionRef struct {
	SessionID    string `json:"sessionId"`
	Title        string `json:"title"`
	ProjectID    string `json:"projectId"`
	TimeCreated  int64  `json:"timeCreated"`  // 新增分类时使用
	LastActivity int64  `json:"lastActivity"` // 僵尸分类时使用（session.time_updated）
}

// MessagePart represents a single text part of a session message, used in
// response to a "messages" request.
type MessagePart struct {
	MessageID   string `json:"messageId"`
	Role        string `json:"role"`
	Text        string `json:"text"`
	TimeCreated int64  `json:"timeCreated"`
}

// ArchiveSessionRef 是 "archiveSessions" 应答的单条影子库 session 引用
// （含已删线），供 CLI 模糊匹配候选池使用。
type ArchiveSessionRef struct {
	SessionID   string `json:"sessionId"`
	Title       string `json:"title"`
	Deleted     bool   `json:"deleted"`
	DeletedAtMs int64  `json:"deletedAtMs,omitempty"`
}

// ---------------------------------------------------------------------------
// Action protocol: TUI -> daemon management operations.
// ---------------------------------------------------------------------------

// ActionMsg is sent by a TUI client to request a management operation.
type ActionMsg struct {
	Type       string   `json:"type"`   // "action"
	Action     string   `json:"action"` // "delete" | "export" | "create" | "fork" | "send"
	SessionID  string   `json:"sessionId,omitempty"`
	SessionIDs []string `json:"sessionIds,omitempty"`
	ProjectID  string   `json:"projectId,omitempty"`
	Directory  string   `json:"directory,omitempty"`
	Message    string   `json:"message,omitempty"`
	// Cascade 仅对 delete 生效：true 时 daemon 把 SessionIDs 扩展为
	// "自身 + 全部后代"再执行（服务端级联）。缺省 false 保持原语义——
	// 只删请求的 ID，级联与否由调用方自行决定（TUI dashboard/sidebar 在
	// 客户端收集全量后代；Favorites 视图只删选中条目；CLI delete 传 true）。
	Cascade bool `json:"cascade,omitempty"`
}

// ProgressMsg notifies subscribers that a long-running action has started.
type ProgressMsg struct {
	Type   string `json:"type"` // "progress"
	Action string `json:"action"`
	Done   bool   `json:"done"`
}

// ResultMsg reports the completion of an action.
type ResultMsg struct {
	Type    string  `json:"type"` // "result"
	Action  string  `json:"action"`
	Summary Summary `json:"summary"`
	Error   string  `json:"error,omitempty"`
}

// Summary aggregates the outcome of a batch management operation.
type Summary struct {
	Total     int      `json:"total"`
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	Results   []Result `json:"results"`
}

// Result represents a single management operation outcome.
type Result struct {
	SessionID string `json:"sessionId"`
	Action    string `json:"action"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// Icon returns a short icon + label string for the session status, suitable for TUI display.
func (s SessionStatus) Icon() string {
	switch s {
	case StatusPermission:
		// 权限确认与 agent 提问统一显示 ASK：二者语义相同——都在等用户处理。
		return "🟡 ASK"
	case StatusBusy:
		return "🔵 BUSY"
	case StatusRetry:
		return "🟠 RETRY"
	case StatusError:
		return "🔴 ERROR"
	case StatusIdle:
		return "⚪ IDLE"
	case StatusUnknown:
		return "◯ ???"
	case StatusArchived:
		return "  —"
	default:
		return "◯ ???"
	}
}

// Glyph returns only the status icon without any text label, used for the
// compact first column in the TUI (status + favorite marker). The meanings of
// each glyph are documented in the TUI Help view.
//
// 注意：所有 glyph 必须是 Emoji_Presentation 的彩色 emoji（🔵🟡🟠🔴🟢📦❔），
// 保证在任何终端都渲染为固定 2 列宽，且与 go-runewidth 的计数一致——第一列
// 用 leftAlign 按宽度填充对齐，若混入文本字形（如 ⚪/★/—/◯ 这类宽度不确定字符），
// 终端实际渲染宽度与计数不一致会导致后续列错位。
func (s SessionStatus) Glyph() string {
	switch s {
	case StatusPermission:
		return "🟡"
	case StatusBusy:
		return "🔵"
	case StatusRetry:
		return "🟠"
	case StatusError:
		return "🔴"
	case StatusIdle:
		return "🟢"
	case StatusArchived:
		return "📦"
	default:
		return "❔"
	}
}

// ---------------------------------------------------------------------------
// ViewMsg: daemon-pushed rendering data for TUI and sidebar.
// ---------------------------------------------------------------------------

// ViewMsg is broadcast by the daemon to subscribers. It contains all data
// needed to render the TUI dashboard and the OpenCode sidebar, with business
// logic (state derivation, aggregation, tree building) already applied.
type ViewMsg struct {
	Type      string        `json:"type"` // "view"
	Projects  []ViewProject `json:"projects"`
	Stats     ViewStats     `json:"stats"`
	Favorites []ViewSession `json:"favorites,omitempty"` // 预组装的收藏列表，按插入顺序
}

// ViewProject represents a project group ready for rendering.
type ViewProject struct {
	ProjectID   string        `json:"projectId"`
	Name        string        `json:"name"`
	Worktree    string        `json:"worktree"`
	TimeUpdated int64         `json:"timeUpdated"`
	RowStatus   SessionStatus `json:"rowStatus"`
	Sessions    []ViewSession `json:"sessions"`
}

// ViewSession represents a session ready for rendering.
type ViewSession struct {
	SessionID    string        `json:"sessionId"`
	Title        string        `json:"title"`
	Directory    string        `json:"directory"`
	Agent        string        `json:"agent"`
	Cost         float64       `json:"cost"`
	TimeUpdated  int64         `json:"timeUpdated"`
	TimeArchived int64         `json:"timeArchived"`
	Status       SessionStatus `json:"status"`
	RowStatus    SessionStatus `json:"rowStatus"`
	ParentID     string        `json:"parentId,omitempty"`
	HasChildren  bool          `json:"hasChildren"`
	Depth        int           `json:"depth"`
	IsFavorite   bool          `json:"isFavorite"` // 是否已收藏
	PID          int64         `json:"pid"`        // 附着的 opencode 进程 pid，0 表示未知
	TMUXPane     string        `json:"tmuxPane"`   // e.g. "%5"，空表示未附着 tmux
	TMUXSession  string        `json:"tmuxSession"`
}

// ViewStats contains global aggregates for rendering.
type ViewStats struct {
	TotalSessions  int     `json:"totalSessions"`
	ActiveSessions int     `json:"activeSessions"`
	TotalCost      float64 `json:"totalCost"`
	TotalTokens    int64   `json:"totalTokens"`
}
