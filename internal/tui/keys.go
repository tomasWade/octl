// Package tui provides the terminal user interface for octl using Bubble Tea.
package tui

import (
	"github.com/charmbracelet/bubbles/key"
)

// KeyMap 定义 TUI 的所有按键绑定。
type KeyMap struct {
	Quit     key.Binding
	Tab      key.Binding
	ShiftTab key.Binding
	Up       key.Binding
	Down     key.Binding
	One      key.Binding
	Two      key.Binding
	Three    key.Binding
	Four     key.Binding
}

// Keys 持有全局按键绑定。
var Keys = KeyMap{
	Quit: key.NewBinding(
		key.WithKeys("ctrl+x", "ctrl+c"),
		key.WithHelp("C-x", "quit"),
	),
	Tab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next view"),
	),
	ShiftTab: key.NewBinding(
		key.WithKeys("shift+tab", "T"),
		key.WithHelp("S-Tab", "previous view"),
	),
	Up: key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "move down"),
	),
	One: key.NewBinding(
		key.WithKeys("1"),
		key.WithHelp("1", "dashboard"),
	),
	Two: key.NewBinding(
		key.WithKeys("2"),
		key.WithHelp("2", "favorites"),
	),
	Three: key.NewBinding(
		key.WithKeys("3"),
		key.WithHelp("3", "stats"),
	),
	Four: key.NewBinding(
		key.WithKeys("4"),
		key.WithHelp("4", "help"),
	),
}
