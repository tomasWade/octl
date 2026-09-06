package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// StatusBarModel 表示显示会话统计信息的底部状态栏。
type StatusBarModel struct {
	SessionCount   int
	ActiveSessions int
	TotalCost      float64
}

// NewStatusBarModel 创建一个统计信息归零的 StatusBarModel。
func NewStatusBarModel() StatusBarModel {
	return StatusBarModel{}
}

var statusBarStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("236")).
	Foreground(lipgloss.Color("252"))

// View 渲染状态栏，左侧对齐统计信息，右侧对齐退出提示。
func (m StatusBarModel) View(width int) string {
	left := fmt.Sprintf(" %d sessions | Active: %d | Cost: $%.2f",
		m.SessionCount, m.ActiveSessions, m.TotalCost)
	right := "[C-x] quit"

	padding := width - lipgloss.Width(left) - lipgloss.Width(right)
	if padding < 1 {
		padding = 1
	}

	bar := left + strings.Repeat(" ", padding) + right
	return statusBarStyle.Width(width).Render(bar)
}
