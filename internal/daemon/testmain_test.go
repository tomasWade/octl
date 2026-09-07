// Package daemon 测试无全局 setup 需求：底片目录重定向随周期落盘一并退役
// （writeReport 仅在显式传 dir 的测试中执行，不会写真实 ~/.local/share/opencode/daily/）。
// 保留空 TestMain 以便将来挂全局夹具。
package daemon

import (
	"testing"
)

var _ = testing.Short
