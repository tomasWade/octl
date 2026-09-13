// statusbar.js — octl.sessions omarchy bar-widget 纯逻辑层。
//
// 双运行时兼容：QML 侧 `import "statusbar.js" as Statusbar` 直接调用顶层
// 函数声明（QML JS 资源不支持 export 语句）；bun 测试侧经 CommonJS 互操作
// 读 module.exports（文件上方无 package.json，bun 按 CJS 解析，QML 的 V4
// 引擎里 `module` 未定义、整段守卫跳过）。
//
// 数据来源：octl daemon `view` 频道推送的 ViewMsg（JSON Lines），
// projects[].sessions[] 为 ViewSession：sessionId/title/directory/status/
// pid(0=未知)/tmuxPane(空=不在 tmux)/tmuxSession(形如 "$29")。

// 栏上状态展示顺序（固定）：ERROR → PERMISSION(ASK) → RETRY → BUSY。
// IDLE/UNKNOWN/ARCHIVED 不上栏。
var STATUS_GROUPS = [
  ["ERROR", "🔴"],
  ["PERMISSION", "🟡"],
  ["RETRY", "🟠"],
  ["BUSY", "🔵"],
]

// 栏上计数用的状态集合（非 IDLE 且非忽略态）。
var COUNTED_STATUSES = { ERROR: true, PERMISSION: true, RETRY: true, BUSY: true }

// shQuote 把任意字符串安全地包进 sh 单引号字面量：
// 控制字符（换行等）先替换为空格防脚本注入，内嵌单引号用 '\'' 转义。
// tmuxSession 形如 "$29" 必须经此包裹，否则 sh 会把 $29 展开成位置参数。
function shQuote(v) {
  var s = String(v === null || v === undefined ? "" : v)
  s = s.replace(/[\r\n\t\0]/g, " ")
  return "'" + s.replace(/'/g, "'\\''") + "'"
}

// collectSessions 把 ViewMsg 的 projects 树摊平为 ViewSession 数组，
// 按 sessionId 去重（first-win）。favorites 与 projects 内容重复，
// 若调用方把两者拼在一起传入同结构对象也由这里的去重兜住。
function collectSessions(view) {
  var out = []
  var seen = {}
  var projects = (view && view.projects) || []
  for (var i = 0; i < projects.length; i++) {
    var sessions = (projects[i] && projects[i].sessions) || []
    for (var j = 0; j < sessions.length; j++) {
      var s = sessions[j]
      if (!s || !s.sessionId || seen[s.sessionId]) continue
      seen[s.sessionId] = true
      out.push(s)
    }
  }
  return out
}

// groupCounts 返回栏上要显示的计数组：固定 STATUS_GROUPS 顺序、仅非零，
// 形如 [{key:"ERROR", icon:"🔴", n:2}, ...]。
function groupCounts(sessions) {
  var byKey = {}
  var list = sessions || []
  for (var i = 0; i < list.length; i++) {
    var st = list[i] && list[i].status
    if (!st || !COUNTED_STATUSES[st]) continue
    byKey[st] = (byKey[st] || 0) + 1
  }
  var out = []
  for (var j = 0; j < STATUS_GROUPS.length; j++) {
    var key = STATUS_GROUPS[j][0]
    if (byKey[key]) out.push({ key: key, icon: STATUS_GROUPS[j][1], n: byKey[key] })
  }
  return out
}

// truncateTitle 截断标题用于弹层行展示（缺省 40 字符，超出加省略号）。
function truncateTitle(title, max) {
  var limit = typeof max === "number" && max > 0 ? max : 40
  var s = String(title === null || title === undefined ? "" : title)
  if (s.length <= limit) return s
  return s.slice(0, Math.max(1, limit - 1)) + "…"
}

// groupTooltip 构造 hover 弹层的分组列表：按 STATUS_GROUPS 顺序分组，
// 组内保持输入顺序，标题截断；总数超过 maxItems（缺省 8）时折叠，
// 余量计入 hidden（调用方渲染 "+N more"）。
// 返回 {groups: [{key, icon, items: [{sessionId, title, session}]}], hidden}。
function groupTooltip(sessions, maxItems) {
  var limit = typeof maxItems === "number" && maxItems > 0 ? maxItems : 8
  var byKey = {}
  var order = []
  var list = sessions || []
  for (var i = 0; i < list.length; i++) {
    var s = list[i]
    var st = s && s.status
    if (!st || !COUNTED_STATUSES[st]) continue
    if (!byKey[st]) {
      byKey[st] = []
      order.push(st)
    }
    byKey[st].push(s)
  }

  var groups = []
  var used = 0
  var hidden = 0
  for (var g = 0; g < STATUS_GROUPS.length; g++) {
    var key = STATUS_GROUPS[g][0]
    var items = byKey[key]
    if (!items || items.length === 0) continue
    var rows = []
    for (var k = 0; k < items.length; k++) {
      if (used < limit) {
        rows.push({
          sessionId: items[k].sessionId,
          title: truncateTitle(items[k].title, 40),
          session: items[k],
        })
        used++
      } else {
        hidden++
      }
    }
    groups.push({ key: key, icon: STATUS_GROUPS[g][1], items: rows })
  }
  return { groups: groups, hidden: hidden }
}

// makeTmuxSessionName 为 dead 场景新建 tmux session 生成合法名称：
// title 清理特殊字符（仅保留字母数字与中文）后截前 15 字，空回退
// sessionId，两者皆空回退 "unknown"。与 sidebar 插件同名函数同规则。
function makeTmuxSessionName(title, sessionId) {
  var source = String(title || "").trim() ? String(title).trim() : String(sessionId || "").trim()
  if (!source) return "unknown"
  var cleaned = source.replace(/[^a-zA-Z0-9\u4e00-\u9fa5]/g, "").slice(0, 15)
  return cleaned || "unknown"
}

// classifyJump 按 ViewSession 的 tmuxPane/tmuxSession/pid 字段把行点击
// 跳转分到四类：
//   attached  tmuxPane+tmuxSession 且 pid>0 —— opencode 活在 tmux pane 里，
//             该 tmux session 当前是否有 client 在看是运行时状态，由脚本
//             内 list-clients 分支处理（attached/detached 两行为合一脚本）；
//   detached  tmuxPane+tmuxSession 但 pid=0 —— 进程上报已断，直接拉终端附着；
//   bare      无 tmux 但 pid>0 —— 裸终端进程，沿 PPID 上溯聚焦其窗口；
//   dead      pid=0 —— 无进程信息，重建/复用 tmux session 拉起 opencode。
function classifyJump(s) {
  if (!s) return "dead"
  var inTmux = !!s.tmuxPane && !!s.tmuxSession
  var pid = Number(s.pid) || 0
  if (inTmux) return pid > 0 ? "attached" : "detached"
  if (pid > 0) return "bare"
  return "dead"
}

// hyprlandPidsSnippet 返回把 hyprctl clients -j 的窗口 pid 集合取到
// shell 变量 __octl_pids 的脚本片段（各脚本自包含，重复内联）。
function hyprlandPidsSnippet() {
  return '__octl_pids="$(hyprctl clients -j 2>/dev/null | grep -o \'"pid": *[0-9][0-9]*\' | grep -o \'[0-9][0-9]*\')"'
}

// buildJumpScript 生成的 sh 脚本由 widget 经 `sh -c` 本地执行，daemon 与
// wire 协议零参与。所有 tmux 目标均单引号包裹（$29/%5 不被 sh 展开）。
function buildJumpScript(s) {
  if (!s || !s.sessionId) return ""
  var kind = classifyJump(s)
  var sess = shQuote(s.tmuxSession)
  var pane = shQuote(s.tmuxPane)
  var pid = Math.max(0, Math.floor(Number(s.pid) || 0))
  var id = shQuote(s.sessionId)
  var dir = String(s.directory || "")

  if (kind === "attached") {
    // 运行时双分支：tmux session 有 client → 聚焦其终端窗口（client tty 的
    // 进程沿 PPID 上溯命中 hyprctl 窗口 pid）后 switch-client 导航到目标
    // pane；无 client → 拉起终端 attach（detached 行为）。任一环失败静默
    // 降级（focuswindow 已先行，后续命令 2>/dev/null 不影响前者）。
    return (
      "__octl_sess=" + sess + "\n" +
      "__octl_pane=" + pane + "\n" +
      '__octl_ctty="$(tmux list-clients -t "$__octl_sess" -F \'#{client_tty}\' 2>/dev/null | head -n 1)"\n' +
      'if [ -n "$__octl_ctty" ]; then\n' +
      "  " + hyprlandPidsSnippet() + "\n" +
      '  __octl_win=""\n' +
      '  for __octl_st in $(ps -t "$__octl_ctty" -o pid= 2>/dev/null); do\n' +
      '    __octl_p="$__octl_st"\n' +
      '    while [ -n "$__octl_p" ] && [ "$__octl_p" -gt 1 ] 2>/dev/null; do\n' +
      '      if printf \'%s\\n\' $__octl_pids | grep -qx "$__octl_p"; then __octl_win="$__octl_p"; break 2; fi\n' +
      '      __octl_p="$(ps -o ppid= -p "$__octl_p" 2>/dev/null | tr -d \' \')"\n' +
      "    done\n" +
      "  done\n" +
      '  if [ -n "$__octl_win" ]; then\n' +
      '    hyprctl dispatch focuswindow "pid:$__octl_win" 2>/dev/null\n' +
      "  fi\n" +
      '  tmux switch-client -c "$__octl_ctty" -t "$__octl_sess" 2>/dev/null\n' +
      '  tmux select-window -t "$__octl_pane" 2>/dev/null\n' +
      '  tmux select-pane -t "$__octl_pane" 2>/dev/null\n' +
      "else\n" +
      '  omarchy launch terminal tmux attach -t "$__octl_sess" 2>/dev/null\n' +
      "fi\n"
    )
  }

  if (kind === "detached") {
    // 进程已上报断连：不做 client 探测，直接拉终端附着既有 tmux session。
    return "omarchy launch terminal tmux attach -t " + sess + " 2>/dev/null\n"
  }

  if (kind === "bare") {
    // 裸终端进程：pid 沿 PPID 上溯命中某个 Hyprland 窗口则聚焦。
    return (
      hyprlandPidsSnippet() + "\n" +
      '__octl_p="' + pid + '"\n' +
      'while [ -n "$__octl_p" ] && [ "$__octl_p" -gt 1 ] 2>/dev/null; do\n' +
      '  if printf \'%s\\n\' $__octl_pids | grep -qx "$__octl_p"; then\n' +
      '    hyprctl dispatch focuswindow "pid:$__octl_p" 2>/dev/null\n' +
      "    exit 0\n" +
      "  fi\n" +
      '  __octl_p="$(ps -o ppid= -p "$__octl_p" 2>/dev/null | tr -d \' \')"\n' +
      "done\n"
    )
  }

  // dead：重建/复用以标题命名的 tmux session（TUI 模式 opencode --session，
  // 不能用 opencode run —— 非交互缺 message 即退，session 建立即死），
  // 再拉终端附着。shell-command 参数整体单引号包裹，交由 tmux 的 sh -c 执行。
  var name = shQuote(makeTmuxSessionName(s.title, s.sessionId))
  var cwdArg = dir ? " -c " + shQuote(dir) : ""
  var command = shQuote("opencode --session " + String(s.sessionId))
  return (
    "__octl_name=" + name + "\n" +
    'if ! tmux has-session -t "$__octl_name" 2>/dev/null; then\n' +
    '  tmux new-session -d -s "$__octl_name"' + cwdArg + " " + command + "\n" +
    "fi\n" +
    'omarchy launch terminal tmux attach -t "$__octl_name" 2>/dev/null\n'
  )
}

// CommonJS 导出（bun 测试用；QML V4 引擎中 module 未定义，整段跳过）。
if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    STATUS_GROUPS: STATUS_GROUPS,
    shQuote: shQuote,
    collectSessions: collectSessions,
    groupCounts: groupCounts,
    truncateTitle: truncateTitle,
    groupTooltip: groupTooltip,
    makeTmuxSessionName: makeTmuxSessionName,
    classifyJump: classifyJump,
    buildJumpScript: buildJumpScript,
  }
}
