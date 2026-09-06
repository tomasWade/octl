package views

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HelpModel 显示操作指南与第一列状态图标的含义。
// 纯静态内容，不依赖 daemon 数据。
type HelpModel struct {
	width  int
	height int
}

// NewHelpModel 创建一个新的 HelpModel。
func NewHelpModel() HelpModel {
	return HelpModel{}
}

// Init 返回空命令。
func (m HelpModel) Init() tea.Cmd {
	return nil
}

// Update 处理 Help 视图的消息（目前仅窗口尺寸）。
func (m HelpModel) Update(msg tea.Msg) (HelpModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	}
	return m, nil
}

// 样式
var (
	helpTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("229"))

	helpSectionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("39"))

	helpKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("45"))

	helpDescStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	helpMutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))
)

// View 渲染帮助内容。
func (m HelpModel) View() string {
	var b strings.Builder

	b.WriteString(helpTitleStyle.Render("Help — 操作指南与图标含义"))
	b.WriteString("\n\n")

	// 视图切换
	b.WriteString(helpSectionStyle.Render("视图切换"))
	b.WriteString("\n")
	writeHelpRow(&b, "1 / 2 / 3 / 4", "切换 Manage / Favorites / Stats / Help")
	writeHelpRow(&b, "Tab / Shift+Tab", "下一个 / 上一个视图")
	writeHelpRow(&b, "Ctrl+X / Ctrl+C", "退出")
	b.WriteString("\n")

	// Manage 视图按键
	b.WriteString(helpSectionStyle.Render("Manage 视图按键"))
	b.WriteString("\n")
	writeHelpRow(&b, "j/k 或 ↑/↓", "移动光标")
	writeHelpRow(&b, "h/l 或 ←/→ / Enter", "收起 / 展开节点")
	writeHelpRow(&b, "Space", "多选（project 全选/反选全部后代 session）")
	writeHelpRow(&b, "d", "删除（有选中时批量删除，否则删除当前子树）")
	writeHelpRow(&b, "e", "导出到 /tmp/octl-exports")
	writeHelpRow(&b, "m", "查看对话历史（h/l 翻消息，j/k 滚动）")
	writeHelpRow(&b, "n", "新建 session（project）/ fork（session）")
	writeHelpRow(&b, "s", "发送消息到当前 session")
	writeHelpRow(&b, "f", "收藏 / 取消收藏（有选中时批量切换）")
	writeHelpRow(&b, "r", "提示由 daemon 自动刷新")
	b.WriteString("\n")

	// Favorites 视图按键
	b.WriteString(helpSectionStyle.Render("Favorites 视图按键"))
	b.WriteString("\n")
	writeHelpRow(&b, "j/k 或 ↑/↓", "移动光标")
	writeHelpRow(&b, "Space", "多选")
	writeHelpRow(&b, "f", "取消收藏（有选中时批量移除）")
	writeHelpRow(&b, "d", "删除（先确认）")
	b.WriteString("\n")

	// 第一列状态图标含义
	b.WriteString(helpSectionStyle.Render("第一列图标含义"))
	b.WriteString("\n")
	writeHelpRow(&b, "🔵", "进行中（AI 正在思考/执行）")
	writeHelpRow(&b, "🟢", "空闲（等待输入）")
	writeHelpRow(&b, "🟡", "等待处理（权限确认或 agent 提问）")
	writeHelpRow(&b, "🟠", "重试中")
	writeHelpRow(&b, "🔴", "出错")
	writeHelpRow(&b, "❔", "状态未知")
	writeHelpRow(&b, "📦", "已归档")
	writeHelpRow(&b, "⭐", "已收藏（叠加在状态图标之后，间隔一个空格，如 🟢 ⭐）")

	return b.String()
}

// writeHelpRow 渲染一行 "按键/图标 + 说明"。
func writeHelpRow(b *strings.Builder, key, desc string) {
	b.WriteString("  ")
	b.WriteString(helpKeyStyle.Render(key))
	b.WriteString("  ")
	b.WriteString(helpDescStyle.Render(desc))
	b.WriteString("\n")
}
