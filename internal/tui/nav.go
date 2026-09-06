package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// ViewType 表示应用程序中的活动视图。
type ViewType int

const (
	// DashboardView 是默认的仪表板视图。
	DashboardView ViewType = iota
	// FavoritesView 用于显示收藏的 session 列表。
	FavoritesView
	// StatsView 用于显示全局用量统计信息。
	StatsView
	// HelpView 显示操作指南与图标含义。
	HelpView
	// viewCount 是用于循环的内部哨兵值。
	viewCount
)

// NavItem 表示导航侧边栏中的单个条目。
type NavItem struct {
	Icon     string
	Label    string
	ViewType ViewType
}

// NavItems 按顺序定义可用的导航项目。
var NavItems = []NavItem{
	{Icon: "📋", Label: "Manage", ViewType: DashboardView},
	{Icon: "⭐", Label: "Favorites", ViewType: FavoritesView},
	{Icon: "📊", Label: "Stats", ViewType: StatsView},
	{Icon: "❓", Label: "Help", ViewType: HelpView},
}

// NavModel 表示导航侧边栏组件。
type NavModel struct {
	// Items 是导航条目的列表。
	Items []NavItem
	// Selected 是当前活动条目的索引。
	Selected int
	// ContentHeight 是侧边栏可用的垂直空间。
	ContentHeight int
}

// NewNavModel 使用默认状态创建一个 NavModel。
func NewNavModel() NavModel {
	return NavModel{
		Items:    NavItems,
		Selected: 0,
	}
}

var (
	navStyle = lipgloss.NewStyle().
			Width(18).
			Border(lipgloss.RoundedBorder()).
			BorderTop(false).
			BorderBottom(false).
			BorderLeft(false).
			PaddingLeft(1).
			PaddingRight(0).
			PaddingTop(1)

	activeNavItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("39")).
				Bold(true)

	navItemStyle = lipgloss.NewStyle()
)

// View 将导航侧边栏渲染为带有右边框的垂直列表。
// 活动项以蓝色加粗高亮显示。
func (m NavModel) View() string {
	var lines []string
	for i, item := range m.Items {
		style := navItemStyle
		if i == m.Selected {
			style = activeNavItemStyle
		}
		lines = append(lines, style.Render(item.Icon+" "+item.Label))
	}

	navContent := lipgloss.JoinVertical(lipgloss.Top, lines...)

	// 填充到完整高度，使右边框延伸到整个侧边栏。
	if m.ContentHeight > len(lines) {
		for range m.ContentHeight - len(lines) {
			navContent += "\n"
		}
	}

	return navStyle.Render(navContent)
}
