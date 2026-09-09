package tui

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tomasWade/octl/internal/daemon"
	"github.com/tomasWade/octl/internal/tui/views"
)

// initTUILog 把 log 输出重定向到 TUI 专属日志文件。必须在 TUI 启动路径
// （New）里调用而非 init()：octl 单二进制多模式，daemon 进程同样 import
// 本包，init() 里的全局 SetOutput 会把 daemon 的所有 log.Printf 吞进
// TUI 日志文件，journalctl 下 daemon 日志全部丢失。
func initTUILog() {
	f, err := os.OpenFile("/tmp/octl-tui.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		log.SetOutput(f)
	}
}

// daemonOnlineMsg 在 TUI 成功连接到 daemon 时发送。
type daemonOnlineMsg struct {
	client *daemon.SocketClient
}

// daemonOfflineMsg 在 TUI 与 daemon 断开连接时发送。
type daemonOfflineMsg struct {
	err error
}

// reconnectDaemonMsg 触发一次 daemon 重连尝试。
type reconnectDaemonMsg struct{}

// daemonViewMsg 在从 daemon 收到视图数据时发送。
type daemonViewMsg struct {
	view daemon.ViewMsg
}

// daemonResultMsg 在从 daemon 收到 action 结果时发送。
type daemonResultMsg struct {
	result daemon.ResultMsg
}

// Model 是 octl TUI 的顶层 Bubble Tea 模型。
type Model struct {
	width                 int
	height                int
	contentWidth          int
	contentHeight         int
	activeView            ViewType
	nav                   NavModel
	statusBar             StatusBarModel
	dashboard             views.DashboardModel
	favorites             views.FavoritesModel
	stats                 views.StatsModel
	help                  views.HelpModel
	socketPath            string
	daemonClient          *daemon.SocketClient
	daemonOnline          bool
	daemonVersionMismatch bool // daemonVersionMismatch 在 daemon 返回协议版本不匹配响应时置位，header 显示升级提示。
	reconnectAttempts     int  // 连续重连失败计数，驱动 reconnectDelay 退避曲线；连上即清零。
	daemonMsgCh           chan interface{}
	favoritesMap          map[string]bool // 本端收藏集合，跨视图、跨 ViewMsg 刷新保持
}

// New 使用给定的刷新间隔和 daemon socket 路径创建一个新的 Model。
func New(refreshTime int, socketPath string) Model {
	initTUILog()
	return Model{
		activeView:   DashboardView,
		nav:          NewNavModel(),
		statusBar:    NewStatusBarModel(),
		dashboard:    views.NewDashboardModel(refreshTime),
		favorites:    views.NewFavoritesModel(),
		stats:        views.NewStatsModel(),
		help:         views.NewHelpModel(),
		socketPath:   socketPath,
		daemonMsgCh:  make(chan interface{}, 256),
		favoritesMap: make(map[string]bool),
	}
}

// Init 返回启动 daemon 连接的初始命令。
func (m Model) Init() tea.Cmd {
	log.Printf("[tui] Init called")
	return tea.Batch(
		m.dashboard.Init(),
		m.stats.Init(),
		m.help.Init(),
		m.connectDaemon(),
	)
}

// connectDaemon 尝试连接 daemon 并订阅 view 通道。
func (m Model) connectDaemon() tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewSocketClientWithSocket(m.socketPath)
		if err := client.Connect(); err != nil {
			return daemonOfflineMsg{err: err}
		}
		if err := client.SubscribeView(); err != nil {
			_ = client.Close()
			return daemonOfflineMsg{err: err}
		}
		return daemonOnlineMsg{client: client}
	}
}

// consumeDaemonMsgs 在后台读取 daemon 消息并转发到 daemonMsgCh。
func (m *Model) consumeDaemonMsgs(client *daemon.SocketClient) {
	for msg := range client.Msgs() {
		log.Printf("[tui] consumeDaemonMsgs got %T", msg)
		select {
		case m.daemonMsgCh <- msg:
			log.Printf("[tui] forwarded %T to daemonMsgCh", msg)
		default:
			log.Printf("[tui] dropped %T (daemonMsgCh full)", msg)
		}
	}
	// Channel closed means the connection was lost.
	// 阻塞发送，确保 TUI 一定能收到离线通知；waitForDaemonMsg 在线时
	// 始终有 goroutine 在读取此 channel，所以不会死锁。
	log.Printf("[tui] client.Msgs() closed, sending offline")
	m.daemonMsgCh <- daemonOfflineMsg{}
}

// waitForDaemonMsg 从 daemonMsgCh 读取下一条消息并转换为 tea.Msg。
func (m Model) waitForDaemonMsg() tea.Msg {
	msg, ok := <-m.daemonMsgCh
	if !ok {
		log.Printf("[tui] daemonMsgCh closed, returning offline")
		return daemonOfflineMsg{}
	}
	log.Printf("[tui] waitForDaemonMsg got %T", msg)
	switch v := msg.(type) {
	case daemon.ViewMsg:
		log.Printf("[tui] waitForDaemonMsg -> daemonViewMsg")
		return daemonViewMsg{view: v}
	case daemon.ProgressMsg:
		log.Printf("[tui] waitForDaemonMsg -> ProgressMsg")
		return v
	case daemon.ResultMsg:
		log.Printf("[tui] waitForDaemonMsg -> daemonResultMsg")
		return daemonResultMsg{result: v}
	case daemon.ResponseMsg:
		log.Printf("[tui] waitForDaemonMsg -> ResponseMsg")
		return v
	case daemonOfflineMsg:
		log.Printf("[tui] waitForDaemonMsg -> daemonOfflineMsg")
		return v
	default:
		// Keep listening for the next daemon message even if this one is not
		// directly handled by the top-level model.
		log.Printf("[tui] waitForDaemonMsg -> waitForNextDaemonMsg (unhandled %T)", msg)
		return waitForNextDaemonMsg{}
	}
}

// waitForNextDaemonMsg is an internal signal that causes Update to schedule
// another waitForDaemonMsg call, keeping the read loop alive.
type waitForNextDaemonMsg struct{}

// reconnectDelays 是重连退避曲线：快首试 + 指数退避 + 封顶。daemon 重启
// 窗口通常在秒级，250ms 首试让 TUI 几乎无缝恢复；持续离线时逐级退到 5s
// 封顶，避免高频空连。
var reconnectDelays = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second}

// reconnectDelay 返回第 attempts 次（0 起）连续失败后的重连延迟。
func reconnectDelay(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts >= len(reconnectDelays) {
		return reconnectDelays[len(reconnectDelays)-1]
	}
	return reconnectDelays[attempts]
}

// scheduleReconnect 按退避曲线在 reconnectDelay(attempts) 后触发一次重连。
func (m Model) scheduleReconnect() tea.Cmd {
	delay := reconnectDelay(m.reconnectAttempts)
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return reconnectDaemonMsg{}
	})
}

// sendAction 通过 daemon socket 发送一个管理操作请求。
func (m Model) sendAction(action daemon.ActionMsg) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return daemonResultMsg{result: daemon.ResultMsg{Action: action.Action, Error: "daemon offline"}}
		}
		if err := m.daemonClient.SendAction(action); err != nil {
			return daemonResultMsg{result: daemon.ResultMsg{Action: action.Action, Error: err.Error()}}
		}
		return nil
	}
}

// requestMessages 通过 daemon socket 请求某个 session 的消息历史。
func (m Model) requestMessages(sessionID string) tea.Cmd {
	return func() tea.Msg {
		if m.daemonClient == nil {
			return daemon.ResponseMsg{Ok: false, Error: "daemon offline"}
		}
		id := fmt.Sprintf("msg-%d", time.Now().UnixNano())
		if err := m.daemonClient.RequestMessages(id, sessionID); err != nil {
			return daemon.ResponseMsg{Ok: false, Error: err.Error()}
		}
		return nil
	}
}

// Update 处理所有传入消息并返回更新后的模型。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.contentWidth = msg.Width - 19 // nav width (18) + border (1)
		// header (1) + separator (1) + status bar (1) + nav top padding (1)
		m.contentHeight = msg.Height - 4
		if m.contentWidth < 10 {
			m.contentWidth = 10
		}
		if m.contentHeight < 5 {
			m.contentHeight = 5
		}

		// 将内容区域尺寸传播到导航栏和所有视图。
		m.nav.ContentHeight = m.contentHeight

		resizeMsg := tea.WindowSizeMsg{
			Width:  m.contentWidth,
			Height: m.contentHeight,
		}
		var cmds []tea.Cmd
		var cmd tea.Cmd
		m.dashboard, cmd = m.dashboard.Update(resizeMsg)
		cmds = append(cmds, cmd)
		m.favorites, cmd = m.favorites.Update(resizeMsg)
		cmds = append(cmds, cmd)
		m.stats, cmd = m.stats.Update(resizeMsg)
		cmds = append(cmds, cmd)
		m.help, cmd = m.help.Update(resizeMsg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)

	case daemonOnlineMsg:
		log.Printf("[tui] daemon online, starting consumeDaemonMsgs")
		m.daemonOnline = true
		m.daemonClient = msg.client
		m.reconnectAttempts = 0 // 连上即清零退避计数，下次断连从快首试开始
		go m.consumeDaemonMsgs(msg.client)
		return m, m.waitForDaemonMsg

	case reconnectDaemonMsg:
		return m, m.connectDaemon()

	case daemonOfflineMsg:
		m.daemonOnline = false
		if m.daemonClient != nil {
			_ = m.daemonClient.Close()
			m.daemonClient = nil
		}
		m.reconnectAttempts++
		return m, tea.Batch(m.scheduleReconnect(), m.waitForDaemonMsg)

	case daemonViewMsg:
		// 将从 daemon 收到的完整视图数据转发给 dashboard 和 stats。
		m.daemonVersionMismatch = false // 收到有效 view 说明版本已匹配，清除 mismatch 提示
		log.Printf("[tui] received daemonViewMsg projects=%d favorites=%d stats=%+v", len(msg.view.Projects), len(msg.view.Favorites), msg.view.Stats)
		m.statusBar.SessionCount = msg.view.Stats.TotalSessions
		m.statusBar.ActiveSessions = msg.view.Stats.ActiveSessions
		m.statusBar.TotalCost = msg.view.Stats.TotalCost

		// 以 daemon 为权威数据源重建本端收藏集合。
		// ViewMsg.Favorites 含完整 ViewSession（含 IsFavorite），按 daemon 插入顺序排列。
		m.favoritesMap = make(map[string]bool, len(msg.view.Favorites))
		for _, fav := range msg.view.Favorites {
			m.favoritesMap[fav.SessionID] = true
		}

		var dashCmd, statsCmd, favCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(views.ViewLoadedMsg{View: msg.view})
		// 重建树后同步当前收藏集合，确保 dashboard 渲染一致。
		m.dashboard, _ = m.dashboard.Update(views.FavoritesChangedMsg{Favorites: m.favoritesMap})
		m.favorites, favCmd = m.favorites.Update(views.ViewLoadedMsg{View: msg.view})
		// 同步收藏集合到 favorites 视图。
		m.favorites, _ = m.favorites.Update(views.FavoritesChangedMsg{Favorites: m.favoritesMap})
		m.stats, statsCmd = m.stats.Update(views.ViewLoadedMsg{View: msg.view})
		log.Printf("[tui] forwarding view to dashboard/stats/favorites, scheduling next wait")
		return m, tea.Batch(dashCmd, favCmd, statsCmd, m.waitForDaemonMsg)

	case waitForNextDaemonMsg:
		return m, m.waitForDaemonMsg

	case views.MessageRequestMsg:
		return m, tea.Batch(m.requestMessages(msg.SessionID), m.waitForDaemonMsg)

	case views.ManageActionMsg:
		// 将 dashboard 触发的管理操作请求转发给 daemon。
		action := daemon.ActionMsg{
			Type:       "action",
			Action:     msg.Action,
			SessionIDs: msg.SessionIDs,
			ProjectID:  msg.ProjectID,
			Directory:  msg.Directory,
			Message:    msg.Message,
		}
		if msg.SessionID != "" {
			action.SessionID = msg.SessionID
		}
		return m, tea.Batch(m.sendAction(action), m.waitForDaemonMsg)

	case views.FavoritesToggleRequestMsg:
		// toggle 语义：对已收藏的 session 发 unfavorite，未收藏的发 favorite。
		// 乐观更新本地 favoritesMap，daemon 为最终权威数据源（下一 ViewMsg 确认）。
		var favIDs, unfavIDs []string
		for _, id := range msg.SessionIDs {
			if m.favoritesMap[id] {
				unfavIDs = append(unfavIDs, id)
			} else {
				favIDs = append(favIDs, id)
			}
		}
		// 乐观更新
		for _, id := range favIDs {
			m.favoritesMap[id] = true
		}
		for _, id := range unfavIDs {
			delete(m.favoritesMap, id)
		}
		// 广播更新后的收藏集合给子视图
		var cmds []tea.Cmd
		changed := views.FavoritesChangedMsg{Favorites: m.favoritesMap}
		var dashCmd, favCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(changed)
		m.favorites, favCmd = m.favorites.Update(changed)
		cmds = append(cmds, dashCmd, favCmd)
		// 向 daemon 发送 favorite / unfavorite action
		if len(favIDs) > 0 {
			cmds = append(cmds, m.sendAction(daemon.ActionMsg{
				Type:       "action",
				Action:     "favorite",
				SessionIDs: favIDs,
			}))
		}
		if len(unfavIDs) > 0 {
			cmds = append(cmds, m.sendAction(daemon.ActionMsg{
				Type:       "action",
				Action:     "unfavorite",
				SessionIDs: unfavIDs,
			}))
		}
		cmds = append(cmds, m.waitForDaemonMsg)
		return m, tea.Batch(cmds...)

	case views.FavoritesRemoveRequestMsg:
		// 乐观更新：从本端收藏集合中移除，并向 daemon 发送 unfavorite action。
		for _, id := range msg.SessionIDs {
			delete(m.favoritesMap, id)
		}
		// 广播更新后的收藏集合给子视图。
		var cmds []tea.Cmd
		changed := views.FavoritesChangedMsg{Favorites: m.favoritesMap}
		var dashCmd, favCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(changed)
		m.favorites, favCmd = m.favorites.Update(changed)
		cmds = append(cmds, dashCmd, favCmd)
		// 向 daemon 发送 unfavorite action
		cmds = append(cmds, m.sendAction(daemon.ActionMsg{
			Type:       "action",
			Action:     "unfavorite",
			SessionIDs: msg.SessionIDs,
		}))
		cmds = append(cmds, m.waitForDaemonMsg)
		return m, tea.Batch(cmds...)

	case daemon.ProgressMsg:
		var dashCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(msg)
		return m, tea.Batch(dashCmd, m.waitForDaemonMsg)

	case daemonResultMsg:
		// favorite/unfavorite 是即时内存操作（daemon 仍会回 result 消息），
		// 不应被当作管理操作进度弹窗处理——下一帧 view 消息即确认收藏状态。
		if msg.result.Action == "favorite" || msg.result.Action == "unfavorite" {
			return m, m.waitForDaemonMsg
		}
		var dashCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(views.ManageResultMsg{Result: msg.result})
		return m, tea.Batch(dashCmd, m.waitForDaemonMsg)

	case daemon.ResponseMsg:
		if !msg.Ok && strings.Contains(msg.Error, "version mismatch") {
			m.daemonVersionMismatch = true
		}
		var dashCmd tea.Cmd
		m.dashboard, dashCmd = m.dashboard.Update(msg)
		return m, tea.Batch(dashCmd, m.waitForDaemonMsg)

	case tea.KeyMsg:
		// 视图切换按键：处理并停止——不转发给子视图。
		var switchedView bool
		switch {
		case key.Matches(msg, Keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, Keys.Tab):
			m.activeView = ViewType((int(m.activeView) + 1) % int(viewCount))
			switchedView = true
		case key.Matches(msg, Keys.ShiftTab):
			m.activeView = ViewType((int(m.activeView) - 1 + int(viewCount)) % int(viewCount))
			switchedView = true
		case key.Matches(msg, Keys.One):
			m.activeView = DashboardView
			switchedView = true
		case key.Matches(msg, Keys.Two):
			m.activeView = FavoritesView
			switchedView = true
		case key.Matches(msg, Keys.Three):
			m.activeView = StatsView
			switchedView = true
		case key.Matches(msg, Keys.Four):
			m.activeView = HelpView
			switchedView = true
		}
		m.nav.Selected = int(m.activeView)

		if switchedView {
			return m, nil
		}

		// 将其他按键消息转发给当前活动视图以进行导航等操作。
		var cmd tea.Cmd
		switch m.activeView {
		case DashboardView:
			m.dashboard, cmd = m.dashboard.Update(msg)
		case FavoritesView:
			m.favorites, cmd = m.favorites.Update(msg)
		case StatsView:
			m.stats, cmd = m.stats.Update(msg)
		case HelpView:
			m.help, cmd = m.help.Update(msg)
		}
		return m, cmd
	}

	return m, nil
}

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("252"))
	onlineStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("82"))
	offlineStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("196"))
	offlineContentStyle = lipgloss.NewStyle().
				Faint(true)
)

// renderHeader 渲染顶部标题栏：左侧空、中间标题、右侧 online/offline 状态。
func (m Model) renderHeader() string {
	var status string
	if m.daemonVersionMismatch {
		status = offlineStyle.Render(daemon.VersionMismatchError)
	} else if m.daemonOnline {
		status = onlineStyle.Render("🟢 ONLINE")
	} else {
		status = offlineStyle.Render("🔴 OFFLINE")
	}

	title := "octl — opencode Session Manager"
	if m.width < 40 {
		title = "octl"
	}

	// 用空格手动填充，避免 lipgloss.JoinHorizontal 在某些终端下丢失内容。
	available := m.width - lipgloss.Width(title) - lipgloss.Width(status)
	if available < 1 {
		available = 1
	}
	padding := available / 2
	leftPad := strings.Repeat(" ", padding)
	rightPad := strings.Repeat(" ", available-padding)

	// 把标题和状态放在同一行，并强制整行宽度为 m.width，
	// 保证背景色铺满且不出现首行被吞掉的情况。
	line := leftPad + title + rightPad + status
	extra := m.width - lipgloss.Width(line)
	if extra > 0 {
		line += strings.Repeat(" ", extra)
	}
	return headerStyle.Width(m.width).Render(line)
}

// View 渲染完整的 TUI：头部、导航栏+内容主体和状态栏。
func (m Model) View() string {
	if m.width == 0 {
		return "octl — opencode Session Manager\nInitializing..."
	}

	if m.width < 30 || m.height < 8 {
		return fmt.Sprintf("Terminal too small: %dx%d\nPlease resize to at least 30x8.", m.width, m.height)
	}

	// 头部。
	header := m.renderHeader()

	// 导航侧边栏。
	navView := m.nav.View()

	// 当前活动内容视图。
	var content string
	switch m.activeView {
	case DashboardView:
		content = m.dashboard.View()
	case FavoritesView:
		content = m.favorites.View()
	case StatsView:
		content = m.stats.View()
	case HelpView:
		content = m.help.View()
	}

	// daemon 离线时内容区变暗，提示用户状态不再刷新。
	if !m.daemonOnline {
		content = offlineContentStyle.Render(content)
	}

	// 主体：左侧导航栏，右侧内容。
	body := lipgloss.JoinHorizontal(lipgloss.Top, navView, content)

	// 白色分割线，分隔 header 和主体内容，与左侧导航栏右边框同色。
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Render(strings.Repeat("─", m.width))

	// 底部的状态栏。
	statusBar := m.statusBar.View(m.width)

	return lipgloss.JoinVertical(lipgloss.Top, header, separator, body, statusBar)
}
