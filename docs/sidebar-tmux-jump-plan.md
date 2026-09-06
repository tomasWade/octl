# Sidebar Tmux 跳转功能改动计划

> 分支：`feature/sidebar-tmux-jump` → 已手工迁移至 `merge-jump`
> 源实现基于：`master` (9d5e99c)；迁移目标：`merge-jump`（含 question 事件修复与 favorites 的最新 master）
> 状态：**已实现并完成迁移，`go test ./...` 全绿、`bun test` 684 pass / 0 fail，等待人工验收**

## 0. 实现状态

- [x] daemon 维护 `session -> pid -> tmuxPane/tmuxSession` 映射
- [x] daemon 在 event-source 连接断开时清理映射
- [x] daemon 将 process 信息填充到 ViewMsg / ViewSession（快照+注入，与 favorites 同模式）
- [x] hook 插件事件上报 `pid` / `tmuxPane` / `tmuxSession`
- [x] sidebar 插件 ↗ 按钮点击 session 切换或新建 tmux 会话
- [x] 单元测试（daemon / hook / sidebar）
- [x] 集成测试（socket 事件 → snapshot / view 链路）
- [x] 迁移至 `internal/plugins/templates/` 目录结构（go:embed 生成）
- [ ] 人工真机测试（等待用户）

测试统计（迁移后）：
- `go test ./...`：全部通过（含新增 4 个单测 + 3 个集成测试）
- `cd plugin && bun test`：684 pass / 0 fail

> 注意：bun 测试需要仓库根存在 `node_modules` 符号链接指向 `plugin/node_modules`
> （模板 tsx 从 `internal/plugins/templates/` 向上解析 `@opentui/solid` 时需要），
> `.gitignore` 已包含 `/node_modules` 规则。缺少该链接时 sidebar 测试文件整体
> 加载失败，表现为「1 个 @opentui 环境性 fail」。

## 1. 背景与目标

当前 `octl-sidebar` 插件可以显示 session 列表及状态，但还不能让用户点击后直接跳转到该 session 所在的 tmux pane。

本改动目标：
- 维护 `opencode session -> opencode pid -> tmux pane/session` 的轻量映射。
- sidebar 点击 session 时，若该 session 已附着在某个 tmux pane 中，则直接切换 focus；否则新建 tmux session 并打开该 opencode session。
- 仅处理在 tmux 内部运行的 opencode 进程；独立运行的 opencode 一律按“未找到”处理。

## 2. 核心设计

### 2.1 映射关系

```
opencode session ID
  └─ opencode process pid
       └─ tmux pane ID (e.g. %5)
       └─ tmux session ID (e.g. $0)
```

daemon 维护这个映射，并随 ViewMsg 广播给 sidebar。

### 2.2 关键数据类型

```go
// internal/daemon/types.go

// ProcessInfo 描述一个 opencode session 当前附着的进程和 tmux 位置。
type ProcessInfo struct {
    PID         int64  `json:"pid"`
    TMUXPane    string `json:"tmuxPane"`    // e.g. "%5"
    TMUXSession string `json:"tmuxSession"` // e.g. "$0"
    LastSeenAt  int64  `json:"lastSeenAt"`  // unix ms
}
```

```go
// internal/daemon/daemon.go

type StateManager struct {
    // sessionID -> 进程/tmux 信息
    sessionProcess map[string]*ProcessInfo

    // pid -> 该进程正在操作的 session 集合（用于连接断开时清理）
    pidIndex map[int64]map[string]struct{}
}
```

### 2.3 插件上报

`internal/plugins/templates/octl-hook.js`（由 `octl plugins` 生成到 opencode 的 plugins 目录）在每次事件里附带：

```json
{
  "type": "session.status",
  "properties": {
    "sessionID": "sess_xxx",
    "pid": 12345,
    "tmuxPane": "%5",
    "tmuxSession": "$0",
    "status": {"type": "busy"}
  }
}
```

获取方式：
- `pid`：`process.pid`
- `tmuxPane`：`process.env.TMUX_PANE`
- `tmuxSession`：通过 `tmux display-message -p "#{session_id}"` 获取（500ms 超时）

若 `TMUX_PANE` 不存在或为空，则不上报 `tmuxPane`/`tmuxSession`（值为 null），daemon 按未附着处理。

### 2.4 连接断开清理

插件与 daemon 是 Unix socket 长连接。连接断开时：

```go
func (m *StateManager) onDisconnect(pid int64) {
    for sessionID := range m.pidIndex[pid] {
        delete(m.sessionProcess, sessionID)
    }
    delete(m.pidIndex, pid)
}
```

这意味着 opencode 退出后，映射自动失效。

### 2.5 buildView 注入（迁移后方式）

master 侧 buildView 已有 favorites 的「RLock 快照 + 构建后循环注入」模式，迁移时
process 信息注入复用同一模式：在同一 RLock 段快照 `sessionProcess`，构建完成后在
同一循环里逐 session 注入 `IsFavorite` 与 `PID` / `TMUXPane` / `TMUXSession`。
与源实现（addViewSession 传 sm 逐节点读取）语义等价，且 addViewSession 保持纯函数。

## 3. 文件变更清单

### 3.1 插件侧（迁移后位于 templates，测试位于 plugin/）

- `internal/plugins/templates/octl-hook.js`
  - 事件里增加 `pid`、`tmuxPane`、`tmuxSession` 字段（`getTMUXSession()` 助手）。

- `internal/plugins/templates/octl-sidebar.tsx`
  - 导出 `makeTmuxSessionName` / `focusSession` / `CommandRunner`；`normalizeProject`
    与 `SidebarSession` 透传 `pid` / `tmuxPane` / `tmuxSession`。
  - session 行尾新增 ↗ 跳转按钮（位于 ★ 与 X 之间），左键点击执行 tmux 切换或新建。
  - 收藏 tab 条目行尾同样有 ↗ 按钮（条目反查时带上 pid/tmux 附着信息）。

- `plugin/octl-hook/octl-hook.test.js`
  - 补充测试，验证事件 payload 包含新字段（import 路径指向 templates）。

- `plugin/octl-sidebar/octl-sidebar.test.ts`
  - 补充 `makeTmuxSessionName` / `focusSession` / `normalizeProject`（pid/tmux 字段）测试。

### 3.2 Daemon 侧

- `internal/daemon/types.go`
  - 新增 `ProcessInfo` 结构体；`SessionState` 加 `ProcessInfo` 字段；
    `ViewSession` 加 `PID` / `TMUXPane` / `TMUXSession`。

- `internal/daemon/daemon.go`
  - `StateManager` 增加 `sessionProcess` 和 `pidIndex`；`clientConn` 记录 event-source 的 pid。
  - 新增 `updateProcessInfo`、`onDisconnect`、`eventPID`、`parsePIDFromRaw` 方法。
  - `processEvent` 全部 10 类非 deleted 状态事件（11 类事件中除 `session.deleted`）
    调用 `updateProcessInfo`，含 master 新增的 `question.*` 两类。
  - `buildView` 快照+注入 process 信息（见 §2.5）。

- `internal/daemon/daemon_test.go`
  - 测试映射更新（10 类事件表驱动）、pid 变更迁移索引、断开清理、ViewMsg 字段。

- `internal/daemon/daemon_integration_test.go`
  - 集成测试：socket 事件 → snapshot 含 ProcessInfo、断连清理、view 频道含 tmux 上下文。

### 3.3 文档侧

- 本文件 `docs/sidebar-tmux-jump-plan.md`（设计计划，含迁移注记）。
- `AGENTS.md`：wire protocol 事件附加字段、ViewMsg 新字段、daemon 映射与清理、sidebar ↗ 交互。

## 4. 详细流程

### 4.1 插件上报流程

```
opencode (in tmux pane)
  │
  ▼
octl-hook.js 读取 process.pid / TMUX_PANE / tmux session_id
  │
  ▼
发送事件到 daemon Unix socket
```

### 4.2 Daemon 处理流程

```
收到事件
  │
  ▼
提取 sessionID / pid / tmuxPane / tmuxSession
  │
  ▼
更新 sessionProcess[sessionID] 和 pidIndex[pid]
  │
  ▼
broadcast snapshot / view 给所有 subscriber
```

### 4.3 Sidebar 点击流程（迁移后为 ↗ 按钮）

```
用户点击 session 行尾 ↗ 按钮
  │
  ▼
读取该 session 的 tmuxSession / tmuxPane
  │
  ├─ 两者都存在 ──► tmux switch-client -t '<tmuxSession>'   # 切 tmux session
  │                  tmux select-window -t '<tmuxPane>'      # 切到 pane 所在 window
  │                  tmux select-pane -t '<tmuxPane>'        # 激活 pane
  │
  └─ 任一不存在 ──► 生成合法 tmux session 名
                    tmux has-session 检查：
                      已存在 → tmux switch-client -t '<name>'
                      不存在 → tmux new-session -d -s '<name>' -c <directory> "opencode --session <sessionID>"
                               tmux switch-client -t '<name>'
```

> 源实现为「点击 session 行任意位置跳转」；master 的 sidebar UI 已重构（行主文本
> 用于展开/折叠、行尾 ★ 收藏、X 删除），迁移后跳转改为行尾专用 ↗ 按钮，收藏 tab
> 条目行尾同样有 ↗。
>
> **真机修正一（打开命令）**：源实现用 `opencode run --session` 打开——`run` 是
> 非交互命令，缺 message 参数直接报 `You must provide a message or a command`
> 退出，导致 tmux session 创建后立即死亡（表现为「一闪」）。正确命令是 TUI 模式
> 的 `opencode --session <id>`，并以 `tmux new-session -c <dir>` 落在 session
> 所属目录。
>
> **真机修正二（跳转命令）**：tmuxSession 形如 `$29`，拼进 `sh -c` 时 `$29` 被
> 展开为位置参数（变成 `9`），报 `can't find session: 9`——所有 tmux 目标必须
> 单引号包裹。且 `select-pane` 不会切换 window（目标 pane 常在同 session 的
> 另一个 window），需要 `select-window -t <paneId>`（tmux 支持 pane id 直接作
> window 目标）先行切换。

## 5. tmux session 名处理

新建 tmux session 时，session 名直接复用 opencode session 的 **title**，截取前 15 个字符：

```text
<cleaned-title>
```

示例：
- title = "My Session" → `MySession`
- title = "My: Session/Name" → `MySessionName`
- title = "这是一个很长的会话标题" → `这是一个很长的会话标题`（15 字）
- title 为空 → 回退到清理后的 sessionId
- title 和 sessionId 都为空 → `unknown`

清理规则：
- 保留 `a-zA-Z0-9` 和中文字符
- 移除空格、标点和其他特殊字符
- 截取前 15 个字符

创建前检查是否已存在：

```bash
if tmux has-session -t "$tmux_session_name" 2>/dev/null; then
    tmux switch-client -t "$tmux_session_name"
else
    tmux new-session -d -s "$tmux_session_name" -c "$directory" "opencode --session $session_id"
    tmux switch-client -t "$tmux_session_name"
fi
```

> 注意：按 title 命名在多个 session 标题前 15 字相同时会冲突。当前行为是 `tmux has-session` 发现已存在时直接切过去，不再新建。

## 6. 边界情况

| 场景 | 预期行为 |
|---|---|
| opencode 正常在 tmux pane 运行 | ↗ 点击直接 `switch-client` + `select-pane` |
| opencode 退出 | daemon 清理映射，↗ 点击新建 tmux session |
| 插件未上报 tmuxPane/tmuxSession | 按未附着处理，↗ 点击新建 |
| tmuxPane 失效但 tmuxSession 存在 | 仍尝试切换（pane/Session 均存在才走跳转分支） |
| daemon 重启 | 映射丢失，等插件重新上报或 ↗ 点击新建 |
| 同一 tmux session 被多个 client 显示 | `select-pane` 会同步到所有显示该 session 的 client |
| 多个 terminal 连同一个 tmux server 但显示不同 session | `switch-client` 会先切到目标 session，再切 pane |
| 同一 session 换 pid（fork 后新进程接管） | `updateProcessInfo` 自动迁移 pidIndex 旧索引 |

## 7. 测试计划

### 7.1 单元测试

- daemon：`updateProcessInfo` 正确更新映射（10 类事件表驱动，含 question.*）。
- daemon：pid 变更时索引迁移。
- daemon：`onDisconnect` 正确清理映射。
- daemon：ViewMsg 包含 pid/tmux 字段。
- sidebar：`makeTmuxSessionName` 命名/清理/截断/回退规则。
- sidebar：`focusSession` 依据 tmuxPane 是否存在决定切换或新建命令序列（注入 runner）。
- sidebar：`normalizeProject` 解析 pid/tmux 字段并兜底默认值。
- octl-hook：事件 payload 包含 `pid`、`tmuxPane`、`tmuxSession`。

### 7.2 手动验证

- 在 tmux 中启动 opencode，确认 ↗ 能跳转到对应 pane。
- 退出 opencode，确认 ↗ 点击后新建 tmux session。
- 多 terminal / 同 tmux server 场景下验证跳转。
- 收藏 tab 中 ↗ 跳转行为与「全部」tab 一致。

## 8. 风险与限制

- 仅支持在 tmux 内部启动的 opencode。独立启动的 opencode 无法被跳转。
- 用户手动移动 pane（`move-pane`/`join-pane`）后，环境变量中的 `TMUX_PANE` 不会自动更新，映射会过期。但用户已确认不移动 pane，此限制可接受。
- `tmuxSession` 通过 `tmux display-message -p "#{session_id}"` 获取，要求插件运行时有权限调用 tmux 命令。
- 模板内容变化导致协议 MD5 变化：部署时需重新执行 `octl plugins` 生成插件并重启 opencode，否则 daemon 拒绝订阅（version mismatch）。

## 9. 后续可选增强

- 支持 `TMUX_PANE` 过期检测：当 `select-pane` 失败时，自动降级为新建。
- 支持从 DB 加载最近活跃的 session 列表，作为 daemon 重启后的初始状态。
- 支持 sidebar 中显示“未附着”状态图标。

## 10. 迁移注记（feature/sidebar-tmux-jump → merge-jump）

源分支基于旧目录结构（插件源码在 `plugin/` 下），master 已迁移至 `internal/plugins/templates/`
（go:embed + MD5 版本注入）。手工迁移（未用 git merge / cherry-pick）时的合流决策：

1. **插件源码位置**：pid 上报写入 `templates/octl-hook.js`，跳转逻辑（helper + ↗ 按钮）
   写入 `templates/octl-sidebar.tsx`；`plugin/` 下仅保留测试，import 指向 templates。
2. **事件覆盖**：master 修复了 question 事件（8→11 类），`processEvent` 全部 10 类非
   deleted 状态 handler（含 `question.asked`、`question.replied/rejected`）调用
   `updateProcessInfo`——hook 对全部 11 类事件附带 pid，保持映射新鲜。
3. **statusForSession 豁免**：PERMISSION/ERROR 的 DB 接管豁免（含 question 语义）原样保留，未受迁移影响。
4. **buildView 注入方式**：采用 master 的快照+注入模式（与 favorites 共用 RLock 段），
   替代源实现的 addViewSession 传 sm，语义等价。
5. **跳转交互**：源实现「点击行任意位置跳转」与 master 的展开/收藏/删除交互冲突
   （且 @opentui 的 span 不支持挂事件），改为行尾专用 ↗ 按钮；收藏 tab 条目同样加 ↗。
6. **协议版本**：模板内容变化使 ProtocolMD5 改变，`octl plugins` 重新生成后新旧
   插件与 daemon 通过版本握手自动识别。
