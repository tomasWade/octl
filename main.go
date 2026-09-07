package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tomasWade/octl/internal/daemon"
	"github.com/tomasWade/octl/internal/db"
	"github.com/tomasWade/octl/internal/manage"
	"github.com/tomasWade/octl/internal/plugins"
	"github.com/tomasWade/octl/internal/shadow"
	"github.com/tomasWade/octl/internal/tui"
)

// version 是 octl 自身版本号。默认 "dev"，发布时由构建脚本通过
// -ldflags "-X main.version=<UTC 时间戳>" 注入。
var version = "dev"

func main() {
	// 子命令 "plugins" 必须在标准 flag 解析前拦截，否则 --output 等参数会被顶层 flag 吞掉。
	if len(os.Args) > 1 && os.Args[1] == "plugins" {
		os.Exit(runPlugins(os.Args[2:]))
	}

	// 子命令 "query" 同样在标准 flag 解析前拦截：一次性查询 daemon 后退出，不进入 TUI。
	if len(os.Args) > 1 && os.Args[1] == "query" {
		os.Exit(runQuery(os.Args[2:]))
	}

	// 子命令 "report" 在标准 flag 解析前拦截：请求 daemon 生成底片并落盘。
	if len(os.Args) > 1 && os.Args[1] == "report" {
		os.Exit(runReport(os.Args[2:]))
	}

	// 动作类子命令（delete/create/fork/send/purge）在标准 flag 解析前
	// 拦截：一次性向 daemon 发送 action 并等待 result 后退出，不进入 TUI。
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "delete", "create", "fork", "send", "purge":
			os.Exit(runActionCommand(os.Args[1], os.Args[2:]))
		}
	}

	daemonMode := flag.Bool("daemon", false, "Run the octl daemon service (foreground)")
	flag.Bool("tui", false, "Run the TUI (default)")
	socketPath := flag.String("socket", "", "Path to the octl daemon Unix socket")
	refreshTime := flag.Int("refresh-time", 5, "Dashboard auto-refresh interval in seconds (0 = disable)")
	retentionDays := flag.Int("retention-days", 180, "Shadow archive retention in days (0 = keep forever)")
	showVersion := flag.Bool("version", false, "Print version and exit")

	// 标准 flag 的默认 Usage 只列出主命令 flag，而 plugins 子命令在
	// flag 解析前就被拦截（见上方 os.Args[1] 判断），不会出现在帮助里，
	// 因此这里自定义 Usage 补全子命令说明（主体抽为 printMainUsage 供测试）。
	flag.Usage = func() { printMainUsage(flag.CommandLine.Output(), flag.CommandLine) }
	// "help" 不是任何子命令，落进 flag 解析会直接启动 TUI，非常反直觉；
	// 显式拦截为标准帮助入口（输出走 stdout）。
	if len(os.Args) > 1 && os.Args[1] == "help" {
		flag.CommandLine.SetOutput(os.Stdout)
		flag.Usage()
		os.Exit(0)
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("octl version %s (protocol %s)\n", version, plugins.ProtocolMD5())
		os.Exit(0)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot get home directory: %v\n", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(home, ".local/share/opencode/opencode.db")

	if *daemonMode {
		runDaemon(dbPath, *socketPath, *retentionDays)
		return
	}

	// Default and --tui both launch the TUI.
	runTUI(dbPath, *socketPath, *refreshTime)
}

// printMainUsage 输出主命令帮助。抽为独立函数供 flag.Usage 与
// "octl help" 复用，同时作为 help 回归测试的直接断言对象。
// FlagSet 作为参数注入而非硬编码 flag.CommandLine——测试环境下全局
// CommandLine 被 testing 框架注册了 -test.* flag，直接打印会串台。
func printMainUsage(out io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(out, "octl - opencode session manager\n\n")
	fmt.Fprintf(out, "Usage:\n")
	fmt.Fprintf(out, "  octl [flags]           Launch the TUI (default)\n")
	fmt.Fprintf(out, "  octl --daemon [flags]  Run the daemon service (foreground)\n")
	fmt.Fprintf(out, "  octl plugins [flags]   Generate opencode plugin files\n")
	fmt.Fprintf(out, "  octl query [flags]     One-shot daemon query: snaps | sessions | messages <id> | daily\n")
	fmt.Fprintf(out, "  octl report [flags]    Write the daily raw digest (daily factual base)\n")
	fmt.Fprintf(out, "  octl delete [flags]    Delete sessions (fuzzy id match, batch)\n")
	fmt.Fprintf(out, "  octl create [flags]    Create a session with an initial message\n")
	fmt.Fprintf(out, "  octl fork [flags]      Fork a session with a new message\n")
	fmt.Fprintf(out, "  octl send [flags]      Send a message to a session\n")
	fmt.Fprintf(out, "  octl purge [flags]     Destroy deleted sessions in the shadow archive\n\n")
	fmt.Fprintf(out, "Flags:\n")
	fs.SetOutput(out)
	fs.PrintDefaults()
	fmt.Fprintf(out, "\nSubcommands:\n")
	fmt.Fprintf(out, "  plugins    Generate octl-hook.js and octl-sidebar.tsx opencode plugins\n")
	fmt.Fprintf(out, "             Usage: octl plugins --output=<dir>\n")
	fmt.Fprintf(out, "  query      Query the daemon without a TUI and exit\n")
	fmt.Fprintf(out, "             Methods: snaps | sessions | messages <sessionId> | daily\n")
	fmt.Fprintf(out, "             Run `octl query` with no arguments for full help\n")
	fmt.Fprintf(out, "  report     Write the daily raw digest to disk (daemon-side, markdown)\n")
	fmt.Fprintf(out, "             Usage: octl report [--date <day> | --from <t> [--to <t>]] [--dir <path>]\n")
	fmt.Fprintf(out, "  delete     Delete sessions without a TUI (fuzzy match, batch, --yes)\n")
	fmt.Fprintf(out, "             Usage: octl delete <sessionId>... [--yes]\n")
	fmt.Fprintf(out, "  create     Create a session with an initial message\n")
	fmt.Fprintf(out, "             Usage: octl create <message> [--dir <path>]\n")
	fmt.Fprintf(out, "  fork       Fork an existing session with a new message\n")
	fmt.Fprintf(out, "             Usage: octl fork <sessionId> <message> [--dir <path>]\n")
	fmt.Fprintf(out, "  send       Send a message to an existing session\n")
	fmt.Fprintf(out, "             Usage: octl send <sessionId> <message> [--dir <path>]\n")
	fmt.Fprintf(out, "  purge      Destroy sessions in the shadow archive (irreversible; only\n")
	fmt.Fprintf(out, "             sessions already deleted from opencode are eligible)\n")
	fmt.Fprintf(out, "             Usage: octl purge <sessionId>... [--yes]\n")
}

// runPlugins 处理 "octl plugins" 子命令：生成配套插件文件到指定目录。
func runPlugins(args []string) int {
	fs := flag.NewFlagSet("plugins", flag.ExitOnError)
	outputDir := fs.String("output", ".", "Output directory for generated plugin files")
	_ = fs.Parse(args) // ExitOnError：出错时已 os.Exit

	if err := plugins.Generate(*outputDir); err != nil {
		fmt.Fprintf(os.Stderr, "plugins: %v\n", err)
		return 1
	}
	fmt.Printf("generated octl-hook.js and octl-sidebar.tsx in %s\n", *outputDir)
	return 0
}

// runDaemon starts the octl state manager and blocks until it exits.
// 影子库打开失败视为致命错误：它承载"删除不失史"的职责，静默降级会让
// 用户误以为历史有保障。
func runDaemon(dbPath, socketPath string, retentionDays int) {
	database, err := db.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "DB error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = database.Close() }()

	shadowPath, err := shadow.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "shadow db path: %v\n", err)
		os.Exit(1)
	}
	shadowDB, err := shadow.Open(shadowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shadow db error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = shadowDB.Close() }()

	mgr := manage.New(database)
	sm := daemon.NewStateManagerWithSocket(database, socketPath)
	sm.SetManager(mgr)
	sm.SetShadow(shadowDB, retentionDays)
	if err := sm.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "state manager: %v\n", err)
		os.Exit(1)
	}
}

// runTUI starts the Bubble Tea TUI. All data and management operations are
// handled by the daemon over the Unix socket.
func runTUI(dbPath, socketPath string, refreshTime int) {
	p := tea.NewProgram(tui.New(refreshTime, socketPath), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
