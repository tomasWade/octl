// Package daemon provides the octl notification daemon that tracks session states
// via Unix socket events and periodic database sync.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tomasWade/octl/internal/db"
	"github.com/tomasWade/octl/internal/manage"
	"github.com/tomasWade/octl/internal/paths"
	"github.com/tomasWade/octl/internal/plugins"
	"github.com/tomasWade/octl/internal/shadow"
	"github.com/tomasWade/octl/internal/types"
)

const (
	dbSyncInterval  = 30 * time.Second
	eventStaleAfter = 60 * time.Second
)

// connRole classifies a Unix socket connection.
type connRole int

const (
	// roleEventSource receives no snapshots; each line is treated as an event.
	roleEventSource connRole = iota
	// roleSubscriber receives snapshot broadcasts after sending a subscribe message.
	roleSubscriber
)

// clientConn wraps a single Unix socket connection and its role.
type clientConn struct {
	conn     net.Conn
	role     connRole
	channels []string
	enc      *json.Encoder
	mu       sync.Mutex
	pid      int64 // OS pid of the event-source process, captured from the first event
}

// write sends a JSON message to the connection. Errors are ignored; the caller
// should detect failures through the next Encode error and close the conn.
func (cl *clientConn) write(v interface{}) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	_ = cl.enc.Encode(v)
}

// StateManager tracks runtime session state through live Unix socket events
// and periodic reconciliation with the SQLite database. It also serves as the
// single business-logic center for management operations requested by TUI/sidebar
// clients.
type StateManager struct {
	db               *db.DB
	mgr              *manage.Manager
	shadowDB         *shadow.DB // 影子库（nil = 未接入，走旧路径）
	retentionDays    int        // 影子库保留期（天）；0 = 永不过期
	mu               sync.RWMutex
	stateMap         map[string]*SessionState
	sessionProcess   map[string]*ProcessInfo       // sessionID -> latest process info
	pidIndex         map[int64]map[string]struct{} // pid -> set of sessionIDs
	favorites        []string                      // 有序收藏 sessionID 列表，保持插入顺序
	favoritesSet     map[string]bool               // O(1) 成员检查
	eventCh          chan []byte                   // raw JSON events from event-source connections
	snapshotCh       chan []SessionState           // in-process snapshots for integration tests (not consumed by production TUI)
	clients          []*clientConn                 // active subscriber connections
	clientsMu        sync.Mutex
	customSocketPath string        // custom socket path; empty = use default
	ln               net.Listener  // Unix socket listener, stored for Close()
	closeOnce        sync.Once     // guards Close()
	dbSyncInterval   time.Duration // period between DB syncs
	eventStaleAfter  time.Duration // age after which event state is DB-overridden
}

// NewStateManager creates a StateManager backed by the given database with
// the default Unix socket path.
func NewStateManager(database *db.DB) *StateManager {
	return NewStateManagerWithSocket(database, "")
}

// NewStateManagerWithSocket creates a StateManager with a custom Unix socket
// path. Pass an empty string to use the default path (computed at Run time).
// This is useful for tests that need to isolate the socket in a temp directory.
func NewStateManagerWithSocket(database *db.DB, socketPath string) *StateManager {
	return &StateManager{
		db:               database,
		stateMap:         make(map[string]*SessionState),
		sessionProcess:   make(map[string]*ProcessInfo),
		pidIndex:         make(map[int64]map[string]struct{}),
		favorites:        make([]string, 0),
		favoritesSet:     make(map[string]bool),
		eventCh:          make(chan []byte, 100),
		snapshotCh:       make(chan []SessionState, 10),
		clients:          make([]*clientConn, 0),
		customSocketPath: socketPath,
		dbSyncInterval:   dbSyncInterval,
		eventStaleAfter:  eventStaleAfter,
	}
}

// SetManager registers the management operation executor with the daemon.
// This must be called before the daemon accepts action requests from clients.
func (sm *StateManager) SetManager(mgr *manage.Manager) {
	sm.mgr = mgr
}

// SetShadow 接入影子库并设定保留期（天，0 = 永不过期）。
// nil 库表示不接入（测试或降级运行），相关路径退回旧行为。
func (sm *StateManager) SetShadow(sdb *shadow.DB, retentionDays int) {
	sm.shadowDB = sdb
	sm.retentionDays = retentionDays
}

// SnapshotCh returns the read-only snapshot channel for test consumption.
// Production TUI clients receive snapshots over the Unix socket instead.
func (sm *StateManager) SnapshotCh() <-chan []SessionState {
	return sm.snapshotCh
}

// Close stops the Unix socket listener and disconnects all clients. Calling
// Close multiple times is safe.
func (sm *StateManager) Close() {
	sm.closeOnce.Do(func() {
		if sm.ln != nil {
			_ = sm.ln.Close()
		}
		sm.clientsMu.Lock()
		for _, cl := range sm.clients {
			_ = cl.conn.Close()
		}
		sm.clients = sm.clients[:0]
		sm.clientsMu.Unlock()
	})
}

// socketPath 返回 daemon Unix socket 的默认路径（~/.local/share/octl/octl.sock）。
func (sm *StateManager) socketPath() (string, error) {
	p, err := paths.SocketPath()
	if err != nil {
		return "", fmt.Errorf("resolve socket path: %w", err)
	}
	return p, nil
}

// isSocketActive reports whether another process is already listening on the
// given Unix socket path. It dials with a short timeout and treats any
// successful connection as evidence of a live daemon.
func isSocketActive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// alignPluginsMu 串行化 alignPlugins（mismatch 兜底可能被多个连接并发触发）。
var alignPluginsMu sync.Mutex

// pluginsDirOverride 供测试注入插件对齐目录；空时用默认目录。
var pluginsDirOverride string

// alignPlugins 把内嵌模板的渲染结果写入默认插件目录
// （~/.config/opencode/plugins/），md5 不一致才写。两个触发点：daemon 启动
// 时、收到版本不符的 subscribe 时。由此升级流程收敛为「替换二进制 + 重启
// daemon」：磁盘立即对齐，新启动的 opencode 直接用新插件；运行中实例的
// sidebar 被拒后弹出「重启」按钮，由用户点击原地重启加载新插件（opencode
// 对 TUI 插件无热重载，1.18.30 实测）。失败只记日志不阻塞。
func alignPlugins() {
	alignPluginsMu.Lock()
	defer alignPluginsMu.Unlock()
	dir := pluginsDirOverride
	if dir == "" {
		var err error
		dir, err = plugins.DefaultOutputDir()
		if err != nil {
			log.Printf("[daemon] align plugins: resolve dir: %v", err)
			return
		}
	}
	if err := plugins.Generate(dir); err != nil {
		log.Printf("[daemon] align plugins: %v", err)
	}
}

// Run starts the state manager event loop and blocks until a fatal error
// occurs. It performs an initial DB sync, listens on a Unix socket for
// events and subscriber connections, runs a periodic DB sync every 30 s,
// and pushes state snapshots to subscribers after each batch of processing.
func (sm *StateManager) Run() error {
	var sockPath string
	if sm.customSocketPath != "" {
		sockPath = sm.customSocketPath
	} else {
		var err error
		sockPath, err = sm.socketPath()
		if err != nil {
			return fmt.Errorf("state manager: %w", err)
		}
	}

	// Ensure the socket parent directory exists.
	if err := os.MkdirAll(filepath.Dir(sockPath), 0755); err != nil {
		return fmt.Errorf("state manager: mkdir: %w", err)
	}

	// Detect an already-running daemon. A successful dial means the socket is
	// live; we must not remove it and steal connections from the existing daemon.
	if isSocketActive(sockPath) {
		return fmt.Errorf("state manager: another daemon is already listening on %s", sockPath)
	}

	// Remove any stale socket file from a previous run.
	_ = os.Remove(sockPath)

	// 启动对齐插件文件：把内嵌模板渲染结果写入默认插件目录（md5 不一致
	// 才写），保证升级二进制并重启 daemon 后磁盘立即与当前协议一致。
	// 失败只记日志（alignPlugins 内部处理），退化为人工 octl install。
	alignPlugins()

	// Start listening on the Unix socket BEFORE the initial DB sync: clients
	// (TUI/sidebar/hook) can connect and complete their subscribe immediately
	// during the whole restart window instead of hitting ECONNREFUSED while
	// the sync (whose duration grows with session count) runs. Subscribers
	// that arrive mid-sync get an empty view first; the full view is
	// broadcast below once the initial sync completes.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("state manager: listen: %w", err)
	}
	sm.ln = ln
	fmt.Fprintf(os.Stderr, "octl daemon listening on %s\n", sockPath)

	// Accept connections in a background goroutine.
	errCh := make(chan error, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				// Best-effort delivery of the accept error; a full buffer
				// means Run is already shutting down.
				select {
				case errCh <- err:
				default:
				}
				return
			}
			go sm.handleConn(conn)
		}
	}()

	defer func() { _ = ln.Close() }()
	defer func() { _ = os.Remove(sockPath) }()

	// Perform the initial database sync (non-fatal on failure: the periodic
	// sync retries and live events keep the state up to date).
	//
	// restoreState depends on stateMap being populated (stuck entries are
	// matched against sessions that still exist in the database), so it is
	// deferred until the first successful sync — here if possible, or from
	// the periodic ticker when the initial sync fails.
	initialSyncErr := sm.syncFromDB()
	if initialSyncErr != nil {
		fmt.Fprintf(os.Stderr, "state manager: initial sync failed (will retry): %v\n", initialSyncErr)
	} else {
		sm.restoreState()
	}
	// 广播首帧：覆盖同步期间到达的订阅者先拿到的空视图。
	sm.drainPending()
	sm.pushSnapshot()
	sm.pushView()

	ticker := time.NewTicker(sm.dbSyncInterval)
	defer ticker.Stop()

	// 影子库对账：启动先跑一轮（watermark=0 时即全量回填），之后周期增量；
	// 保留期清理挂在同一节拍上（内部按天节流）。
	shadowTicker := time.NewTicker(shadowSyncInterval)
	defer shadowTicker.Stop()
	sm.shadowReconcileNow()
	sm.shadowRetentionSweep(time.Now())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	for {
		select {
		case <-ctx.Done():
			// Flush persisted in-memory state before shutdown.
			sm.saveState()
			return nil

		case err := <-errCh:
			// Listener failure also exits: flush state so the two exit
			// paths stay symmetric.
			sm.saveState()
			return fmt.Errorf("state manager: listener: %w", err)

		case ev := <-sm.eventCh:
			sm.processEvent(ev)
			sm.drainPending()
			sm.pushSnapshot()
			sm.pushView()

		case <-ticker.C:
			if err := sm.syncFromDB(); err != nil {
				// DB sync errors are non-fatal; log and continue.
				fmt.Fprintf(os.Stderr, "state manager: sync: %v\n", err)
			} else if initialSyncErr != nil {
				// 初次 sync 失败时 restoreState 被推迟；首次成功后补跑
				//（stuck 恢复依赖 stateMap 已灌入 DB session）。
				initialSyncErr = nil
				sm.restoreState()
			}
			sm.drainPending()
			sm.pushSnapshot()
			sm.pushView()
			// 全量快照持久化（幂等）：收藏 + 卡住态，30s 粒度兜底。
			sm.saveState()

		case <-shadowTicker.C:
			sm.shadowReconcileNow()
			sm.shadowRetentionSweep(time.Now())
		}
	}
}

// handleConn serves a single Unix socket connection. The first line determines
// whether the peer is an event source (plugin) or a subscriber (TUI).
func (sm *StateManager) handleConn(conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[daemon] handleConn panic: %v", r)
		}
		_ = conn.Close()
	}()
	sc := bufio.NewScanner(conn)
	sc.Split(bufio.ScanLines)

	cl := &clientConn{conn: conn, enc: json.NewEncoder(conn)}

	// Read the first line to classify the connection.
	if sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())

		var msg SubscribeMsg
		if err := json.Unmarshal(line, &msg); err == nil && msg.Type == "subscribe" {
			cl.role = roleSubscriber
			cl.channels = msg.Channels
			if len(cl.channels) == 0 {
				cl.channels = []string{"snapshot"}
			}

			// 版本比对：客户端必须携带与当前协议一致的 Version，否则拒绝订阅。
			// 拒绝前触发插件对齐兜底（防文件被手改/删除导致的漂移）：写盘后仍
			// 拒绝本次订阅——旧代码客户端停止重连并在 sidebar 弹出「重启」
			// 按钮，由用户决定何时原地重启加载新插件。
			if msg.Version != plugins.ProtocolMD5() {
				log.Printf("[daemon] subscribe version mismatch: client=%s got=%s want=%s", conn.RemoteAddr(), msg.Version, plugins.ProtocolMD5())
				alignPlugins()
				cl.write(ResponseMsg{
					Type:  "response",
					Ok:    false,
					Error: VersionMismatchError,
				})
				return
			}

			sm.addClient(cl)
			log.Printf("[daemon] subscriber registered channels=%v", cl.channels)
			cl.write(SubscribedMsg{Type: "subscribed", Channels: cl.channels})
			if containsChannel(cl.channels, "snapshot") {
				sm.pushSnapshotTo(cl)
			}
			if containsChannel(cl.channels, "view") {
				sm.pushViewTo(cl)
			}
		} else {
			cl.role = roleEventSource
			if pid := parsePIDFromRaw(line); pid > 0 {
				cl.pid = pid
			}
			sm.eventCh <- line
		}
	}

	// Continue reading according to the connection role.
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())

		if cl.role == roleSubscriber {
			sm.handleSubscriberMsg(cl, line)
		} else {
			sm.eventCh <- line
		}
	}

	sm.removeClient(cl)
	if cl.pid > 0 {
		sm.onDisconnect(cl.pid)
	}
}

// addClient registers a subscriber connection.
func (sm *StateManager) addClient(cl *clientConn) {
	sm.clientsMu.Lock()
	defer sm.clientsMu.Unlock()
	sm.clients = append(sm.clients, cl)
}

// removeClient unregisters a connection.
func (sm *StateManager) removeClient(cl *clientConn) {
	sm.clientsMu.Lock()
	defer sm.clientsMu.Unlock()
	for i, c := range sm.clients {
		if c == cl {
			sm.clients = append(sm.clients[:i], sm.clients[i+1:]...)
			break
		}
	}
}

// handleSubscriberMsg handles messages from subscriber connections.
func (sm *StateManager) handleSubscriberMsg(cl *clientConn, line []byte) {
	var msg BaseMsg
	if err := json.Unmarshal(line, &msg); err != nil {
		return
	}

	switch msg.Type {
	case "ping":
		cl.write(PongMsg{Type: "pong"})

	case "action":
		var action ActionMsg
		if err := json.Unmarshal(line, &action); err != nil {
			return
		}
		sm.handleActionMsg(cl, action)

	case "request":
		var req RequestMsg
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		sm.handleRequestMsg(cl, req)
	}
}

// handleRequestMsg dispatches subscriber request messages.
func (sm *StateManager) handleRequestMsg(cl *clientConn, req RequestMsg) {
	switch req.Method {
	case "snapshot":
		cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: true, States: sm.currentSnapshot()})
	case "listSessions":
		projects, err := sm.listSessionsForSidebar()
		if err != nil {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: false, Error: err.Error()})
		} else {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: true, Projects: projects})
		}
	case "messages":
		var parts []types.MessagePart
		var err error
		// 活线看活库：永远新鲜，且反映 revert/compaction 之后的现行视图
		//（opencode 的 revert/压缩会真删消息行，影子库按档案语义保留旧
		// 内容——那是给死线的）。死线（已从 opencode 消失）才走影子库。
		if sess, serr := sm.db.GetSession(req.SessionID); serr == nil && sess != nil {
			parts, err = sm.db.GetSessionMessages(req.SessionID)
		} else if sm.shadowDB != nil {
			parts, err = sm.shadowDB.SessionMessages(req.SessionID)
			// 未收录的 session（影子库建立前就已消失的老尸体）回落活库
			// （两边都空就返回空，与旧行为一致）；HasSession 出错不掩埋。
			if err == nil && len(parts) == 0 {
				has, herr := sm.shadowDB.HasSession(req.SessionID)
				if herr != nil {
					err = herr
				} else if !has {
					parts, err = sm.db.GetSessionMessages(req.SessionID)
				}
			}
		} else {
			parts, err = sm.db.GetSessionMessages(req.SessionID)
		}
		if err != nil {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: false, Error: err.Error()})
			return
		}
		messages := make([]MessagePart, 0, len(parts))
		for _, p := range parts {
			messages = append(messages, MessagePart{
				MessageID:   p.MessageID,
				Role:        p.Role,
				Text:        p.Text,
				TimeCreated: p.TimeCreated,
			})
		}
		cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: true, Messages: messages})
	case "archiveSessions":
		refs := make([]ArchiveSessionRef, 0)
		if sm.shadowDB != nil {
			rows, err := sm.shadowDB.ListSessions()
			if err != nil {
				cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: false, Error: err.Error()})
				return
			}
			for _, r := range rows {
				refs = append(refs, ArchiveSessionRef{
					SessionID:   r.ID,
					Title:       r.Title,
					Deleted:     r.DeletedAtMs != 0,
					DeletedAtMs: r.DeletedAtMs,
				})
			}
		}
		cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: true, Archive: refs})
	case "daily":
		if req.From <= 0 || req.To <= req.From {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: false,
				Error: fmt.Sprintf("daily requires 0 < from < to (unix ms), got from=%d to=%d", req.From, req.To)})
			return
		}
		digest, err := sm.buildDailyDigest(req.From, req.To)
		if err != nil {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: false, Error: err.Error()})
		} else {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: true, Daily: digest})
		}
	case "report":
		if req.From <= 0 || req.To <= req.From {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: false,
				Error: fmt.Sprintf("report requires 0 < from < to (unix ms), got from=%d to=%d", req.From, req.To)})
			return
		}
		result, err := sm.writeReport(req.From, req.To, req.Dir)
		if err != nil {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: false, Error: err.Error()})
		} else {
			cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: true, Report: result})
		}
	default:
		cl.write(ResponseMsg{Type: "response", ID: req.ID, Ok: false,
			Error: fmt.Sprintf("unknown method %q", req.Method)})
	}
}

// listSessionsForSidebar returns the sidebar session list grouped by project,
// with each root session's status aggregated across its subsession tree.
func (sm *StateManager) listSessionsForSidebar() ([]SidebarProject, error) {
	sessions, err := sm.db.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	projects, err := sm.db.GetAllProjects()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	// Filter out archived sessions.
	activeSessions := make([]types.Session, 0, len(sessions))
	for _, s := range sessions {
		if s.TimeArchived == 0 {
			activeSessions = append(activeSessions, s)
		}
	}

	// Build parent -> children map.
	children := make(map[string][]types.Session)
	for _, s := range activeSessions {
		if s.ParentID != "" {
			children[s.ParentID] = append(children[s.ParentID], s)
		}
	}

	// Group by project.
	projectMap := make(map[string]*SidebarProject)
	for _, p := range projects {
		projectMap[p.ID] = &SidebarProject{
			ProjectID:   p.ID,
			Name:        projectName(p),
			Worktree:    p.Worktree,
			TimeUpdated: p.TimeUpdated,
			Sessions:    []SidebarSession{},
		}
	}

	for _, s := range activeSessions {
		// Only root sessions become rows; subsessions are aggregated into their root.
		if s.ParentID != "" {
			continue
		}
		sp := sm.sidebarSession(s, children)
		proj := projectMap[s.ProjectID]
		if proj == nil {
			proj = &SidebarProject{
				ProjectID:   s.ProjectID,
				Name:        s.ProjectID,
				Worktree:    s.Directory,
				TimeUpdated: s.TimeUpdated,
				Sessions:    []SidebarSession{},
			}
			projectMap[s.ProjectID] = proj
		}
		proj.Sessions = append(proj.Sessions, sp)
	}

	result := make([]SidebarProject, 0, len(projectMap))
	for _, p := range projectMap {
		sort.Slice(p.Sessions, func(i, j int) bool {
			return p.Sessions[i].TimeUpdated > p.Sessions[j].TimeUpdated
		})
		result = append(result, *p)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TimeUpdated > result[j].TimeUpdated
	})

	return result, nil
}

// handleActionMsg executes a management action requested by a subscriber and
// notifies the caller of progress and the final result. After the action
// completes, a fresh ViewMsg is pushed to all view subscribers.
func (sm *StateManager) handleActionMsg(cl *clientConn, action ActionMsg) {
	if sm.mgr == nil {
		cl.write(ResultMsg{Type: "result", Action: action.Action, Error: "manager not configured"})
		return
	}

	cl.write(ProgressMsg{Type: "progress", Action: action.Action, Done: false})

	var summary manage.Summary
	switch action.Action {
	case "delete":
		summary = sm.handleDeleteAction(action)
	case "export":
		summary = sm.handleExportAction(action)
	case "create":
		summary = sm.handleCreateAction(action)
	case "fork":
		summary = sm.handleForkAction(action)
	case "send":
		summary = sm.handleSendAction(action)
	case "favorite":
		summary = sm.handleFavoriteAction(action)
	case "unfavorite":
		summary = sm.handleUnfavoriteAction(action)
	case "purge":
		summary = sm.shadowPurgeAction(action)
	default:
		cl.write(ResultMsg{Type: "result", Action: action.Action, Error: "unknown action: " + action.Action})
		return
	}

	result := ResultMsg{
		Type:    "result",
		Action:  action.Action,
		Summary: manageSummaryToSummary(summary),
	}
	cl.write(result)

	// Push an updated view so all subscribers see the result immediately.
	sm.pushView()
}

// handleDeleteAction deletes the requested sessions and, if applicable, the project row.
// action.Cascade 为 true 时先把请求的 ID 集合扩展为"自身 + 全部后代"
// （服务端级联，见 expandSessionTree）；缺省 false 保持原语义（只删请求的
// ID，级联由调用方决定——TUI dashboard/sidebar 客户端收集全量、Favorites
// 只删选中条目），保证既有客户端行为不受影响。删除成功后同步清理收藏
// 列表中的对应 session，保证收藏数据不会被已删除 session 污染。
func (sm *StateManager) handleDeleteAction(action ActionMsg) manage.Summary {
	ids := action.SessionIDs
	if action.Cascade {
		ids = sm.expandSessionTree(ids)
	}
	// 删除前同步抢救镜像：此刻 opencode DB 行还在，是唯一确定性定格全史
	// 的机会（事件路径的抢救大概率扑空——opencode 先删行再广播）。
	// 同步执行而非 goroutine：必须赶在 BatchDelete 破坏数据之前完成。
	sm.shadowReconcileSessions(ids)
	var sessions []types.Session
	for _, id := range ids {
		sessions = append(sessions, types.Session{ID: id})
	}
	summary := sm.mgr.BatchDelete(sessions)
	if action.ProjectID != "" && action.ProjectID != "global" && summary.Failed == 0 {
		if err := sm.mgr.DeleteProject(action.ProjectID); err != nil {
			summary.Failed++
			summary.Results = append(summary.Results, manage.OpResult{
				SessionID: action.ProjectID,
				Action:    "delete",
				Success:   false,
				Error:     err.Error(),
			})
		}
	}

	// 删除成功的 session 同步从收藏中移除（剪枝）。
	sm.mu.Lock()
	var deletedOK []string
	for _, r := range summary.Results {
		if r.Success {
			sm.removeFavorite(r.SessionID)
			deletedOK = append(deletedOK, r.SessionID)
		}
	}
	sm.mu.Unlock()
	// 收藏剪枝后聚合落盘（goroutine 自取锁）。
	go sm.saveState()
	// 影子库标记删除（opencode 的 session.deleted 事件也会到，幂等）。
	go sm.shadowMarkDeleted(deletedOK)

	return summary
}

// expandSessionTree 按 DB 的 parent_id 关系把 session ID 集合扩展为
// "自身 + 全部后代"：从每个输入 ID 出发做显式栈 DFS，visited 去重保证
// 多输入交集只删一次、parentId 环不死循环；自引用（parent == self）按
// 叶子处理。DB 中不存在的输入 ID 原样保留（后续 BatchDelete 会给出对应
// 的失败条目）。ListSessions 失败时退化为只删指定 ID，不让整个 action 失败。
func (sm *StateManager) expandSessionTree(ids []string) []string {
	if len(ids) == 0 {
		return ids
	}
	sessions, err := sm.db.ListSessions()
	if err != nil {
		return ids
	}

	children := make(map[string][]string)
	for _, s := range sessions {
		if s.ParentID != "" && s.ParentID != s.ID {
			children[s.ParentID] = append(children[s.ParentID], s.ID)
		}
	}

	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		stack := []string{id}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[cur] {
				continue
			}
			seen[cur] = true
			out = append(out, cur)
			stack = append(stack, children[cur]...)
		}
	}
	return out
}

// handleExportAction exports the requested sessions to /tmp/octl-exports.
func (sm *StateManager) handleExportAction(action ActionMsg) manage.Summary {
	var sessions []types.Session
	for _, id := range action.SessionIDs {
		sessions = append(sessions, types.Session{ID: id})
	}
	dir := filepath.Join(os.TempDir(), "octl-exports")
	return sm.mgr.BatchExport(sessions, dir)
}

// handleCreateAction creates a new session with the given first message.
func (sm *StateManager) handleCreateAction(action ActionMsg) manage.Summary {
	err := sm.mgr.CreateSession(action.Directory, action.Message)
	return errorToSummary(err, "", "create")
}

// handleForkAction forks an existing session with the given message.
// action.Directory 为空时自动用 DB 中该 session 的 directory 补全，
// CLI 调用方（octl fork）无需显式传 --dir。
func (sm *StateManager) handleForkAction(action ActionMsg) manage.Summary {
	dir, err := sm.resolveActionDirectory(action.SessionID, action.Directory)
	if err != nil {
		return errorToSummary(err, action.SessionID, "fork")
	}
	err = sm.mgr.ForkSession(action.SessionID, dir, action.Message)
	return errorToSummary(err, action.SessionID, "fork")
}

// handleSendAction sends a message to an existing session.
// action.Directory 为空时自动用 DB 中该 session 的 directory 补全，
// CLI 调用方（octl send）无需显式传 --dir。
func (sm *StateManager) handleSendAction(action ActionMsg) manage.Summary {
	dir, err := sm.resolveActionDirectory(action.SessionID, action.Directory)
	if err != nil {
		return errorToSummary(err, action.SessionID, "send")
	}
	err = sm.mgr.SendMessage(action.SessionID, dir, action.Message)
	return errorToSummary(err, action.SessionID, "send")
}

// resolveActionDirectory 解析 fork/send 操作的工作目录：显式指定时直接
// 透传（不查库）；否则从 DB 读取该 session 的 directory 补全。session 不
// 存在或 directory 为空时返回错误，避免向 opencode 传递空 --dir 导致其
// 回退到 daemon 进程的 /tmp 工作目录。
func (sm *StateManager) resolveActionDirectory(sessionID, directory string) (string, error) {
	if directory != "" {
		return directory, nil
	}
	s, err := sm.db.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("resolve directory for session %s: %w", sessionID, err)
	}
	if s.Directory == "" {
		return "", fmt.Errorf("session %s has no directory on record; pass --dir explicitly", sessionID)
	}
	return s.Directory, nil
}

// handleFavoriteAction 将 action.SessionIDs 中的 session 加入收藏集合。
// 已收藏的 session 为 no-op（仍返回成功）。
// 空字符串和纯空白 sessionID 会被跳过，不加入收藏也不产生 OpResult。
// 并发安全：handleActionMsg 可能在多个 handleConn goroutine 中并发调用，
// 因此通过 sm.mu 串行化对 favorites 的写操作，与 stateMap 的并发模式保持一致。
func (sm *StateManager) handleFavoriteAction(action ActionMsg) manage.Summary {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	results := make([]manage.OpResult, 0, len(action.SessionIDs))
	for _, sid := range action.SessionIDs {
		// 跳过空字符串和纯空白 sessionID，避免污染收藏集合
		if strings.TrimSpace(sid) == "" {
			continue
		}
		if sm.favoritesSet[sid] {
			// 已收藏 → no-op，仍返回成功
			results = append(results, manage.OpResult{
				SessionID: sid,
				Action:    "favorite",
				Success:   true,
			})
		} else {
			sm.favorites = append(sm.favorites, sid)
			sm.favoritesSet[sid] = true
			results = append(results, manage.OpResult{
				SessionID: sid,
				Action:    "favorite",
				Success:   true,
			})
		}
	}
	// 变更即异步落盘（聚合到循环外：goroutine 自取锁，等本函数 Unlock
	// 后快照，批量操作只 spawn 一次，避免放大并发写竞态）。
	go sm.saveState()
	return manage.Summary{
		Total:     len(results),
		Succeeded: len(results),
		Results:   results,
	}
}

// handleUnfavoriteAction 将 action.SessionIDs 中的 session 从收藏集合中移除。
// 未收藏的 session 为 no-op（仍返回成功）。
// 空字符串和纯空白 sessionID 会被跳过，不产生 OpResult。
// 并发安全：与 handleFavoriteAction 相同，通过 sm.mu 串行化写操作。
func (sm *StateManager) handleUnfavoriteAction(action ActionMsg) manage.Summary {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	results := make([]manage.OpResult, 0, len(action.SessionIDs))
	for _, sid := range action.SessionIDs {
		// 跳过空字符串和纯空白 sessionID
		if strings.TrimSpace(sid) == "" {
			continue
		}
		if !sm.favoritesSet[sid] {
			// 未收藏 → no-op，仍返回成功
			results = append(results, manage.OpResult{
				SessionID: sid,
				Action:    "unfavorite",
				Success:   true,
			})
		} else {
			// 复用 removeFavorite helper 执行保序删除
			sm.removeFavorite(sid)
			results = append(results, manage.OpResult{
				SessionID: sid,
				Action:    "unfavorite",
				Success:   true,
			})
		}
	}
	// 与 handleFavoriteAction 对称：聚合落盘。
	go sm.saveState()
	return manage.Summary{
		Total:     len(results),
		Succeeded: len(results),
		Results:   results,
	}
}

// removeFavorite 从收藏集合中移除指定的 sessionID。若该 sessionID 不在收藏中
// 则直接返回（幂等）。调用方必须持有 sm.mu 写锁；持久化由调用方聚合触发。
func (sm *StateManager) removeFavorite(sid string) {
	if !sm.favoritesSet[sid] {
		return
	}
	// 从有序 slice 中保序删除
	for i, f := range sm.favorites {
		if f == sid {
			sm.favorites = append(sm.favorites[:i], sm.favorites[i+1:]...)
			break
		}
	}
	delete(sm.favoritesSet, sid)
}

// errorToSummary converts a single error into a manage.Summary for uniform handling.
func errorToSummary(err error, sessionID, action string) manage.Summary {
	if err == nil {
		return manage.Summary{Total: 1, Succeeded: 1, Results: []manage.OpResult{{SessionID: sessionID, Action: action, Success: true}}}
	}
	return manage.Summary{Total: 1, Succeeded: 0, Failed: 1, Results: []manage.OpResult{{SessionID: sessionID, Action: action, Success: false, Error: err.Error()}}}
}

// manageSummaryToSummary converts a manage.Summary into the daemon wire format.
func manageSummaryToSummary(s manage.Summary) Summary {
	results := make([]Result, len(s.Results))
	for i, r := range s.Results {
		results[i] = Result{
			SessionID: r.SessionID,
			Action:    r.Action,
			Success:   r.Success,
			Error:     r.Error,
		}
	}
	return Summary{
		Total:     s.Total,
		Succeeded: s.Succeeded,
		Failed:    s.Failed,
		Results:   results,
	}
}

// projectName returns the display name for a project.
func projectName(p types.Project) string {
	if p.Name != "" {
		return p.Name
	}
	if p.ID == "global" {
		return "global"
	}
	return p.ID
}

// sidebarSession builds a SidebarSession for a root session, aggregating
// the statuses of all its subsessions.
func (sm *StateManager) sidebarSession(s types.Session, children map[string][]types.Session) SidebarSession {
	statuses := make(map[SessionStatus]int)
	sm.collectStatuses(s, children, statuses)

	return SidebarSession{
		SessionID:   s.ID,
		Title:       s.Title,
		TimeUpdated: s.TimeUpdated,
		Statuses:    statuses,
		RowStatus:   dominantStatus(statuses),
	}
}

// collectStatuses recursively collects status counts for a session and its subsessions.
func (sm *StateManager) collectStatuses(s types.Session, children map[string][]types.Session, statuses map[SessionStatus]int) {
	status := sm.statusForSession(s)
	statuses[status]++
	for _, child := range children[s.ID] {
		sm.collectStatuses(child, children, statuses)
	}
}

// statusForSession returns the current status for a single session.
func (sm *StateManager) statusForSession(s types.Session) SessionStatus {
	sm.mu.RLock()
	state, exists := sm.stateMap[s.ID]
	sm.mu.RUnlock()
	if exists && state.Source == SourceEvent {
		if time.Since(state.LastEventAt) < sm.eventStaleAfter {
			return state.Status
		}
		// PERMISSION / ERROR 是 opencode 内存态（权限请求、提问、错误信息都不落
		// 库），与 syncFromDB 的接管豁免保持一致（见 SourceEvent 分支）：否则等待
		// 确认/回答超过 60s 的 session 会在显示路径被 deriveFromDB 错误降级为
		// BUSY/IDLE，"等待处理"徽标丢失。释放只能靠事件：
		// PERMISSION ← permission.replied / question.replied / question.rejected /
		//              session.idle / session.error
		// ERROR      ← session.idle / 下一个状态事件
		if state.Status == StatusPermission || state.Status == StatusError {
			return state.Status
		}
	}
	return sm.deriveFromDB(s)
}

// dominantStatus returns the highest-priority status from a status histogram.
func dominantStatus(statuses map[SessionStatus]int) SessionStatus {
	order := []SessionStatus{StatusError, StatusPermission, StatusRetry, StatusBusy, StatusIdle, StatusUnknown}
	for _, st := range order {
		if statuses[st] > 0 {
			return st
		}
	}
	return StatusUnknown
}

// drainPending drains all events currently buffered in eventCh without
// blocking. It is called after processing an event or after a DB sync.
func (sm *StateManager) drainPending() {
	for {
		select {
		case ev := <-sm.eventCh:
			sm.processEvent(ev)
		default:
			return
		}
	}
}

// pushSnapshot sends a deep-copy snapshot of stateMap values to the test
// channel and to all subscriber connections.
func (sm *StateManager) pushSnapshot() {
	snap := sm.currentSnapshot()

	select {
	case sm.snapshotCh <- snap:
	default:
	}

	sm.broadcastSnapshot(snap)
}

// currentSnapshot returns a value-copy snapshot of the current stateMap.
func (sm *StateManager) currentSnapshot() []SessionState {
	sm.mu.RLock()
	snap := make([]SessionState, 0, len(sm.stateMap))
	for _, s := range sm.stateMap {
		snap = append(snap, *s)
	}
	sm.mu.RUnlock()
	return snap
}

// pushSnapshotTo sends the current snapshot to a single subscriber.
func (sm *StateManager) pushSnapshotTo(cl *clientConn) {
	cl.write(SnapshotMsg{Type: "snapshot", States: sm.currentSnapshot()})
}

// broadcastSnapshot sends a snapshot to all subscriber connections that
// subscribed to the "snapshot" channel. Failed connections are closed and
// removed from the registry. Clients that do not subscribe to the snapshot
// channel are preserved so they continue to receive their own channel pushes.
func (sm *StateManager) broadcastSnapshot(snap []SessionState) {
	msg := SnapshotMsg{Type: "snapshot", States: snap}

	sm.clientsMu.Lock()
	defer sm.clientsMu.Unlock()

	alive := make([]*clientConn, 0, len(sm.clients))
	for _, cl := range sm.clients {
		if cl.role != roleSubscriber {
			continue
		}
		if containsChannel(cl.channels, "snapshot") {
			cl.mu.Lock()
			err := cl.enc.Encode(msg)
			cl.mu.Unlock()
			if err != nil {
				_ = cl.conn.Close()
				continue
			}
		}
		alive = append(alive, cl)
	}
	sm.clients = alive
}

// buildView constructs a ViewMsg containing all projects, sessions, and stats,
// with state derivation and aggregation already applied.
func (sm *StateManager) buildView() (*ViewMsg, error) {
	sessions, err := sm.db.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	projects, err := sm.db.GetAllProjects()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	// Build parent -> children map for the session tree.
	children := make(map[string][]types.Session)
	for _, s := range sessions {
		if s.ParentID != "" {
			children[s.ParentID] = append(children[s.ParentID], s)
		}
	}

	// Compute the display status for every session.
	statusMap := make(map[string]SessionStatus, len(sessions))
	for _, s := range sessions {
		statusMap[s.ID] = sm.statusForSession(s)
	}

	// Compute aggregated rowStatus for each root session.
	rowStatusMap := make(map[string]SessionStatus)
	for _, s := range sessions {
		if s.ParentID == "" {
			statuses := make(map[SessionStatus]int)
			collectViewStatuses(s, children, statusMap, statuses)
			rowStatusMap[s.ID] = dominantStatus(statuses)
		}
	}

	// Group sessions by project. Include all projects from DB, plus any orphan
	// sessions that reference a project not yet in the DB.
	projectMap := make(map[string]*ViewProject)
	for _, p := range projects {
		projectMap[p.ID] = &ViewProject{
			ProjectID:   p.ID,
			Name:        projectName(p),
			Worktree:    p.Worktree,
			TimeUpdated: p.TimeUpdated,
			Sessions:    []ViewSession{},
		}
	}

	for _, s := range sessions {
		// Render root sessions and their nested subsessions under the project.
		if s.ParentID != "" {
			continue
		}
		proj := projectMap[s.ProjectID]
		if proj == nil {
			proj = &ViewProject{
				ProjectID:   s.ProjectID,
				Name:        s.ProjectID,
				Worktree:    s.Directory,
				TimeUpdated: s.TimeUpdated,
				Sessions:    []ViewSession{},
			}
			projectMap[s.ProjectID] = proj
		}
		addViewSession(proj, s, children, statusMap, rowStatusMap, 1)
	}

	// Compute project row status from root session row statuses.
	result := make([]ViewProject, 0, len(projectMap))
	for _, p := range projectMap {
		if len(p.Sessions) > 0 {
			statuses := make(map[SessionStatus]int)
			for _, s := range p.Sessions {
				if s.ParentID == "" {
					statuses[s.RowStatus]++
				}
			}
			p.RowStatus = dominantStatus(statuses)
		}
		result = append(result, *p)
	}

	// Sort projects by time updated descending.
	sort.Slice(result, func(i, j int) bool {
		return result[i].TimeUpdated > result[j].TimeUpdated
	})

	// 注入收藏数据：逐 session 标记 IsFavorite，并按 sm.favorites 插入顺序组装 Favorites 列表。
	// 同一 RLock 段快照 sessionProcess（session→pid/tmux 映射），构建后在同一循环注入，
	// 供 sidebar 跳转使用（已确认的合流方式：与收藏注入共用快照-注入模式）。
	// 读取 favorites / favoritesSet / sessionProcess 使用 RLock 保护，与
	// handleFavoriteAction / handleUnfavoriteAction / updateProcessInfo 的写锁互斥。
	// 先快照收藏集合与进程映射以避免在整个构建过程中持锁，然后按快照注入。
	sm.mu.RLock()
	favOrder := make([]string, len(sm.favorites))
	copy(favOrder, sm.favorites)
	favSet := make(map[string]bool, len(sm.favoritesSet))
	for k, v := range sm.favoritesSet {
		favSet[k] = v
	}
	procSnap := make(map[string]*ProcessInfo, len(sm.sessionProcess))
	for k, v := range sm.sessionProcess {
		procSnap[k] = v
	}
	sm.mu.RUnlock()

	// 逐 session 注入 IsFavorite 标记与 pid/tmux 附着信息。
	for i := range result {
		for j := range result[i].Sessions {
			sid := result[i].Sessions[j].SessionID
			result[i].Sessions[j].IsFavorite = favSet[sid]
			if info, ok := procSnap[sid]; ok {
				result[i].Sessions[j].PID = info.PID
				result[i].Sessions[j].TMUXPane = info.TMUXPane
				result[i].Sessions[j].TMUXSession = info.TMUXSession
			}
		}
	}

	// 按插入顺序组装 Favorites 列表，跳过孤儿收藏（session 已不存在于当前视图）。
	favList := make([]ViewSession, 0, len(favOrder))
	for _, sid := range favOrder {
		if vs, ok := findSessionInProjects(result, sid); ok {
			favList = append(favList, vs)
		}
	}

	// Global stats.
	stats := ViewStats{}
	for _, s := range sessions {
		stats.TotalSessions++
		if s.TimeArchived == 0 {
			stats.ActiveSessions++
		}
		stats.TotalCost += s.Cost
		stats.TotalTokens += s.TokensInput + s.TokensOutput + s.TokensReasoning + s.TokensCacheRead
	}

	return &ViewMsg{
		Type:      "view",
		Projects:  result,
		Stats:     stats,
		Favorites: favList,
	}, nil
}

// collectViewStatuses recursively collects status counts for a session and its
// subsessions, using the pre-computed per-session status map.
func collectViewStatuses(s types.Session, children map[string][]types.Session, statusMap map[string]SessionStatus, statuses map[SessionStatus]int) {
	statuses[statusMap[s.ID]]++
	for _, child := range children[s.ID] {
		collectViewStatuses(child, children, statusMap, statuses)
	}
}

// addViewSession appends a ViewSession for s and recursively appends its
// children. Children are sorted by time updated descending before recursion.
func addViewSession(proj *ViewProject, s types.Session, children map[string][]types.Session, statusMap, rowStatusMap map[string]SessionStatus, depth int) {
	vs := ViewSession{
		SessionID:    s.ID,
		Title:        s.Title,
		Directory:    s.Directory,
		Agent:        s.Agent,
		Cost:         s.Cost,
		TimeUpdated:  s.TimeUpdated,
		TimeArchived: s.TimeArchived,
		Status:       statusMap[s.ID],
		ParentID:     s.ParentID,
		HasChildren:  len(children[s.ID]) > 0,
		Depth:        depth,
	}
	if s.ParentID == "" {
		vs.RowStatus = rowStatusMap[s.ID]
	} else {
		vs.RowStatus = statusMap[s.ID]
	}
	proj.Sessions = append(proj.Sessions, vs)

	childSessions := children[s.ID]
	sort.Slice(childSessions, func(i, j int) bool {
		return childSessions[i].TimeUpdated > childSessions[j].TimeUpdated
	})
	for _, child := range childSessions {
		addViewSession(proj, child, children, statusMap, rowStatusMap, depth+1)
	}
}

// findSessionInProjects 在 projects 列表中线性查找指定 sessionID 的 ViewSession，
// 返回找到的 ViewSession 值拷贝和 true，未找到返回零值和 false。
// 值返回消除指针别名脆弱性——调用方获得独立拷贝，不受后续 projects 修改影响。
// 该函数为纯函数，不涉及任何共享状态，无需加锁。
func findSessionInProjects(projects []ViewProject, sessionID string) (ViewSession, bool) {
	for i := range projects {
		for j := range projects[i].Sessions {
			if projects[i].Sessions[j].SessionID == sessionID {
				return projects[i].Sessions[j], true
			}
		}
	}
	return ViewSession{}, false
}

// pushView builds the current view and broadcasts it to all subscribers.
// Errors are logged but not propagated, keeping the event loop alive.
func (sm *StateManager) pushView() {
	view, err := sm.buildView()
	if err != nil {
		fmt.Fprintf(os.Stderr, "state manager: build view: %v\n", err)
		return
	}
	log.Printf("[daemon] pushView projects=%d stats=%+v", len(view.Projects), view.Stats)
	for _, p := range view.Projects {
		for _, s := range p.Sessions {
			log.Printf("[daemon]   session=%s status=%s rowStatus=%s", s.SessionID, s.Status, s.RowStatus)
		}
	}
	sm.broadcastView(view)
}

// pushViewTo sends the current view to a single subscriber.
func (sm *StateManager) pushViewTo(cl *clientConn) {
	view, err := sm.buildView()
	if err != nil {
		fmt.Fprintf(os.Stderr, "state manager: build view: %v\n", err)
		return
	}
	log.Printf("[daemon] pushViewTo client channels=%v projects=%d", cl.channels, len(view.Projects))
	cl.write(*view)
}

// broadcastView sends a ViewMsg to all subscriber connections that subscribed
// to the "view" channel. Failed connections are closed and removed from the
// registry.
func (sm *StateManager) broadcastView(view *ViewMsg) {
	sm.clientsMu.Lock()
	defer sm.clientsMu.Unlock()

	log.Printf("[daemon] broadcastView clients=%d", len(sm.clients))
	alive := make([]*clientConn, 0, len(sm.clients))
	for _, cl := range sm.clients {
		if cl.role != roleSubscriber {
			continue
		}
		if containsChannel(cl.channels, "view") {
			cl.mu.Lock()
			err := cl.enc.Encode(view)
			cl.mu.Unlock()
			if err != nil {
				_ = cl.conn.Close()
				continue
			}
		}
		alive = append(alive, cl)
	}
	sm.clients = alive
}

// containsChannel reports whether channels contains the given channel name.
func containsChannel(channels []string, name string) bool {
	for _, c := range channels {
		if c == name {
			return true
		}
	}
	return false
}

// getOrCreateState returns the existing SessionState for the given session
// ID, or creates a new zero-valued one. The caller MUST hold sm.mu write
// lock.
func (sm *StateManager) getOrCreateState(id string) *SessionState {
	s, ok := sm.stateMap[id]
	if !ok {
		s = &SessionState{SessionID: id}
		sm.stateMap[id] = s
	}
	return s
}

// updateProcessInfo records the OS/tmux context for a session and indexes it
// by pid so it can be cleared when the source process disconnects. The caller
// MUST hold sm.mu write lock.
func (sm *StateManager) updateProcessInfo(sid string, pid int64, pane, session string, seen time.Time) {
	if sid == "" || pid <= 0 {
		return
	}
	if sm.sessionProcess == nil {
		sm.sessionProcess = make(map[string]*ProcessInfo)
	}
	if sm.pidIndex == nil {
		sm.pidIndex = make(map[int64]map[string]struct{})
	}

	// If the session previously reported a different pid, remove the old mapping.
	if old, ok := sm.sessionProcess[sid]; ok && old.PID != pid {
		if oldSet, ok := sm.pidIndex[old.PID]; ok {
			delete(oldSet, sid)
			if len(oldSet) == 0 {
				delete(sm.pidIndex, old.PID)
			}
		}
	}

	info := &ProcessInfo{
		PID:         pid,
		TMUXPane:    pane,
		TMUXSession: session,
		LastSeenAt:  seen.UnixMilli(),
	}
	sm.sessionProcess[sid] = info

	if sm.pidIndex[pid] == nil {
		sm.pidIndex[pid] = make(map[string]struct{})
	}
	sm.pidIndex[pid][sid] = struct{}{}

	// Keep the in-memory session state in sync.
	if state, ok := sm.stateMap[sid]; ok {
		state.ProcessInfo = info
	}
}

// onDisconnect clears all session process mappings tied to a given pid. It is
// called when an event-source connection closes.
func (sm *StateManager) onDisconnect(pid int64) {
	if pid <= 0 {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.pidIndex == nil {
		return
	}
	sids, ok := sm.pidIndex[pid]
	if !ok {
		return
	}
	for sid := range sids {
		if state, ok := sm.stateMap[sid]; ok {
			state.ProcessInfo = nil
		}
		delete(sm.sessionProcess, sid)
	}
	delete(sm.pidIndex, pid)
}

// rawEvent mirrors the JSON wire format produced by the opencode forwarder.
type rawEvent struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	RawTop     map[string]interface{} `json:"-"`
}

// processEvent unmarshals a raw JSON byte slice and applies the resulting
// state transition to stateMap. It tolerates both the documented
// `properties.*` wrapper and flat top-level fields, because different opencode
// versions emit slightly different shapes.
func (sm *StateManager) processEvent(raw []byte) {
	var top map[string]interface{}
	if err := json.Unmarshal(raw, &top); err != nil {
		return // silently drop malformed events
	}
	typeStr, _ := top["type"].(string)
	var props map[string]interface{}
	if p, ok := top["properties"].(map[string]interface{}); ok {
		props = p
	}
	evt := rawEvent{Type: typeStr, Properties: props, RawTop: top}

	log.Printf("[daemon] received event type=%s raw=%s", evt.Type, string(raw))

	switch evt.Type {
	case "session.status":
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		statusType := eventStatusType(evt)
		seen := time.Now()
		sm.mu.Lock()
		state := sm.getOrCreateState(sid)
		state.Status = mapStatus(statusType)
		state.Source = SourceEvent
		state.LastEventAt = seen
		if title := eventStr(evt, "title"); title != "" {
			state.Title = title
		}
		if pid := eventStr(evt, "projectID"); pid != "" {
			state.ProjectID = pid
		}
		state.Tombstone = false
		sm.updateProcessInfo(sid, eventPID(evt), eventStr(evt, "tmuxPane"), eventStr(evt, "tmuxSession"), seen)
		sm.mu.Unlock()

	case "session.idle":
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		seen := time.Now()
		sm.mu.Lock()
		state := sm.getOrCreateState(sid)
		state.Status = StatusIdle
		state.Source = SourceEvent
		state.LastEventAt = seen
		state.ErrorMsg = ""
		state.Tombstone = false
		sm.updateProcessInfo(sid, eventPID(evt), eventStr(evt, "tmuxPane"), eventStr(evt, "tmuxSession"), seen)
		sm.mu.Unlock()
		// 回合结束即定格：把刚完成的对话增量镜像进影子库。
		go sm.shadowReconcileSessions([]string{sid})

	case "session.created":
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		seen := time.Now()
		sm.mu.Lock()
		state := sm.getOrCreateState(sid)
		state.Status = StatusUnknown
		state.Source = SourceEvent
		state.LastEventAt = seen
		if title := eventStr(evt, "title"); title != "" {
			state.Title = title
		}
		if pid := eventStr(evt, "projectID"); pid != "" {
			state.ProjectID = pid
		}
		state.Tombstone = false
		sm.updateProcessInfo(sid, eventPID(evt), eventStr(evt, "tmuxPane"), eventStr(evt, "tmuxSession"), seen)
		sm.mu.Unlock()
		// 新生即镜像：压缩"创建后、对账前被删"的抢救盲区。
		go sm.shadowReconcileSessions([]string{sid})

	case "session.deleted":
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		sm.mu.Lock()
		delete(sm.stateMap, sid)
		sm.removeFavorite(sid) // 实时剪枝：hook 活跃时被删除的 session 同步移出收藏
		sm.mu.Unlock()
		go sm.saveState()
		// 先尽力抢救（opencode 通常已删 DB，扑空无害），再标记删除时刻。
		go func() {
			sm.shadowReconcileSessions([]string{sid})
			sm.shadowMarkDeleted([]string{sid})
		}()

	case "session.error":
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		seen := time.Now()
		sm.mu.Lock()
		state := sm.getOrCreateState(sid)
		state.Status = StatusError
		state.Source = SourceEvent
		state.LastEventAt = seen
		state.ErrorMsg = eventStr(evt, "error")
		state.Tombstone = false
		sm.updateProcessInfo(sid, eventPID(evt), eventStr(evt, "tmuxPane"), eventStr(evt, "tmuxSession"), seen)
		sm.mu.Unlock()

	case "permission.updated", "permission.asked":
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		seen := time.Now()
		sm.mu.Lock()
		state := sm.getOrCreateState(sid)
		state.Status = StatusPermission
		state.Source = SourceEvent
		state.LastEventAt = seen
		state.PermType = eventStr(evt, "permission")
		if state.PermType == "" {
			state.PermType = eventStr(evt, "permType")
		}
		state.PermTitle = eventStr(evt, "permTitle")
		state.Tombstone = false
		sm.updateProcessInfo(sid, eventPID(evt), eventStr(evt, "tmuxPane"), eventStr(evt, "tmuxSession"), seen)
		sm.mu.Unlock()

	case "question.asked":
		// opencode 的 question 工具：agent 向用户提问并阻塞等待回答。该等待态
		// 不体现在 session.status（只有 idle/retry/busy 三种），只能靠本事件感知。
		// 与权限确认同义复用 PERMISSION（UI 统一显示 🟡 ASK，等待用户处理）。
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		seen := time.Now()
		sm.mu.Lock()
		state := sm.getOrCreateState(sid)
		state.Status = StatusPermission
		state.Source = SourceEvent
		state.LastEventAt = seen
		state.Tombstone = false
		sm.updateProcessInfo(sid, eventPID(evt), eventStr(evt, "tmuxPane"), eventStr(evt, "tmuxSession"), seen)
		sm.mu.Unlock()

	case "permission.replied", "question.replied", "question.rejected":
		// 权限已确认 / 提问已回答或被忽略后 agent 继续生成，先标记 BUSY；
		// 若实际转入空闲，后续 session.idle 事件会修正。question 路径没有
		// PermType/PermTitle，清空操作是幂等无害的。
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		seen := time.Now()
		sm.mu.Lock()
		state := sm.getOrCreateState(sid)
		state.Status = StatusBusy
		state.Source = SourceEvent
		state.LastEventAt = seen
		state.PermType = ""
		state.PermTitle = ""
		state.Tombstone = false
		sm.updateProcessInfo(sid, eventPID(evt), eventStr(evt, "tmuxPane"), eventStr(evt, "tmuxSession"), seen)
		sm.mu.Unlock()

	case "session.compacted":
		sid := eventSID(evt)
		if sid == "" {
			return
		}
		seen := time.Now()
		sm.mu.Lock()
		state := sm.getOrCreateState(sid)
		state.Status = StatusBusy
		state.Source = SourceEvent
		state.LastEventAt = seen
		state.Tombstone = false
		sm.updateProcessInfo(sid, eventPID(evt), eventStr(evt, "tmuxPane"), eventStr(evt, "tmuxSession"), seen)
		sm.mu.Unlock()
	}
}

// strProp extracts a string value from a property map, returning "" if the
// key is missing or the value is not a string.
func strProp(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// nestedStrProp extracts a string value from a nested object property, e.g.
// status.type from {"status": {"type": "idle"}}. Returns "" if any part of
// the path is missing or the leaf value is not a string.
func nestedStrProp(m map[string]interface{}, outer, inner string) string {
	v, ok := m[outer]
	if !ok {
		return ""
	}
	innerMap, ok := v.(map[string]interface{})
	if !ok {
		return ""
	}
	iv, ok := innerMap[inner]
	if !ok {
		return ""
	}
	s, _ := iv.(string)
	return s
}

// eventSID extracts a session ID from an event, tolerating both the
// `properties.sessionID` wrapper and top-level `sessionID` / `id` fields.
func eventSID(evt rawEvent) string {
	if s := strProp(evt.Properties, "sessionID"); s != "" {
		return s
	}
	if s := strProp(evt.Properties, "id"); s != "" {
		return s
	}
	if s := strProp(evt.RawTop, "sessionID"); s != "" {
		return s
	}
	return strProp(evt.RawTop, "id")
}

// eventStr extracts a string field from an event, preferring the properties
// wrapper and falling back to top-level fields.
func eventStr(evt rawEvent, key string) string {
	if s := strProp(evt.Properties, key); s != "" {
		return s
	}
	return strProp(evt.RawTop, key)
}

// eventStatusType extracts the status type from a session.status event,
// accepting both {"status":{"type":"busy"}} and flat "busy" shapes.
func eventStatusType(evt rawEvent) string {
	if s := nestedStrProp(evt.Properties, "status", "type"); s != "" {
		return s
	}
	if s := strProp(evt.Properties, "status"); s != "" {
		return s
	}
	if s := nestedStrProp(evt.RawTop, "status", "type"); s != "" {
		return s
	}
	return strProp(evt.RawTop, "status")
}

// eventPID extracts the process pid from an event, preferring the properties
// wrapper and falling back to top-level fields. It tolerates numeric and
// string representations.
func eventPID(evt rawEvent) int64 {
	var raw interface{}
	if v, ok := evt.Properties["pid"]; ok {
		raw = v
	} else if v, ok := evt.RawTop["pid"]; ok {
		raw = v
	}
	switch v := raw.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	}
	return 0
}

// parsePIDFromRaw extracts a pid from a raw JSON event line without fully
// decoding the event. Used by handleConn to tag an event-source connection.
func parsePIDFromRaw(raw []byte) int64 {
	var top map[string]interface{}
	if err := json.Unmarshal(raw, &top); err != nil {
		return 0
	}
	var props map[string]interface{}
	if p, ok := top["properties"].(map[string]interface{}); ok {
		props = p
	}
	return eventPID(rawEvent{Properties: props, RawTop: top})
}

// mapStatus converts a status type string from the opencode event protocol
// to a SessionStatus constant.
func mapStatus(s string) SessionStatus {
	switch s {
	case "idle":
		return StatusIdle
	case "busy", "thinking":
		return StatusBusy
	case "permission":
		return StatusPermission
	case "retry":
		return StatusRetry
	case "error":
		return StatusError
	default:
		return StatusUnknown
	}
}

// syncFromDB reconciles stateMap with the current database contents.
//
// DB sync rules (for each DB session):
//   - Not in memory: create new entry with status=deriveFromDB, source=DB.
//   - In memory with source=EVENT and LastEventAt < 60s: keep event status,
//     update title/projectID.
//   - In memory with source=EVENT and LastEventAt >= 60s: DB takeover
//     (derive status from DB and switch source to DB). Exceptions: stale
//     PERMISSION and ERROR states are kept — permission requests and error
//     messages live only in opencode's memory, so the DB cannot reproduce
//     them; they are cleared by events (permission.replied / session.idle /
//     session.error / next status event) instead.
//   - In memory with source=DB: refresh status from DB.
//
// Sessions present in memory but absent from the DB result are marked as
// tombstones. Tombstoned sessions are removed on the second consecutive
// sync cycle where they remain absent.
func (sm *StateManager) syncFromDB() error {
	sessions, err := sm.db.ListSessions()
	if err != nil {
		return err
	}

	// 批量预取全部 session 的末条消息（一条 SQL，消逐条查询的 N+1）。
	// 失败非致命：降级为"无消息"（UNKNOWN 派生），与单条查询失败的语义
	// 一致，30s 周期会重试。
	lastMsgs, err := sm.db.GetAllLastMessages()
	if err != nil {
		fmt.Fprintf(os.Stderr, "state manager: batch last-message fetch failed (statuses degrade to UNKNOWN this cycle): %v\n", err)
		lastMsgs = nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Build the set of session IDs currently in the DB.
	dbIDs := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		dbIDs[s.ID] = true
		existing, exists := sm.stateMap[s.ID]

		if !exists {
			// Rule: not in memory → create new DB-derived entry.
			sm.stateMap[s.ID] = &SessionState{
				SessionID:  s.ID,
				Status:     deriveStatusFromLastMessage(s.TimeArchived, lastMsgs[s.ID]),
				Source:     SourceDB,
				LastSyncAt: time.Now(),
				Title:      s.Title,
				ProjectID:  s.ProjectID,
			}
			continue
		}

		// Always refresh title and project ID from the DB.
		existing.Title = s.Title
		existing.ProjectID = s.ProjectID
		existing.LastSyncAt = time.Now()
		existing.Tombstone = false

		switch existing.Source {
		case SourceEvent:
			if time.Since(existing.LastEventAt) >= sm.eventStaleAfter {
				// PERMISSION / ERROR 是交互与异常态：权限请求、提问和错误信息
				// 只存在于 opencode 内存中，DB 里没有持久化痕迹，DB 接管无法还原
				// 它们。若不豁免，长时间等待确认会被推导成 BUSY/IDLE（"等待确
				// 认"徽标丢失），出错会被推导成 BUSY/IDLE（错误显示丢失，甚
				// 至看起来像还在干活）。保持这两种状态直到对应事件将其解除：
				// PERMISSION ← permission.replied / question.replied / question.rejected /
				//              session.idle / session.error（权限确认与提问同义复用）
				// ERROR      ← session.idle（清 ErrorMsg）/ 下一个状态事件
				if existing.Status != StatusPermission && existing.Status != StatusError {
					// Event state is stale → DB takeover.
					existing.Status = deriveStatusFromLastMessage(s.TimeArchived, lastMsgs[s.ID])
					existing.Source = SourceDB
					existing.ErrorMsg = ""
					existing.PermType = ""
					existing.PermTitle = ""
				}
			}
			// Otherwise keep the event-derived status.
		case SourceDB:
			existing.Status = deriveStatusFromLastMessage(s.TimeArchived, lastMsgs[s.ID])
		}
	}

	// Tombstone sweep: sessions in memory that are not in the DB.
	var tombstoneConfirmed []string
	for id, state := range sm.stateMap {
		if !dbIDs[id] {
			if state.Tombstone {
				delete(sm.stateMap, id)
				tombstoneConfirmed = append(tombstoneConfirmed, id)
			} else {
				state.Tombstone = true
			}
		}
	}
	// 连续两个周期缺席 → 判定外部删除，影子库只做标记不删行（goroutine
	// 逃出锁作用域；影子库自身并发安全）。
	if len(tombstoneConfirmed) > 0 {
		go sm.shadowMarkDeleted(tombstoneConfirmed)
	}

	// 影子库 session 镜像：把刚读到的新鲜全量行喂进去（goroutine 逃锁）。
	// 归档（opencode setArchived 不 bump time_updated）与改标题等无水位
	// 的字段变化靠这条 30s 路径收敛——影子库的 daily 归档分类依赖它。
	go sm.shadowUpsertLiveSessions(sessions)

	// 收藏剪枝：外部删除（如 opencode CLI）的 session 在 30 s DB 同步周期内被
	// 检测到，对应的收藏 ID 同步移除。
	toRemove := make([]string, 0)
	for _, sid := range sm.favorites {
		if !dbIDs[sid] {
			toRemove = append(toRemove, sid)
		}
	}
	for _, sid := range toRemove {
		sm.removeFavorite(sid)
	}
	// 注意：此处不触发 saveState。syncFromDB 的两个生产调用方——初次
	// sync（restoreState 之前保存会把恢复前的空状态落盘，静默清空收藏，
	// review 发现的启动竞态）与 30s ticker（返回后已有同步 saveState）——
	// 均不需要这里的异步保存；剪枝结果由 ticker 快照兜底。

	return nil
}

// deriveFromDB determines the initial SessionStatus from a database record
// by querying the session's last message (single-session path, used by the
// display path statusForSession; the DB-sync path uses the batch variant).
//
//   - If the session has a non-zero TimeArchived → ARCHIVED.
//   - If the last message role is "assistant":
//     -- with a non-zero time.completed → IDLE (the AI is waiting).
//     -- with time.completed absent/zero → BUSY (message still generating,
//     e.g. a long-running shell command).
//   - Otherwise → UNKNOWN.
func (sm *StateManager) deriveFromDB(s types.Session) SessionStatus {
	if s.TimeArchived > 0 {
		return StatusArchived
	}
	role, completed, err := sm.db.GetLastMessageRoleCompleted(s.ID)
	if err != nil {
		return StatusUnknown
	}
	return deriveStatusFromLastMessage(0, db.LastMessage{Role: role, Completed: completed})
}

// deriveStatusFromLastMessage 是批量路径的纯派生：不查库，输入来自
// GetAllLastMessages 的预取结果（miss = 无消息 → UNKNOWN）。
func deriveStatusFromLastMessage(timeArchived int64, last db.LastMessage) SessionStatus {
	if timeArchived > 0 {
		return StatusArchived
	}
	if last.Role == "assistant" {
		if last.Completed == 0 {
			return StatusBusy
		}
		return StatusIdle
	}
	return StatusUnknown
}
