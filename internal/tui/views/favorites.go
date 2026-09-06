package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tomasWade/octl/internal/daemon"
)

// FavoritesRemoveRequestMsg 在用户于收藏视图中按下 f 键取消收藏时发送。
// 由 app 层处理：从 favoritesMap 中删除并广播 FavoritesChangedMsg。
type FavoritesRemoveRequestMsg struct {
	SessionIDs []string
}

// FavoritesModel 显示收藏的 session 列表，支持光标导航、多选和移除操作。
type FavoritesModel struct {
	view        daemon.ViewMsg  // 最近一次 daemon 推送的视图数据，用于反查 session 标题和状态
	favorites   map[string]bool // 收藏 session ID 集合
	favoriteIDs []string        // 有序的收藏 session ID 列表
	cursor      int
	selected    map[string]bool // 多选集合，与 dashboard 一致
	width       int
	height      int
	loaded      bool

	// 删除确认状态
	confirmingDelete bool     // 是否处于删除确认模式
	pendingDeleteIDs []string // 待删除的 session ID 列表
}

// NewFavoritesModel 创建一个新的 FavoritesModel。
func NewFavoritesModel() FavoritesModel {
	return FavoritesModel{}
}

// Init 返回空命令；数据由 daemon 推送驱动。
func (m FavoritesModel) Init() tea.Cmd {
	return nil
}

// Update 处理收藏视图的消息。
func (m FavoritesModel) Update(msg tea.Msg) (FavoritesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case ViewLoadedMsg:
		m.view = msg.View
		m.loaded = true
		m.rebuildFavoriteIDs()
		return m, nil

	case FavoritesChangedMsg:
		m.favorites = msg.Favorites
		m.rebuildFavoriteIDs()
		// 清理已不在收藏中的多选条目
		if len(m.selected) > 0 {
			for id := range m.selected {
				if !m.favorites[id] {
					delete(m.selected, id)
				}
			}
		}
		return m, nil

	case tea.KeyMsg:
		if !m.loaded {
			return m, nil
		}
		return m.handleKeyMsg(msg)
	}

	return m, nil
}

// rebuildFavoriteIDs 从 favorites map 重建有序的 ID 列表。
// 按 ViewMsg 中 session 的出现顺序排列，保证渲染确定性。
func (m *FavoritesModel) rebuildFavoriteIDs() {
	if len(m.favorites) == 0 {
		m.favoriteIDs = nil
		m.cursor = 0
		return
	}

	var ids []string
	seen := make(map[string]bool)
	// 先按 ViewMsg 中 session 的出现顺序收集
	for _, p := range m.view.Projects {
		for _, s := range p.Sessions {
			if m.favorites[s.SessionID] && !seen[s.SessionID] {
				ids = append(ids, s.SessionID)
				seen[s.SessionID] = true
			}
		}
	}
	// 补全收藏中存在但 ViewMsg 中尚未出现的 session（以防 daemon 尚未来得及推送）
	for id := range m.favorites {
		if !seen[id] {
			ids = append(ids, id)
		}
	}

	m.favoriteIDs = ids
	// 修正越界光标
	if m.cursor >= len(m.favoriteIDs) && len(m.favoriteIDs) > 0 {
		m.cursor = len(m.favoriteIDs) - 1
	}
	if len(m.favoriteIDs) == 0 {
		m.cursor = 0
	}
}

// handleKeyMsg 处理收藏视图的键盘事件。
func (m FavoritesModel) handleKeyMsg(msg tea.KeyMsg) (FavoritesModel, tea.Cmd) {
	// 确认模式下只处理 Enter 确认和 Esc/q 取消，其余按键忽略。
	if m.confirmingDelete {
		switch msg.String() {
		case "enter":
			// 执行删除：先发送删除 action，再发送收藏移除请求
			deleteMsg := ManageActionMsg{
				Action:     "delete",
				SessionIDs: m.pendingDeleteIDs,
			}
			removeMsg := FavoritesRemoveRequestMsg{SessionIDs: m.pendingDeleteIDs}
			m.confirmingDelete = false
			m.pendingDeleteIDs = nil
			return m, tea.Sequence(
				func() tea.Msg { return deleteMsg },
				func() tea.Msg { return removeMsg },
			)
		case "esc", "q":
			// 取消删除，恢复浏览模式
			m.confirmingDelete = false
			m.pendingDeleteIDs = nil
			return m, nil
		default:
			return m, nil
		}
	}

	switch msg.String() {
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}

	case "j", "down":
		if m.cursor < len(m.favoriteIDs)-1 {
			m.cursor++
		}

	case " ":
		// Space 多选：toggle 当前光标所在的 session
		if len(m.favoriteIDs) == 0 {
			return m, nil
		}
		if m.selected == nil {
			m.selected = make(map[string]bool)
		}
		id := m.favoriteIDs[m.cursor]
		if m.selected[id] {
			delete(m.selected, id)
		} else {
			m.selected[id] = true
		}

	case "f":
		// f 取消收藏：多选则移除全部选中 session，否则移除光标处 session
		if len(m.favoriteIDs) == 0 {
			return m, nil
		}
		var ids []string
		if len(m.selected) > 0 {
			for id := range m.selected {
				ids = append(ids, id)
			}
		} else {
			ids = append(ids, m.favoriteIDs[m.cursor])
		}
		if len(ids) > 0 {
			return m, func() tea.Msg {
				return FavoritesRemoveRequestMsg{SessionIDs: ids}
			}
		}

	case "d":
		// d 删除：先进入确认模式，Enter 确认后执行删除
		if len(m.favoriteIDs) == 0 {
			return m, nil
		}
		var ids []string
		if len(m.selected) > 0 {
			for id := range m.selected {
				ids = append(ids, id)
			}
		} else {
			ids = append(ids, m.favoriteIDs[m.cursor])
		}
		if len(ids) > 0 {
			m.confirmingDelete = true
			m.pendingDeleteIDs = ids
		}
	}

	return m, nil
}

// 收藏视图的渲染样式
var (
	favEmptyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	favHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("229"))

	favRowStyle = lipgloss.NewStyle().
			PaddingLeft(1)

	favCursorStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("57"))

	favMultiSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("62")).
				Bold(true)

	favCursorMarker = "▶ "

	favStatusColorMap = map[daemon.SessionStatus]lipgloss.Color{
		daemon.StatusIdle:       lipgloss.Color("251"),
		daemon.StatusBusy:       lipgloss.Color("39"),
		daemon.StatusPermission: lipgloss.Color("227"),
		daemon.StatusRetry:      lipgloss.Color("214"),
		daemon.StatusError:      lipgloss.Color("196"),
		daemon.StatusArchived:   lipgloss.Color("243"),
		daemon.StatusUnknown:    lipgloss.Color("241"),
	}
)

// truncateRunes 按 rune 数量安全截断字符串，超出部分用 … 代替。
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

// sessionByID 从缓存的 ViewMsg 中按 sessionID 查找 ViewSession。
// 若找不到则返回零值 ViewSession（title 为空，status 为 UNKNOWN）。
func (m FavoritesModel) sessionByID(sessionID string) daemon.ViewSession {
	for _, p := range m.view.Projects {
		for _, s := range p.Sessions {
			if s.SessionID == sessionID {
				return s
			}
		}
	}
	return daemon.ViewSession{}
}

// View 渲染收藏视图。
func (m FavoritesModel) View() string {
	if !m.loaded {
		return "Loading favorites..."
	}

	// 确认模式下渲染确认对话框，替代正常列表视图
	if m.confirmingDelete {
		return m.confirmView()
	}

	if len(m.favoriteIDs) == 0 {
		return favEmptyStyle.Render("No favorites yet — press f in Manage to favorite a session")
	}

	var b strings.Builder

	// 标题行
	b.WriteString(favHeaderStyle.Render("⭐ Favorites"))
	b.WriteString("\n\n")

	// 计算可视行数：减去标题和空行开销（标题 1 行 + 2 空行 = 3 行开销）
	visibleRows := m.height - 3
	if visibleRows < 1 {
		visibleRows = 1
	}

	// 计算滚动窗口
	start := 0
	end := len(m.favoriteIDs)
	if len(m.favoriteIDs) > visibleRows {
		// 确保光标在可视窗口内
		if m.cursor >= visibleRows {
			start = m.cursor - visibleRows + 1
		}
		if start+visibleRows > len(m.favoriteIDs) {
			start = len(m.favoriteIDs) - visibleRows
		}
		if start < 0 {
			start = 0
		}
		end = start + visibleRows
		if end > len(m.favoriteIDs) {
			end = len(m.favoriteIDs)
		}
	}

	// 渲染可见行
	for i := start; i < end; i++ {
		id := m.favoriteIDs[i]
		session := m.sessionByID(id)
		line := m.renderRow(id, session, i == m.cursor, m.selected[id])
		b.WriteString(line)
		b.WriteString("\n")
	}

	return b.String()
}

// renderRow 渲染单个收藏 session 行。
// 格式：状态图标 + 标题，前面缀光标标记或多选标记。
func (m FavoritesModel) renderRow(id string, session daemon.ViewSession, isCursor, multiSelected bool) string {
	// 状态图标（纯图标，无文字；含义见 Help 视图）
	statusIcon := session.RowStatus.Glyph()
	// 标题：若 ViewMsg 中有 session 信息则用实际标题，否则用 sessionID 兜底
	title := session.Title
	if title == "" {
		title = id
	}

	// 构建前缀（光标标记和多选标记不能同时出现，光标优先）
	var prefix string
	if isCursor {
		prefix = favCursorMarker
	}
	if multiSelected {
		if isCursor {
			prefix = favCursorMarker + "✓ "
		} else {
			prefix = "✓ "
		}
	}

	line := prefix + statusIcon + " " + title
	statusColor := favStatusColorMap[session.RowStatus]
	if statusColor == "" {
		statusColor = lipgloss.Color("241")
	}

	styled := lipgloss.NewStyle().Foreground(statusColor).Render(line)

	if multiSelected {
		// 多选行：先应用状态色前景，再叠加多选高亮背景
		padding := m.width - lipgloss.Width(styled)
		if padding < 0 {
			padding = 0
		}
		return favMultiSelectedStyle.Width(m.width).Render(styled + strings.Repeat(" ", padding))
	}
	if isCursor {
		// 光标行：先应用状态色前景，再叠加光标背景
		padding := m.width - lipgloss.Width(styled)
		if padding < 0 {
			padding = 0
		}
		return favCursorStyle.Width(m.width).Render(styled + strings.Repeat(" ", padding))
	}

	return favRowStyle.Render(styled)
}

// confirmView 渲染删除确认对话框。
// 显示待删除 session 列表及 Enter 确认 / Esc 取消提示。
func (m FavoritesModel) confirmView() string {
	count := len(m.pendingDeleteIDs)
	if count == 0 {
		return ""
	}

	var sb strings.Builder
	if count == 1 {
		sb.WriteString("Delete this favorite session?\n\n")
		session := m.sessionByID(m.pendingDeleteIDs[0])
		title := session.Title
		if title == "" {
			title = m.pendingDeleteIDs[0]
		}
		if len(title) > 50 {
			title = truncateRunes(title, 47)
		}
		fmt.Fprintf(&sb, "  %s\n", title)
		fmt.Fprintf(&sb, "  ID: %s\n\n", abbreviateID(m.pendingDeleteIDs[0]))
	} else {
		fmt.Fprintf(&sb, "Delete %d favorite sessions?\n\n", count)
		for _, id := range m.pendingDeleteIDs {
			session := m.sessionByID(id)
			title := session.Title
			if title == "" {
				title = id
			}
			if len(title) > 50 {
				title = truncateRunes(title, 47)
			}
			fmt.Fprintf(&sb, "  %s  (%s)\n", title, abbreviateID(id))
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
	if dialogWidth < 1 {
		dialogWidth = 40
	}

	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
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
