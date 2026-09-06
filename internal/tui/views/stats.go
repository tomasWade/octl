package views

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tomasWade/octl/internal/daemon"
)

// StatsModel 显示全局用量统计信息。
type StatsModel struct {
	stats  daemon.ViewStats
	width  int
	height int
	loaded bool
}

// NewStatsModel 创建一个新的 StatsModel。
func NewStatsModel() StatsModel {
	return StatsModel{}
}

// Init 返回空命令；数据现在由 daemon 推送。
func (m StatsModel) Init() tea.Cmd {
	return nil
}

// Update 处理统计视图的消息。
func (m StatsModel) Update(msg tea.Msg) (StatsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case ViewLoadedMsg:
		m.loaded = true
		m.stats = msg.View.Stats
		return m, nil
	}

	return m, nil
}

// 统计视图的样式。
var (
	statsBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			PaddingLeft(1).
			PaddingRight(1)

	statsHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Align(lipgloss.Center).
				Width(38)

	statsLineStyle = lipgloss.NewStyle().
			Width(38)

	statsLabelStyle = lipgloss.NewStyle().
			Width(20).
			Align(lipgloss.Right).
			Foreground(lipgloss.Color("252"))

	statsValueStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229"))
)

// View 渲染全局统计信息。
func (m StatsModel) View() string {
	if !m.loaded {
		return "Loading statistics..."
	}

	var lines []string
	lines = append(lines, statsHeaderStyle.Render("Global Statistics"))
	lines = append(lines, "")
	lines = append(lines, statsLineStyle.Render(
		statsLabelStyle.Render("Total Sessions:")+" "+
			statsValueStyle.Render(fmt.Sprintf("%d", m.stats.TotalSessions))))
	lines = append(lines, statsLineStyle.Render(
		statsLabelStyle.Render("Active Sessions:")+" "+
			statsValueStyle.Render(fmt.Sprintf("%d", m.stats.ActiveSessions))))
	lines = append(lines, statsLineStyle.Render(
		statsLabelStyle.Render("Total Cost:")+" "+
			statsValueStyle.Render(formatCost(m.stats.TotalCost))))
	lines = append(lines, statsLineStyle.Render(
		statsLabelStyle.Render("Total Tokens:")+" "+
			statsValueStyle.Render(formatTokens(m.stats.TotalTokens))))

	statsContent := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return statsBoxStyle.Render(statsContent)
}
