package views

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/tomasWade/octl/internal/daemon"
	"github.com/tomasWade/octl/internal/types"
)

// conversationMessage 表示会话查看器中的单条消息轮次。
type conversationMessage struct {
	msgID       string
	role        string
	text        string
	timeCreated int64 // Unix 毫秒时间戳
}

// NodeType 表示树节点的类型。
type NodeType int

const (
	NodeProject NodeType = iota
	NodeSession
)

// TreeNode 表示树形层级结构中的一个节点。
// 顶层节点是项目（Project）；会话（Session）嵌套在其所属项目下。
type TreeNode struct {
	Type      NodeType
	Project   types.Project        // 当 Type == NodeProject 时有效
	Session   types.Session        // 当 Type == NodeSession 时有效
	Status    daemon.SessionStatus // 当 Type == NodeSession 时有效（自身状态）
	RowStatus daemon.SessionStatus // 聚合状态：project/root session 反映子树状态
	Children  []*TreeNode
	Depth     int
	Expanded  bool
}

// ManageActionMsg 在用户确认对所选会话执行操作时发送。
// 由 app 层转发给 daemon 执行。
type ManageActionMsg struct {
	Action     string
	SessionID  string   // 用于 fork/send
	SessionIDs []string // 用于 delete/export
	ProjectID  string   // 用于 delete 的 project 行清理
	Directory  string   // 用于 create/fork/send
	Message    string   // 用于 create/fork/send
}

// ManageResultMsg 在 daemon 返回 action 结果时发送。
type ManageResultMsg struct {
	Result daemon.ResultMsg
}

// FavoritesToggleRequestMsg 在用户按下 f 键收藏会话时发送。
// 由 app 层处理，更新共享收藏集合后广播 FavoritesChangedMsg 回子视图。
type FavoritesToggleRequestMsg struct {
	SessionIDs []string
}

// FavoritesChangedMsg 在共享收藏集合发生变更后由 app 层广播。
// Dashboard 和 Favorites 视图据此更新本地渲染状态。
type FavoritesChangedMsg struct {
	Favorites map[string]bool
}

// MessageRequestMsg 在请求查看某个 session 的消息历史时发送。
type MessageRequestMsg struct {
	SessionID string
}

// dashboardMode 表示仪表盘的当前交互模式。
type dashboardMode int

const (
	modeBrowse   dashboardMode = iota // 树形导航
	modeConfirm                       // 操作前的确认对话框
	modeProgress                      // 操作执行结果
	modeMessage                       // 最后一条消息弹窗
	modeInput                         // 新建会话的文本输入
)

// DashboardModel 在可展开的树形结构中显示会话，按项目组织层级。
// 项目显示在顶层，会话作为项目的子项。
// 通过 d/e 键可以执行管理操作（删除/导出）。
type DashboardModel struct {
	width        int
	height       int
	loaded       bool
	err          error
	roots        []*TreeNode
	visible      []*TreeNode
	cursor       int
	scrollOffset int // 树可视窗口首行索引
	selectedID   string

	// selected 记录通过 Space 键多选选中的 session ID。
	// 采用懒初始化：首次使用时 if nil 则 make。
	selected map[string]bool

	// favorites 记录来自 app 层的收藏 session ID 集合。
	// 由 FavoritesChangedMsg 更新，跨 ViewMsg 刷新保持。
	favorites map[string]bool

	// 当前 daemon 视图数据
	view daemon.ViewMsg

	// 管理操作状态
	mode     dashboardMode
	action   string // "delete" | "export" | "create" | "fork" | "send"
	progress daemon.Summary
	done     bool // 当收到 ManageResultMsg 时为 true

	// 批量操作状态
	pendingSessionIDs []string
	pendingProjectID  string // 在项目节点上操作时非空
	pendingDirectory  string
	pendingMessage    string
	pendingSessionID  string // 用于 fork/send

	// 最后消息弹窗状态
	messageErr error

	msgScroll int

	cachedLines []string

	feedback string // 在树上方显示的简短反馈消息

	convMsgs []conversationMessage
	msgIndex int

	inputBuf string // 新建会话的文本缓冲区
	inputDir string // 新建会话的目录

	// 以下两个字段在 modeInput 中区分操作类型：
	// forkSessionID 非空 → n 键在 session 节点上，按 Enter 调 ForkSession
	// sendToSessionID 非空 → s 键在 session 节点上，按 Enter 调 SendMessage
	// 两者都为空 → n 键在 project 节点上，按 Enter 调 CreateSession
	forkSessionID   string
	sendToSessionID string
}

// ViewLoadedMsg 在从 daemon 收到新的 ViewMsg 时发送。
type ViewLoadedMsg struct {
	View daemon.ViewMsg
}

// NewDashboardModel 创建一个新的 DashboardModel。
// refreshIntervalSec 已废弃；刷新现在由 daemon 推送驱动。
func NewDashboardModel(refreshIntervalSec int) DashboardModel {
	_ = refreshIntervalSec
	return DashboardModel{}
}

// Init 返回空命令；数据现在由 daemon 推送。
func (m DashboardModel) Init() tea.Cmd {
	return nil
}

// Update 处理仪表盘视图的消息。
func (m DashboardModel) Update(msg tea.Msg) (DashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case ViewLoadedMsg:
		busy, perm, other := 0, 0, 0
		for _, p := range msg.View.Projects {
			for _, s := range p.Sessions {
				switch s.RowStatus {
				case daemon.StatusBusy:
					busy++
				case daemon.StatusPermission:
					perm++
				default:
					other++
				}
			}
		}
		log.Printf("[dashboard] ViewLoadedMsg projects=%d sessions busy=%d perm=%d other=%d loaded=%v", len(msg.View.Projects), busy, perm, other, m.loaded)
		m.view = msg.View
		m.loaded = true
		m.err = nil
		m = m.rebuildTree()
		log.Printf("[dashboard] rebuildTree done visible=%d cursor=%d", len(m.visible), m.cursor)
		return m, nil

	case daemon.ProgressMsg:
		if m.mode == modeProgress {
			m.done = false
		}
		return m, nil

	case ManageResultMsg:
		// favorite/unfavorite 是即时内存操作，不应进入进度弹窗。
		// 防御性处理：即使 result 被路由到这里也直接忽略，保持浏览模式。
		if msg.Result.Action == "favorite" || msg.Result.Action == "unfavorite" {
			return m, nil
		}
		m.mode = modeProgress
		m.progress = msg.Result.Summary
		m.done = true
		for _, r := range msg.Result.Summary.Results {
			if r.Success {
				delete(m.selected, r.SessionID)
			}
		}
		m.clampScrollOffset()
		return m, nil

	case daemon.ResponseMsg:
		if msg.Ok && len(msg.Messages) > 0 {
			m.convMsgs = nil
			for _, p := range msg.Messages {
				if p.Text == "" {
					continue
				}
				if len(m.convMsgs) > 0 && m.convMsgs[len(m.convMsgs)-1].msgID == p.MessageID {
					m.convMsgs[len(m.convMsgs)-1].text += "\n" + p.Text
				} else {
					m.convMsgs = append(m.convMsgs, conversationMessage{
						msgID:       p.MessageID,
						role:        p.Role,
						text:        p.Text,
						timeCreated: p.TimeCreated,
					})
				}
			}
			m.msgIndex = len(m.convMsgs) - 1
			m.msgScroll = 0
			m.mode = modeMessage
			m.rebuildMsgCache()
		} else {
			m.messageErr = fmt.Errorf("failed to load messages")
			if !msg.Ok {
				m.messageErr = fmt.Errorf("%s", msg.Error)
			}
		}
		return m, nil

	case FavoritesChangedMsg:
		m.favorites = msg.Favorites
		return m, nil

	case tea.KeyMsg:
		if !m.loaded {
			return m, nil
		}

		switch m.mode {
		case modeBrowse:
			var navCmd tea.Cmd
			m, navCmd = m.handleTreeNav(msg)
			return m, navCmd

		case modeConfirm:
			switch msg.String() {
			case "enter":
				m.mode = modeProgress
				m.done = false
				ids := append([]string(nil), m.pendingSessionIDs...)
				return m, func() tea.Msg {
					return ManageActionMsg{
						Action:     m.action,
						SessionIDs: ids,
						ProjectID:  m.pendingProjectID,
					}
				}
			case "esc":
				m.mode = modeBrowse
				m.action = ""
				m.pendingProjectID = ""
				m.pendingSessionIDs = nil
				return m, nil
			}

		case modeProgress:
			m.mode = modeBrowse
			m.action = ""
			m.progress = daemon.Summary{}
			m.pendingProjectID = ""
			m.pendingSessionIDs = nil
			m.pendingDirectory = ""
			m.pendingMessage = ""
			m.pendingSessionID = ""
			m.done = false
			return m, nil

		case modeMessage:
			switch msg.String() {
			case "j", "down":
				// 从负值边界 snap 到 0，再 +1，确保按一次 j 立即可见下移 1 行。
				// 否则用户从顶部疯狂按 k 后 msgScroll=-N，按 j 要 N+1 次才见效。
				if m.msgScroll < 0 {
					m.msgScroll = 0
				}
				m.msgScroll++
			case "k", "up":
				// 从超出底部边界 snap 回 maxScroll，再 -1，确保按一次 k 立即可见上移 1 行。
				totalLines := len(m.cachedLines)
				maxLines := m.height*85/100 - 4
				if maxLines < 1 {
					maxLines = 1
				}
				if totalLines > maxLines {
					maxScroll := totalLines - maxLines
					if m.msgScroll > maxScroll {
						m.msgScroll = maxScroll
					}
				}
				m.msgScroll--
				// clamp 到 -1 而非 0，让滚动条能显示 "At top" 状态
				if m.msgScroll < -1 {
					m.msgScroll = -1
				}
			case "h", "left":
				if m.msgIndex > 0 {
					m.msgIndex--
					m.msgScroll = 0
					m.rebuildMsgCache()
				}
			case "l", "right":
				if m.msgIndex < len(m.convMsgs)-1 {
					m.msgIndex++
					m.msgScroll = 0
					m.rebuildMsgCache()
				}
			case "esc", "q", "m", "M":
				m.mode = modeBrowse
				return m, nil
			}
			return m, nil

		case modeInput:
			switch msg.String() {
			case "enter":
				if m.inputBuf != "" {
					m.mode = modeProgress
					m.done = false
					action := "create"
					sessionID := ""
					switch {
					case m.forkSessionID != "":
						action = "fork"
						sessionID = m.forkSessionID
					case m.sendToSessionID != "":
						action = "send"
						sessionID = m.sendToSessionID
					}
					return m, func() tea.Msg {
						return ManageActionMsg{
							Action:    action,
							SessionID: sessionID,
							Directory: m.inputDir,
							Message:   m.inputBuf,
						}
					}
				}
				m.mode = modeBrowse
				m.forkSessionID = ""
				m.sendToSessionID = ""
				return m, nil
			case "esc":
				m.mode = modeBrowse
				m.forkSessionID = ""
				m.sendToSessionID = ""
				return m, nil
			case "backspace":
				// 用 utf8.DecodeLastRuneInString 按完整字符删除，而非按字节截断。
				// 中文 UTF-8 占 3 字节，按字节删会破坏编码。
				if len(m.inputBuf) > 0 {
					_, size := utf8.DecodeLastRuneInString(m.inputBuf)
					m.inputBuf = m.inputBuf[:len(m.inputBuf)-size]
				}
			default:
				// 追加可打印字符
				m.inputBuf += msg.String()
			}
			return m, nil
		}
	}

	return m, nil
}

// handleTreeNav 处理 modeBrowse 模式下的树形导航和管理按键。
// 返回更新后的模型和命令。如果按键未被处理，则返回原模型不变。
func (m DashboardModel) handleTreeNav(msg tea.KeyMsg) (DashboardModel, tea.Cmd) {
	if len(m.visible) == 0 {
		return m, nil
	}
	switch msg.String() {
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			node := m.visible[m.cursor]
			if node.Type == NodeSession {
				m.selectedID = node.Session.ID
			} else {
				m.selectedID = node.Project.ID
			}
		}

	case "j", "down":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			node := m.visible[m.cursor]
			if node.Type == NodeSession {
				m.selectedID = node.Session.ID
			} else {
				m.selectedID = node.Project.ID
			}
		}

	case "l", "right", "enter":
		node := m.visible[m.cursor]
		if len(node.Children) > 0 {
			node.Expanded = true
			m.visible = flattenTree(m.roots)
			for i, n := range m.visible {
				if n == node {
					m.cursor = i
					break
				}
			}
			if m.visible[m.cursor].Type == NodeSession {
				m.selectedID = m.visible[m.cursor].Session.ID
			} else {
				m.selectedID = m.visible[m.cursor].Project.ID
			}
		}

	case "h", "left":
		node := m.visible[m.cursor]
		if node.Expanded {
			node.Expanded = false
			m.visible = flattenTree(m.roots)
			for i, n := range m.visible {
				if n == node {
					m.cursor = i
					break
				}
			}
			if m.visible[m.cursor].Type == NodeSession {
				m.selectedID = m.visible[m.cursor].Session.ID
			} else {
				m.selectedID = m.visible[m.cursor].Project.ID
			}
		} else if node.Depth > 0 {
			for i := m.cursor - 1; i >= 0; i-- {
				if m.visible[i].Depth < node.Depth {
					m.cursor = i
					break
				}
			}
			parent := m.visible[m.cursor]
			if parent.Type == NodeSession {
				m.selectedID = parent.Session.ID
			} else {
				m.selectedID = parent.Project.ID
			}
		}

	case " ":
		// Space 键改为多选：对当前 session/project 行收集其全部后代 session，
		// 执行全选/反选（XOR 语义）。空节点无操作。
		node := m.visible[m.cursor]
		m = m.toggleSelectAll(collectSessionIDs(node))

	case "d", "D":
		// 若已有 Space 多选，则按 visible 顺序收集全部选中 session 进行跨 project 批量删除；
		// 否则保持现有子树删除行为。
		if len(m.selected) > 0 {
			var ids []string
			seen := make(map[string]bool)
			// 第一遍：按树顺序收集当前可见的选中 session，保证顺序确定。
			for _, n := range m.visible {
				if n.Type != NodeSession {
					continue
				}
				if !m.selected[n.Session.ID] || seen[n.Session.ID] {
					continue
				}
				ids = append(ids, n.Session.ID)
				seen[n.Session.ID] = true
			}
			// 第二遍（无条件）：补全因项目折叠而不可见的选中 session，确保不遗漏任何已选项。
			for id := range m.selected {
				if seen[id] {
					continue
				}
				ids = append(ids, id)
				seen[id] = true
			}
			m.mode = modeConfirm
			m.action = "delete"
			m.pendingSessionIDs = ids
			m.pendingProjectID = ""
			break
		}
		node := m.visible[m.cursor]
		if node.Type == NodeProject && node.Project.ID == "global" && len(node.Children) == 0 {
			m.feedback = "Global project has no sessions to remove."
			return m, nil
		}
		m.mode = modeConfirm
		m.action = "delete"
		m.pendingSessionIDs = collectSessionIDs(node)
		if node.Type == NodeProject {
			m.pendingProjectID = node.Project.ID
		} else {
			m.pendingProjectID = ""
		}

	case "e", "E":
		node := m.visible[m.cursor]
		m.mode = modeConfirm
		m.action = "export"
		m.pendingSessionIDs = collectSessionIDs(node)
		m.pendingProjectID = ""

	case "m", "M":
		node := m.visible[m.cursor]
		if node.Type != NodeSession {
			return m, nil
		}
		return m, func() tea.Msg {
			return MessageRequestMsg{SessionID: node.Session.ID}
		}

	case "r":
		if m.loaded {
			m.feedback = "Auto-updated by daemon"
		}

	case "n", "N":
		node := m.visible[m.cursor]
		if node.Type == NodeProject {
			m.mode = modeInput
			m.inputBuf = ""
			dir := node.Project.Worktree
			if dir == "/" {
				dir, _ = os.UserHomeDir()
			}
			m.inputDir = dir
			m.feedback = ""
		} else if node.Type == NodeSession {
			m.mode = modeInput
			m.inputBuf = ""
			m.inputDir = node.Session.Directory
			m.forkSessionID = node.Session.ID
			m.feedback = ""
		}
	case "s", "S":
		// 向已有 session 发送消息。和 n 键共用 modeInput，
		// 按 Enter 时由 sendToSessionID 区分，调 SendMessage。
		node := m.visible[m.cursor]
		if node.Type == NodeSession {
			m.mode = modeInput
			m.inputBuf = ""
			m.inputDir = node.Session.Directory
			m.sendToSessionID = node.Session.ID
			m.feedback = ""
		}

	case "f", "F":
		// 收藏切换（toggle）：Space 多选则切换全部选中 session，否则切换光标处的单个 session。
		// toggle 语义由 app.go 层处理（对已收藏发 unfavorite，未收藏发 favorite），
		// 并向 daemon 发送对应的 action。
		var ids []string
		if len(m.selected) > 0 {
			for id := range m.selected {
				ids = append(ids, id)
			}
		} else {
			node := m.visible[m.cursor]
			if node.Type == NodeSession {
				ids = append(ids, node.Session.ID)
			}
		}
		if len(ids) > 0 {
			return m, func() tea.Msg {
				return FavoritesToggleRequestMsg{SessionIDs: ids}
			}
		}
	}
	m.ensureCursorVisible()
	return m, nil
}

// View 根据当前模式渲染仪表盘。
func (m DashboardModel) View() string {
	if m.width == 0 {
		return "Manage"
	}
	if !m.loaded {
		return "Loading sessions..."
	}
	if m.err != nil {
		return fmt.Sprintf("Error loading sessions: %v", m.err)
	}

	switch m.mode {
	case modeConfirm:
		return m.confirmView()
	case modeProgress:
		return m.progressView()
	case modeMessage:
		return m.messageView()
	case modeInput:
		return m.inputView()
	default:
		if len(m.visible) == 0 {
			return "No sessions found."
		}
		return m.treeView()
	}
}

// treeView 渲染带有列标题的项目/会话树。
func (m DashboardModel) treeView() string {
	w := m.width
	var b strings.Builder

	// 反馈行
	if m.feedback != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("  " + m.feedback))
		b.WriteString("\n")
	}

	// 列宽
	colAgent := 8
	colCost := 8
	colUpdated := 10
	colStatus := 8
	gap := 1
	fixedW := colStatus + gap + colAgent + gap + colCost + gap + colUpdated
	colTitle := w - fixedW - gap
	if colTitle < 5 {
		colTitle = 5
	}

	header := leftAlign("Status", colStatus) + " " +
		leftAlign("Title", colTitle) + " " +
		leftAlign("Agent", colAgent) + " " +
		leftAlign("Cost", colCost) + " " +
		leftAlign("Updated", colUpdated)
	b.WriteString(headerStyle.Width(w).Render(header))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", w)))
	b.WriteString("\n")

	// 可视窗口
	treeHeight := m.treeHeight()
	total := len(m.visible)
	start := m.scrollOffset
	if start < 0 {
		start = 0
	}
	if total > treeHeight {
		maxStart := total - treeHeight
		if start > maxStart {
			start = maxStart
		}
	}
	end := start + treeHeight
	if end > total {
		end = total
	}

	showScrollbar := total > treeHeight
	var scrollGlyphs []string
	if showScrollbar {
		scrollGlyphs = buildTreeScrollBar(treeHeight, total, start)
	}

	// 行
	for i := start; i < end; i++ {
		line := m.renderRow(m.visible[i], i == m.cursor, colTitle, colAgent, colCost, colUpdated, colStatus)
		if showScrollbar && i-start < len(scrollGlyphs) {
			line += scrollGlyphs[i-start]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	// 底部提示
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render("j/k: nav  h/l: fold  Space: select  d: del  e: exp  m: msg  s: send  f: fav  r: refresh")
	b.WriteString(footer)

	// 填充剩余行
	for lines := (end - start) + 3; lines < m.height; lines++ {
		b.WriteString("\n")
	}

	return b.String()
}

// treeHeight 返回树区域可容纳的行数。
func (m DashboardModel) treeHeight() int {
	h := m.height - 4
	if h < 1 {
		h = 1
	}
	return h
}

// ensureCursorVisible 在光标移出可视窗口时调整 scrollOffset，
// 保证光标始终位于视口内。
func (m *DashboardModel) ensureCursorVisible() {
	treeHeight := m.treeHeight()
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+treeHeight {
		m.scrollOffset = m.cursor - treeHeight + 1
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// clampScrollOffset 保证 scrollOffset 不超过当前可见行数允许的最大值。
func (m *DashboardModel) clampScrollOffset() {
	treeHeight := m.treeHeight()
	maxOffset := len(m.visible) - treeHeight
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.scrollOffset > maxOffset {
		m.scrollOffset = maxOffset
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// buildTreeScrollBar 构建树视图的垂直滚动条 glyph 列。
// 用 █ 表示当前可视窗口（thumb）、░ 表示轨道。
func buildTreeScrollBar(treeHeight, totalRows, start int) []string {
	glyphs := make([]string, treeHeight)
	if treeHeight <= 0 {
		return glyphs
	}
	for i := range glyphs {
		glyphs[i] = "░"
	}
	if totalRows <= treeHeight {
		return glyphs
	}
	thumbStart := start * treeHeight / totalRows
	thumbEnd := (start + treeHeight) * treeHeight / totalRows
	if thumbEnd <= thumbStart {
		thumbEnd = thumbStart + 1
	}
	if thumbEnd > treeHeight {
		thumbEnd = treeHeight
	}
	for i := thumbStart; i < thumbEnd && i < treeHeight; i++ {
		glyphs[i] = "█"
	}
	return glyphs
}

// renderRow 根据节点类型分派到对应的渲染器。
func (m DashboardModel) renderRow(node *TreeNode, isCursor bool, colTitle, colAgent, colCost, colUpdated, colStatus int) string {
	multiSelected := false
	if node.Type == NodeSession {
		multiSelected = m.selected[node.Session.ID]
	} else if node.Type == NodeProject {
		multiSelected = hasSelectedDescendant(node, m.selected)
	}
	if node.Type == NodeProject {
		return m.renderProjectRow(node, isCursor, multiSelected, colTitle, colAgent, colCost, colUpdated, colStatus)
	}
	return m.renderSessionRow(node, isCursor, multiSelected, colTitle, colAgent, colCost, colUpdated, colStatus)
}

// hasSelectedDescendant 递归判断节点及其后代中是否存在被选中的 session。
func hasSelectedDescendant(node *TreeNode, selected map[string]bool) bool {
	if node.Type == NodeSession && selected[node.Session.ID] {
		return true
	}
	for _, c := range node.Children {
		if hasSelectedDescendant(c, selected) {
			return true
		}
	}
	return false
}

// renderProjectRow 渲染项目节点行。
func (m DashboardModel) renderProjectRow(node *TreeNode, isCursor, multiSelected bool, colTitle, colAgent, colCost, colUpdated, colStatus int) string {
	indent := ""
	if node.Depth > 0 {
		indent = strings.Repeat("  ", node.Depth)
	}
	icon := "📁"
	if node.Expanded {
		icon = "📂"
	}
	sessionCount := countSessions(node)
	title := node.Project.Worktree
	if title == "/" {
		title = "global"
	}
	if sessionCount > 0 {
		title = fmt.Sprintf("%s (%d)", title, sessionCount)
	}
	prefix := indent + icon + " "
	if multiSelected {
		prefix = "✓ " + prefix
	}
	if isCursor {
		prefix = cursorMarker + prefix
	}
	titleStr := leftAlign(truncateString(prefix+title, colTitle), colTitle)
	agentStr := leftAlign(node.Project.Vcs, colAgent)
	costStr := leftAlign("-", colCost)
	updatedStr := leftAlign("-", colUpdated)
	statusStr := leftAlign(node.RowStatus.Glyph(), colStatus)

	line := statusStr + " " + titleStr + " " + agentStr + " " + costStr + " " + updatedStr
	line = lipgloss.NewStyle().Foreground(statusColor(node.RowStatus)).Render(line)

	if multiSelected {
		padding := m.width - runewidth.StringWidth(line)
		if padding < 0 {
			padding = 0
		}
		return multiSelectedStyle.Width(m.width).Render(line + strings.Repeat(" ", padding))
	}
	if isCursor {
		padding := m.width - runewidth.StringWidth(line)
		if padding < 0 {
			padding = 0
		}
		return selectedStyle.Width(m.width).Render(line + strings.Repeat(" ", padding))
	}
	return line
}

// countSessions 递归统计 TreeNode 下的会话节点数量。
func countSessions(node *TreeNode) int {
	count := 0
	for _, child := range node.Children {
		if child.Type == NodeSession {
			count++
		}
		count += countSessions(child)
	}
	return count
}

// renderSessionRow 渲染会话节点行。
func (m DashboardModel) renderSessionRow(node *TreeNode, isCursor, multiSelected bool, colTitle, colAgent, colCost, colUpdated, colStatus int) string {
	// 树形前缀：缩进 + 展开图标
	indent := strings.Repeat("    ", node.Depth)
	icon := " "
	if len(node.Children) > 0 {
		if node.Expanded {
			icon = "-"
		} else {
			icon = "+"
		}
	}
	treePrefix := indent + icon + " "

	title := treePrefix + node.Session.Title
	// 子节点计数：有子 session 时在标题后显示直接子节点数目 (N)。
	if len(node.Children) > 0 {
		title += fmt.Sprintf(" (%d)", len(node.Children))
	}
	if multiSelected {
		title = "✓ " + title
	}
	if isCursor {
		title = cursorMarker + title
	}
	title = leftAlign(truncateString(title, colTitle), colTitle)
	agent := leftAlign(truncateString(node.Session.Agent, colAgent), colAgent)
	cost := leftAlign(formatCost(node.Session.Cost), colCost)
	updated := leftAlign(formatRelativeTime(node.Session.TimeUpdated), colUpdated)

	// 状态列：纯状态图标（无文字），已收藏的 session 在图标后叠加 ⭐，
	// 两者之间用一个空格分隔。图标含义见 Help 视图（按 4）。
	st := node.RowStatus
	statusGlyph := st.Glyph()
	if m.favorites[node.Session.ID] {
		statusGlyph += " ⭐"
	}
	status := leftAlign(statusGlyph, colStatus)

	line := status + " " +
		title + " " +
		agent + " " +
		cost + " " +
		updated
	line = lipgloss.NewStyle().Foreground(statusColor(st)).Render(line)

	if multiSelected {
		return multiSelectedStyle.Render(line)
	}
	if isCursor {
		highlight := lipgloss.NewStyle().
			Background(lipgloss.Color("57"))
		return highlight.Render(line)
	}
	return line
}

// confirmView 为选中的会话渲染一个居中的确认对话框。
// 项目删除操作始终显示对话框（即使包含 0 个会话）；
// 仅在纯会话操作且没有任何会话时才跳过。
func (m DashboardModel) confirmView() string {
	if len(m.pendingSessionIDs) == 0 && m.pendingProjectID == "" {
		return ""
	}
	actionVerb := m.action

	var sb strings.Builder
	if m.pendingProjectID != "" {
		if m.pendingProjectID == "global" {
			sb.WriteString(fmt.Sprintf("Remove %d session(s) from the global project?\n\nThe global project itself cannot be deleted.\n\n", len(m.pendingSessionIDs)))
		} else {
			sb.WriteString(fmt.Sprintf("Are you sure you want to %s this project and its %d sessions?\n\n", actionVerb, len(m.pendingSessionIDs)))
		}
	} else if len(m.pendingSessionIDs) == 1 {
		sb.WriteString(fmt.Sprintf("Are you sure you want to %s this session?\n\n", actionVerb))
		title := m.sessionTitle(m.pendingSessionIDs[0])
		if title == "" {
			title = "(no title)"
		}
		title = truncateRunes(title, 47)
		sb.WriteString(fmt.Sprintf("  %s\n", title))
		sb.WriteString(fmt.Sprintf("  ID: %s\n\n", abbreviateID(m.pendingSessionIDs[0])))
	} else {
		sb.WriteString(fmt.Sprintf("Are you sure you want to %s %d sessions?\n\n", actionVerb, len(m.pendingSessionIDs)))
		for _, id := range m.pendingSessionIDs {
			label := m.sessionTitle(id)
			if label == "" {
				label = "(no title)"
			}
			label = truncateRunes(label, 47)
			sb.WriteString(fmt.Sprintf("  %s  (%s)\n", label, abbreviateID(id)))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("[Enter] Confirm   [Esc] Cancel")

	dialogWidth := m.width * 60 / 100
	if dialogWidth < 50 {
		dialogWidth = 50
	}
	if dialogWidth > m.width-4 {
		dialogWidth = m.width - 4
	}

	return m.renderDialog(sb.String(), dialogWidth)
}

// progressView 渲染管理操作的结果。
func (m DashboardModel) progressView() string {
	// 仍在等待异步 goroutine 的结果。
	if !m.done {
		return lipgloss.NewStyle().
			Width(m.width).Height(m.height).
			Align(lipgloss.Center).AlignVertical(lipgloss.Center).
			Render(fmt.Sprintf("Executing %s...", m.action))
	}

	var sb strings.Builder
	sb.WriteString(
		fmt.Sprintf("Operation complete: %d succeeded, %d failed\n\n",
			m.progress.Succeeded, m.progress.Failed),
	)

	for _, r := range m.progress.Results {
		icon := "✓"
		if !r.Success {
			icon = "✗"
		}
		sb.WriteString(fmt.Sprintf(" %s  %s\n", icon, r.SessionID))
		if !r.Success && r.Error != "" {
			sb.WriteString(fmt.Sprintf("    └ %s\n", r.Error))
		}
	}

	sb.WriteString("\nPress Esc to continue")

	dialogWidth := m.width * 60 / 100
	if dialogWidth < 50 {
		dialogWidth = 50
	}
	if dialogWidth > m.width-4 {
		dialogWidth = m.width - 4
	}

	return m.renderDialog(sb.String(), dialogWidth)
}

// buildMessageContent 构建会话弹窗的头部和消息文本。
func (m DashboardModel) buildMessageContent(session types.Session, contentWidth int) string {
	var sb strings.Builder
	title := session.Title
	if title == "" {
		title = "(no title)"
	}
	sb.WriteString(fmt.Sprintf("Conversation — %s\n", title))
	dir := session.Directory
	if dir == "" {
		dir = "(unknown)"
	}
	sb.WriteString(fmt.Sprintf("Directory: %s\n", dir))
	if len(m.convMsgs) > 0 && m.msgIndex >= 0 && m.msgIndex < len(m.convMsgs) {
		roleLabel := m.convMsgs[m.msgIndex].role
		if roleLabel == "" {
			roleLabel = "?"
		}
		roleDisplay := "user"
		if roleLabel != "user" {
			roleDisplay = "assistant"
		}
		ts := time.UnixMilli(m.convMsgs[m.msgIndex].timeCreated).Format("2006-01-02 15:04:05")
		sb.WriteString(fmt.Sprintf("─── Message %d/%d [%s] — %s ───\n",
			m.msgIndex+1, len(m.convMsgs), roleDisplay, ts))
		msgText := m.convMsgs[m.msgIndex].text
		if roleLabel != "user" && msgText != "" {
			rendered, err := glamour.Render(msgText, "dark")
			if err == nil {
				sb.WriteString(strings.TrimRight(rendered, "\n"))
			} else {
				sb.WriteString(msgText)
			}
		} else {
			sb.WriteString(msgText)
		}
	} else {
		sb.WriteString("(no messages)")
	}
	return sb.String()
}

// renderDialog 将内容文本包裹在屏幕居中显示的样式化对话框中。
func (m DashboardModel) renderDialog(content string, dialogWidth int) string {
	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(1, 2).
		Width(dialogWidth)
	dialogContent := dialogStyle.Render(content)
	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center).
		AlignVertical(lipgloss.Center).
		Render(dialogContent)
}

// messageView 渲染会话浏览器弹窗。
// h/l 在消息之间导航，j/k 在当前消息内滚动，
// Esc/m/q 关闭。
func (m DashboardModel) messageView() string {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return ""
	}
	node := m.visible[m.cursor]
	if node.Type != NodeSession {
		return ""
	}
	session := node.Session

	// 弹窗尺寸
	dialogWidth := m.width * 90 / 100
	if dialogWidth < 50 {
		dialogWidth = 50
	}
	if dialogWidth > m.width-4 {
		dialogWidth = m.width - 4
	}
	dialogHeight := m.height * 85 / 100
	if dialogHeight < 10 {
		dialogHeight = 10
	}
	if dialogHeight > m.height-4 {
		dialogHeight = m.height - 4
	}

	// 弹窗内部内容宽度（边框=2 + 内边距=4 = 6 开销）
	contentWidth := dialogWidth - 6
	if contentWidth < 10 {
		contentWidth = 10
	}

	// 如果缓存可用则使用缓存行（在 m/h/l 时重建，j/k 不会使缓存失效）
	var allLines []string
	if len(m.cachedLines) > 0 {
		allLines = m.cachedLines
	} else {
		// 回退：通过 buildMessageContent 构建内容
		content := m.buildMessageContent(session, contentWidth)
		wrappedFallback := lipgloss.NewStyle().Width(contentWidth).Render(content)
		allLines = strings.Split(wrappedFallback, "\n")
	}

	// 弹窗内可用的内容行数（边框=2 + 内边距=2 = 4 开销）
	maxLines := dialogHeight - 4
	if maxLines < 1 {
		maxLines = 1
	}

	// 从 msgScroll 计算可见范围 — 不要修改 msgScroll 本身，
	// 这样才能保证用户的按键始终生效，位置指示器反映实际情况。
	// start 仅在两侧做边界限制，确保安全的切片访问。
	totalLines := len(allLines)
	start := m.msgScroll
	if start < 0 {
		start = 0
	}
	if totalLines > maxLines && start > totalLines-maxLines {
		start = totalLines - maxLines
	}
	end := start + maxLines
	if end > totalLines {
		end = totalLines
	}
	// 如果行数少于视口，则从 0 开始显示所有内容。
	if totalLines <= maxLines {
		start = 0
		end = totalLines
	}

	// 构建带有滚动指示器的可见内容
	var display []string

	if start > 0 {
		display = append(display, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("↑ ..."))
	} else {
		display = append(display, "")
	}

	for _, line := range allLines[start:end] {
		display = append(display, line)
	}

	if end < totalLines {
		display = append(display, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("↓ ..."))
	} else {
		display = append(display, "")
	}

	// 滚动位置条 — 仅在内容超出可见区域时显示
	maxScroll := totalLines - maxLines
	if maxScroll > 0 {
		var posLine string
		if m.msgScroll < 0 {
			posLine = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("── At top ──")
		} else if m.msgScroll >= maxScroll {
			posLine = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("── At bottom ──")
		} else {
			scrollPct := float64(m.msgScroll) / float64(maxScroll)
			barWidth := 16
			filled := int(scrollPct * float64(barWidth))
			bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
			pct := int(scrollPct * 100)
			posLine = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(
				fmt.Sprintf("── %s %d%% ──", bar, pct))
		}
		display = append(display, posLine)
	}

	result := strings.Join(display, "\n")
	result = strings.TrimRight(result, "\n")

	return m.renderDialog(result, dialogWidth)
}

// inputView 渲染用于新建会话的文本输入对话框。
func (m DashboardModel) inputView() string {
	dialogWidth := m.width * 70 / 100
	if dialogWidth < 40 {
		dialogWidth = 40
	}
	if dialogWidth > m.width-4 {
		dialogWidth = m.width - 4
	}

	var sb strings.Builder
	if m.forkSessionID != "" {
		sb.WriteString("Fork Session\n")
		sb.WriteString(fmt.Sprintf("Source: %s\n\n", abbreviateID(m.forkSessionID)))
		sb.WriteString("Enter message for forked session:\n\n")
	} else if m.sendToSessionID != "" {
		sb.WriteString("Send Message\n")
		sb.WriteString(fmt.Sprintf("To: %s\n\n", abbreviateID(m.sendToSessionID)))
		sb.WriteString("Enter message:\n\n")
	} else {
		sb.WriteString("New Session\n")
		sb.WriteString(fmt.Sprintf("Directory: %s\n\n", m.inputDir))
		sb.WriteString("Enter first message:\n\n")
	}

	// 带有光标的输入字段
	input := m.inputBuf + "█"
	sb.WriteString(input)
	sb.WriteString("\n\n[Enter] send  [Esc] cancel")

	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(1, 2).
		Width(dialogWidth)

	dialogContent := dialogStyle.Render(sb.String())

	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center).
		AlignVertical(lipgloss.Center).
		Render(dialogContent)
}

// ---------------------------------------------------------------------------
// 树构建
// ---------------------------------------------------------------------------

// flattenTree 将树展开为可见节点的列表。
// 仅当父节点处于展开状态（Expanded）时才包含子节点。
func flattenTree(nodes []*TreeNode) []*TreeNode {
	var result []*TreeNode
	for _, node := range nodes {
		result = append(result, node)
		if node.Expanded {
			result = append(result, flattenTree(node.Children)...)
		}
	}
	return result
}

// collectSessionIDs 收集节点及其所有后代会话的 ID。
// 对 project/session 节点均生效：先包含节点自身（如果是 session），再递归收集所有子 session。
func collectSessionIDs(node *TreeNode) []string {
	var ids []string
	var collect func(n *TreeNode)
	collect = func(n *TreeNode) {
		if n.Type == NodeSession {
			ids = append(ids, n.Session.ID)
		}
		for _, c := range n.Children {
			collect(c)
		}
	}
	collect(node)
	return ids
}

// toggleSelectAll 对一组 session ID 执行 XOR 全选/反选。
// 若全部已选中则全部取消；否则全部选中。空 ID 列表无操作。
func (m DashboardModel) toggleSelectAll(ids []string) DashboardModel {
	if len(ids) == 0 {
		return m
	}
	if m.selected == nil {
		m.selected = make(map[string]bool)
	}
	allSelected := true
	for _, id := range ids {
		if !m.selected[id] {
			allSelected = false
			break
		}
	}
	if allSelected {
		for _, id := range ids {
			delete(m.selected, id)
		}
	} else {
		for _, id := range ids {
			m.selected[id] = true
		}
	}
	return m
}

// sessionTitle looks up the title for a session ID in the current view.
func (m DashboardModel) sessionTitle(id string) string {
	var search func(nodes []*TreeNode) string
	search = func(nodes []*TreeNode) string {
		for _, n := range nodes {
			if n.Type == NodeSession && n.Session.ID == id {
				return n.Session.Title
			}
			if title := search(n.Children); title != "" {
				return title
			}
		}
		return ""
	}
	return search(m.roots)
}

// viewSessionToSession converts a daemon ViewSession into a types.Session for
// management operations. ProjectID is filled from the parent project context.
func viewSessionToSession(vs daemon.ViewSession, projectID string) types.Session {
	return types.Session{
		ID:           vs.SessionID,
		ProjectID:    projectID,
		ParentID:     vs.ParentID,
		Directory:    vs.Directory,
		Title:        vs.Title,
		Agent:        vs.Agent,
		Cost:         vs.Cost,
		TimeUpdated:  vs.TimeUpdated,
		TimeArchived: vs.TimeArchived,
	}
}

// buildTreeFromView builds a TreeNode tree from a daemon ViewMsg.
func buildTreeFromView(view daemon.ViewMsg) []*TreeNode {
	var roots []*TreeNode
	for _, p := range view.Projects {
		projNode := &TreeNode{
			Type:      NodeProject,
			RowStatus: p.RowStatus,
			Project: types.Project{
				ID:          p.ProjectID,
				Worktree:    p.Worktree,
				Name:        p.Name,
				TimeUpdated: p.TimeUpdated,
			},
			Depth: 0,
		}
		nodeMap := make(map[string]*TreeNode)
		for _, s := range p.Sessions {
			node := &TreeNode{
				Type:      NodeSession,
				Session:   viewSessionToSession(s, p.ProjectID),
				Status:    s.Status,
				RowStatus: s.RowStatus,
				Depth:     s.Depth,
			}
			nodeMap[s.SessionID] = node
			if s.ParentID == "" {
				projNode.Children = append(projNode.Children, node)
			} else if parent, ok := nodeMap[s.ParentID]; ok {
				parent.Children = append(parent.Children, node)
			}
		}
		roots = append(roots, projNode)
	}
	return roots
}

// rebuildTree reconstructs the tree from the current view while preserving
// expansion state and cursor position.
func (m DashboardModel) rebuildTree() DashboardModel {
	// Save expansion state.
	expandedIDs := make(map[string]bool)
	var collectExpanded func(nodes []*TreeNode)
	collectExpanded = func(nodes []*TreeNode) {
		for _, node := range nodes {
			if node.Expanded {
				id := ""
				if node.Type == NodeProject {
					id = node.Project.ID
				} else {
					id = node.Session.ID
				}
				expandedIDs[id] = true
			}
			if len(node.Children) > 0 {
				collectExpanded(node.Children)
			}
		}
	}
	collectExpanded(m.roots)

	m.roots = buildTreeFromView(m.view)

	var restoreExpanded func(nodes []*TreeNode)
	restoreExpanded = func(nodes []*TreeNode) {
		for _, node := range nodes {
			id := ""
			if node.Type == NodeProject {
				id = node.Project.ID
			} else {
				id = node.Session.ID
			}
			if expandedIDs[id] {
				node.Expanded = true
			}
			if len(node.Children) > 0 {
				restoreExpanded(node.Children)
			}
		}
	}
	restoreExpanded(m.roots)

	m.visible = flattenTree(m.roots)
	m.cursor = 0
	if m.selectedID != "" {
		for i, node := range m.visible {
			if (node.Type == NodeSession && node.Session.ID == m.selectedID) ||
				(node.Type == NodeProject && node.Project.ID == m.selectedID) {
				m.cursor = i
				break
			}
		}
	}
	m.ensureCursorVisible()
	m.clampScrollOffset()
	return m
}

// ---------------------------------------------------------------------------
// 样式
// ---------------------------------------------------------------------------

var headerStyle = lipgloss.NewStyle().
	Bold(true).
	PaddingTop(0).
	PaddingBottom(0)

// baseStyle 使用普通边框包裹内容。与管理视图和统计视图共享。
var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))

// selectedStyle 用于高亮当前光标所在的树行（只改背景，保留行内状态颜色）。
var selectedStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("236"))

// multiSelectedStyle 用于高亮通过 Space 键多选选中的 session 行。
var multiSelectedStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("62")).
	Bold(true)

// cursorMarker 是光标当前所在行的前缀标记，即使该行已被多选选中也要显示，
// 以便用户始终能识别光标位置。
const cursorMarker = "▶ "

// ---------------------------------------------------------------------------
// 格式化辅助函数
// ---------------------------------------------------------------------------

// formatRelativeTime 将 Unix 毫秒时间戳转换为人类可读的相对时间字符串。
func formatRelativeTime(ms int64) string {
	if ms == 0 {
		return "-"
	}
	t := time.UnixMilli(ms)
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006/01/02")
	}
}

// formatTokens 将 token 数量格式化为人类可读的字符串（例如 "1.2K"、"3.5M"）。
func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatCost 将费用值格式化为美元字符串（例如 "$0.04"）。
func formatCost(cost float64) string {
	return fmt.Sprintf("$%.2f", cost)
}

// abbreviateID 将会话 ID 截断为 12 个字符，如果更长则追加 "..."。
func abbreviateID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "..."
}

// statusColor 返回与 daemon.SessionStatus 对应的 lipgloss 颜色。
func statusColor(status daemon.SessionStatus) lipgloss.Color {
	switch status {
	case daemon.StatusError:
		return lipgloss.Color("196") // 红色
	case daemon.StatusPermission:
		return lipgloss.Color("220") // 黄色
	case daemon.StatusBusy:
		return lipgloss.Color("45") // 青色
	case daemon.StatusRetry:
		return lipgloss.Color("208") // 橙色
	case daemon.StatusIdle:
		return lipgloss.Color("120") // 绿色
	case daemon.StatusUnknown:
		return lipgloss.Color("240") // 灰色
	case daemon.StatusArchived:
		return lipgloss.Color("243") // 暗灰色
	default:
		return lipgloss.Color("250") // 默认亮灰
	}
}

// ---------------------------------------------------------------------------
// 字符串工具函数
// ---------------------------------------------------------------------------

// leftAlign 将字符串左对齐，右侧用空格填充到指定显示宽度。
func leftAlign(s string, width int) string {
	sw := runewidth.StringWidth(s)
	if sw >= width {
		return s
	}
	return s + strings.Repeat(" ", width-sw)
}

// truncateString 将字符串缩短为 maxLen 个显示宽度，当源字符串超过 maxLen 时追加 "...".
func truncateString(s string, maxLen int) string {
	sw := runewidth.StringWidth(s)
	if sw <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return runewidth.Truncate(s, maxLen, "")
	}
	return runewidth.Truncate(s, maxLen-3, "...")
}

// ---------------------------------------------------------------------------
// 消息视图缓存
// ---------------------------------------------------------------------------

// rebuildMsgCache 构建并缓存当前会话消息的换行 + glamour 渲染行。
// 在进入 m 模式时和通过 h/l 切换消息时调用。
// j/k 滚动使用缓存行而非重新渲染。
func (m *DashboardModel) rebuildMsgCache() {
	if len(m.convMsgs) == 0 || m.msgIndex < 0 || m.msgIndex >= len(m.convMsgs) {
		m.cachedLines = nil
		return
	}

	// 弹窗尺寸（与 messageView 中的公式相同）
	dialogWidth := m.width * 90 / 100
	if dialogWidth < 50 {
		dialogWidth = 50
	}
	if dialogWidth > m.width-4 {
		dialogWidth = m.width - 4
	}
	contentWidth := dialogWidth - 6
	if contentWidth < 10 {
		contentWidth = 10
	}

	// 构建内容字符串
	var sb strings.Builder
	if m.cursor >= 0 && m.cursor < len(m.visible) {
		node := m.visible[m.cursor]
		if node.Type == NodeSession {
			title := node.Session.Title
			if title == "" {
				title = "(no title)"
			}
			sb.WriteString(fmt.Sprintf("Conversation — %s\n", title))
			dir := node.Session.Directory
			if dir == "" {
				dir = "(unknown)"
			}
			sb.WriteString(fmt.Sprintf("Directory: %s\n", dir))
		}
	}
	roleLabel := m.convMsgs[m.msgIndex].role
	if roleLabel == "" {
		roleLabel = "?"
	}
	roleDisplay := "user"
	if roleLabel != "user" {
		roleDisplay = "assistant"
	}
	ts := time.UnixMilli(m.convMsgs[m.msgIndex].timeCreated).Format("2006-01-02 15:04:05")
	sb.WriteString(fmt.Sprintf("─── Message %d/%d [%s] — %s ───\n",
		m.msgIndex+1, len(m.convMsgs), roleDisplay, ts))

	// 为助手消息渲染 markdown
	msgText := m.convMsgs[m.msgIndex].text
	if roleLabel != "user" && msgText != "" {
		rendered, err := glamour.Render(msgText, "dark")
		if err == nil {
			sb.WriteString(strings.TrimRight(rendered, "\n"))
		} else {
			sb.WriteString(msgText)
		}
	} else {
		sb.WriteString(msgText)
	}

	// 换行并缓存
	wrapped := lipgloss.NewStyle().Width(contentWidth).Render(sb.String())
	m.cachedLines = strings.Split(wrapped, "\n")
}
