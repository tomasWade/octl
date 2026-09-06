# octl — opencode Session Manager

> 本文档面向 AI coding agent。假设读者对项目一无所知，请从本文件开始了解。

## 项目概述

`octl` 是一个终端 TUI 工具，用于**浏览和管理**本机 opencode 的 SQLite 数据库中的会话（session）。

核心设计原则：

- **daemon 是唯一数据中心**：读 DB、状态推导、数据聚合、管理操作（delete/export/create/fork/send）与收藏维护（favorite/unfavorite，内存态）全部集中在 `internal/daemon`。
- **TUI 与 sidebar 只做渲染**：通过 Unix socket 接收 daemon 推送的完整 `ViewMsg`，维护自身 UI 状态。
- **TUI 不再本地读库**：`internal/tui` 不持有 `*db.DB`，数据完全来自 daemon。
- **数据库只读**：daemon 通过 `?mode=ro` 只读模式打开数据库，驱动层拒绝任何 SQL 写入。
- **删除走 opencode CLI**：不直接修改数据库，删除 session 通过 `opencode session delete` 执行；删除 project 通过 `manage.Manager.DeleteProject` 使用独立可写连接清理 `project` 表记录，不碰原始项目文件。
- **实时状态 daemon**：TUI 通过 Unix socket 连接独立 daemon 进程，获取 session 的实时状态（BUSY / IDLE / PERMISSION / ERROR 等）。
- **双进程运行**：必须先用 `octl --daemon` 启动 daemon，再启动 TUI（默认行为）。

## 技术栈

- **语言**：Go 1.25+（`go.mod` 中 `go 1.25.0`）
- **TUI 框架**：Bubble Tea（`github.com/charmbracelet/bubbletea`）+ bubbles / lipgloss / glamour
- **数据库**：SQLite，通过 `modernc.org/sqlite`（纯 Go，CGO-free）访问
- **Markdown 渲染**：`github.com/charmbracelet/glamour`
- **插件**：JavaScript/TypeScript (ES module / TSX)，Bun 运行时（opencode 内置）
- **无 ORM/查询构建器**：所有 SQL 直接嵌入 Go 源文件
- **无 Makefile / 无 CI / 无 lint 配置**

## 项目结构

```
main.go
  ├── query.go               "octl query" 子命令：一次性查询 daemon（snaps/sessions/messages/daily）
  ├── action.go              "octl delete/create/fork/send" 动作子命令：一次性 action + 等 result
  ├── report.go               "octl report" 子命令：请求 daemon 生成底片并落盘（日报事实层）
  ├── internal/tui/          Bubble Tea 应用外壳
  │     ├── app.go           顶层模型：视图切换、daemon 连接、消息路由、favoritesMap 共享收藏集合
  │     ├── nav.go           左侧导航栏（Manage / Favorites / Stats）
  │     ├── statusbar.go     底部状态栏（session 数 / 活跃数 / 费用）
  │     ├── keys.go          全局按键绑定
  │     └── views/           三个视图
  │           ├── dashboard.go   Manage 视图：project/session 树、管理操作请求、f 键收藏
  │           ├── favorites.go   Favorites 视图：收藏 session 列表（f 取消收藏 / d 删除）
  │           └── stats.go       Stats 视图：全局统计
  ├── internal/daemon/       Unix socket daemon + SocketClient
  │     ├── daemon.go        StateManager：事件处理、DB 同步、ViewMsg 构建/广播、action 执行
  │     ├── daily.go         "daily" 请求的时间窗口聚合（新增/活跃/归档/僵尸/卡住 + 摘录名额）
  │     ├── report.go        底片落盘：writeReport（raw 覆盖写）+ writeObituary（删除前全史讣告）+ 周期触发（4h 覆盖 + 昨日补写）
  │     ├── client.go        TUI 侧 Unix socket 客户端
  │     ├── query.go         QueryOnce：CLI 一次性"订阅+request+收 response"封装（dialSubscribed 与 ActionOnce 共享）
  │     ├── action.go        ActionOnce：CLI 一次性"订阅+action+收 result"封装
  │     └── types.go         SessionState、ViewMsg、wire protocol 消息类型
  ├── internal/db/           只读 SQLite 查询层
  ├── internal/manage/       管理操作封装（delete/export/create/fork/send）
  ├── internal/report/       日报底片渲染落盘（raw 覆盖写 + 讣告追加，纯机械层）
  ├── internal/types/        纯数据模型（Session、Project、MessagePart...）
  ├── cmd/                   辅助脚本
  │     └── dbtest/          针对真实数据库的手动集成测试工具
  ├── scripts/               发布与钩子（见「分支模型与发布」）
  │     ├── publish-github.sh    master→main 快照发布
  │     ├── publish-exclude.txt  私有内容排除清单
  │     └── octl.service      systemd 用户服务示例（daemon 常驻）
  │     └── hooks/pre-push       github remote 只放行 main/tags
  ├── internal/plugins/      插件模板与生成逻辑
  │     ├── gen.go           go:embed + MD5 + Generate
  │     ├── gen_test.go      生成逻辑测试
  │     └── templates/
  │           ├── octl-hook.js      事件转发插件模板
  │           └── octl-sidebar.tsx  sidebar 插件模板
  ├── plugin/                插件测试（Bun）
  │     ├── octl-hook/
  │     │     └── octl-hook.test.js Server 插件测试
  │     ├── octl-sidebar/
  │     │     └── octl-sidebar.test.ts  sidebar 测试
  │     ├── package.json     Bun 模块配置，仅用于 bun test
  │     └── bun.lock
  └── docs/                  设计文档（详见下方）
```

### 依赖规则

```
main → tui → views → daemon → {db, manage, types}
main --daemon → daemon → {db, manage, types}
```

`internal/db` 和 `internal/manage` 是仅有的依赖 `internal/types` 的包。`internal/tui` 不再依赖 `internal/db` 或 `internal/manage`。

### 关键文档

| 文档 | 内容 |
|------|------|
| `README.md` | 用户级快速上手指南 |
| `docs/architecture.md` | 模块依赖、TUI 视图、daemon 数据流、状态机 |
| `docs/data-model.md` | `project`/`session`/`message`/`part` 表结构、JSON 字段、关键 SQL |
| `docs/notification-design.md` | daemon / 插件 / TUI 的通知架构完整设计 |
| `docs/daemon-socket-design.md` | Unix socket 统一设计方案 |
| `docs/sidebar-tmux-jump-plan.md` | sidebar ↗ 跳转 tmux pane 的映射、清理与交互设计 |

**约定**：`docs/architecture.md` **不得**包含 SQL 或实现代码；schema 和 SQL 放在 `docs/data-model.md` 或代码注释中。

## 构建与运行

```bash
# 构建
go build -o octl .

# 1. 启动 daemon（前台运行，建议 supervisor/systemd 管理）
./octl --daemon

# 2. 启动 TUI（默认行为，--tui 可省略）
./octl
# 或显式
./octl --tui
```

### 命令行参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--daemon` | `false` | 前台启动 daemon 服务 |
| `--tui` | `false` | 显式启动 TUI（默认） |
| `--version` | `false` | 打印版本号与协议版本 MD5 后退出 |
| `--socket` | `~/.local/share/opencode/octl.sock` | Unix socket 路径 |
| `--refresh-time` | `5` | 已废弃；TUI 刷新由 daemon 推送驱动 |

另有 `plugins` 子命令：`octl plugins --output=<dir>` 生成配套插件（见下方「启用 opencode 插件」）。

### `query` 子命令（一次性查询，不开 TUI）

`octl query <method> [flags]` 在 flag 解析前拦截（与 plugins 同机制），一次性向 daemon 查询后退出。走 `internal/daemon.QueryOnce`（daily 走 `QueryDaily`，共用 `doQueryOnce` 核心）：新连接 → subscribe（频道用无推送的 `"query"`，注册为 subscriber 但不收初始数据）→ request → 按 ID 匹配收 response 后断开。复用 wire 协议的 `snapshot` / `listSessions` / `messages` / `daily` 四个 request 方法。

| 方法 | 说明 |
|------|------|
| `snaps` | 全部 session 实时状态（CLI 面名字；wire 方法仍为 `snapshot`） |
| `sessions` | project/session 树（root + 聚合状态） |
| `messages <sessionId>` | 对话内容；sessionId 模糊匹配（输入与 ID 转小写子串匹配，带不带 `ses_` 前缀均可，唯一命中才执行；多命中列候选、零命中报错） |
| `daily` | 时间窗口内的活动聚合（新增/活跃/归档/僵尸/卡住 + 摘要素材），供日报 skill 消费 |

- 默认输出 supervisorctl 风格表格（Glyph 彩色状态点 + 状态词、标题、project、相对年龄、去 `ses_` 前缀全小写 sessionID，lipgloss/Tokyo Night 配色，非 tty 自动去色）；`--json` 输出完整 `ResponseMsg`（stdout 纯 JSON）。
- `--nums` 消息选择器（仅 messages，默认 `0`）：`1`=第一条、`0`=最后一条、`-1`=上一条；范围 `3-5`/`4--2`/`-2-6`（降序自动交换）、列表 `1,3,-1`、`all`；解析在客户端做（先按 MessageID 分组再选），去重后按时间序输出；超界钳制（正溢出→最后一条，负溢出→第一条）。
- `daily` 窗口（仅 daily）：缺省=今天（本地时区自然日）；`--date <day>` 单日（纯日期格式，与 `--from/--to` 互斥）；`--from` 可单独用（`--to` 缺省 now）；只给 `--to` 报错；`from >= to` 报错；时间格式 `2026-09-04` / `2026-09-04T14:00` / `2026-09-04 14:00`。窗口语义 `[from, to)` 毫秒，daemon 侧校验 `0 < from < to`。聚合规则见 `internal/daemon/daily.go`：活跃=窗口内有 message；摘录名额=全局按窗口消息数取前 100（用户首条 120 rune / assistant 末条 500 rune）；僵尸=未归档且闲置 >48h 且 30d 内有活动（上限 20）；StuckStates=内存态 PERMISSION/ERROR。**octl 只输出事实，叙事总结由消费方（日报 skill）完成**。
- flags 位置不限：`reorderQueryArgs` 把"method 在前、flags 在后"的参数重排后再交给标准 flag 包（Go flag 遇到首个非 flag 参数即停止解析）。
- 退出码：`0` 成功；`1` 运行错误（daemon 未运行/超时/`ok:false`，含 `ErrDaemonConnect`/`ErrQueryTimeout` 哨兵）；`2` 用法错误。stdout 只出结果，人读信息走 stderr。
- 需要 daemon 运行中；query 不直接读 DB（遵守"daemon 是唯一数据中心"）。daemon 对未知 request method 返回 `ok:false` 错误（不静默超时）。

### `report` 子命令（底片落盘，日报事实层）

`octl report` 请求 daemon 生成底片并落盘（走 wire 协议 `report` 方法，daemon 侧执行 `buildDailyDigest` + 逐线 `GetUserSkeleton` + `internal/report` 渲染）：

- 默认当日窗口；`--date`（单日）/ `--from [--to]`（范围）；`--dir`（daemon 侧目录覆盖，缺省 `~/.local/share/opencode/daily/`）；`--json`。
- **文件语义**：`<date>.raw.md` 整体覆盖（临时文件 + rename 原子替换，date 取窗口起点本地日期）；`deleted/<date>.md` session 级追加（`O_APPEND`，永不覆盖）。
- **内容分层（严格机械，无判断）**：机器可读统计注释行（`<!-- stats: {...} -->`）+ 项目分节 + 每 session 元数据（id/标题/msgCount/时间窗/首末摘录）+ 当日用户消息骨架原文（SQL 层 part 截 1000、Go 层单条 2000 rune 上限）。
- **daemon 自动触发**（`internal/daemon/report.go`）：启动时 + 每 10 分钟检查——当日 raw 缺失或 mtime 超 4h 即覆盖写；昨日 raw 缺失且昨日有活动则补写终版、存在但停在昨日内则刷新。适配非服务器作息（开机即写）。
- **删除讣告**：`handleDeleteAction` 在执行删除前对展开后（含 cascade）的每个 session 拉全史骨架写讣告；失败只记日志不阻断删除。被删 session 的历史因此不随 DB 消失。
- 日报 skill 是消费方：叙事文件写 `daily/<date>.md`（判断层）；`octl report` 是它刷新底片的手动入口。完整的 skill 消费方示例见 `examples/skills/六耳/SKILL.md`。

### 动作子命令（`delete` / `create` / `fork` / `send`，一次性操作，不开 TUI）

`octl delete/create/fork/send` 在 flag 解析前拦截（与 plugins/query 同机制），一次性向 daemon 发送 action、同步等待 result 后退出。走 `internal/daemon.ActionOnce`：新连接 → subscribe（复用无推送的 `query` 频道）→ 发 action → 读循环跳过 `progress`/`subscribed`、按 type 匹配收 `result` 后断开；版本不匹配的错误 `response` 透传为 error。daemon 侧复用 wire 协议既有 action 分发与执行路径，零改动。

- `octl delete <sessionId>...`：批量删除；sessionId 模糊匹配（与 query messages 同一套：不区分大小写子串、唯一命中才执行），全部输入解析成功才执行（任一未命中/歧义整体失败，不会部分删除），按 ID 去重（`resolveActionTargets`）；tty 下默认列出目标并 `[y/N]` 确认（`--yes` 跳过；非 tty 直接执行，脚本友好——stdin 为字符设备即视为 tty，`/dev/null` 也会触发确认并因 EOF 取消，属安全方向误判）；**级联删除目标的全部子 session**（CLI 显式声明 `Cascade=true`，daemon 侧 `expandSessionTree` 展开，见 Action 协议），不会留下悬空 parentId 的孤儿数据。
- `octl create <message>`：新建 session；`--dir` 默认当前目录。
- `octl fork <sessionId> <message>` / `octl send <sessionId> <message>`：sessionId 模糊匹配；`--dir` 缺省时 CLI 不填 Directory，由 daemon 经 `resolveActionDirectory` 从 DB 补全。
- 通用 flags：`--json`（输出完整 `ResultMsg`）、`--socket`、`--timeout`（默认 60s——delete 在 daemon 侧同步逐个执行，批量耗时与数量成正比）。
- 退出码：`0` 成功（含确认时主动取消）；`1` 运行错误（daemon 未运行/超时/任一条目失败/result 带 Error）；`2` 用法错误。stdout 只出结果，人读信息走 stderr（与 query 一致）；成功行 `✓ <id> (<标题>)`、失败行 `✗` + 错误、末行汇总 `<action>: N succeeded, M failed`。
- 参数重排复用 `reorderArgs`（`reorderQueryArgs` 的通用化，valueFlags 含 `socket`/`timeout`/`dir`）；运行期错误经 `cliFatal`（`queryFatal` 的泛化，按动词输出前缀）。
- 需要 daemon 运行中；create/fork/send 返回的 result 只表示"进程已启动"（daemon 侧 `cmd.Start()` 后立即回），任务进度用 `octl query snaps` 查看。

### 运行依赖

- TUI 启动时会连接 `~/.local/share/opencode/octl.sock` 上的 daemon。
- 若 daemon 未运行，TUI **不会退出**，右上角显示 `🔴 OFFLINE`，每 5 秒自动重连；恢复后显示 `🟢 ONLINE`。
- TUI 不再直接读取数据库，所有展示数据来自 daemon 推送的 `ViewMsg`。
- 数据库路径硬编码为 `~/.local/share/opencode/opencode.db`（见 `main.go` 和 `cmd/dbtest/main.go`）。

### 启用 opencode 插件

使用 `octl plugins` 命令生成插件到 opencode 的 plugins 目录：

```bash
octl plugins --output=~/.config/opencode/plugins/
```

该命令会输出 `octl-hook.js` 和 `octl-sidebar.tsx` 两个文件，并注入与当前 `octl` 二进制匹配的协议版本常量。每个 opencode 实例启动时会自动加载 `octl-hook.js`，过滤 11 类目标事件并转发到 daemon（事件 properties 附带 `pid` / `tmuxPane` / `tmuxSession` 附着信息，供 daemon 维护 session→pid→tmux pane 映射并在连接断开时清理）；插件自带 500 条 FIFO 缓冲和断线 2 秒重连。

sidebar 插件通过 `~/.config/opencode/tui.json` 注册，指向 `~/.config/opencode/plugins/octl-sidebar.tsx` 的绝对路径。新版 sidebar 订阅 daemon 的 `view` 频道，直接渲染 `ViewMsg`（`normalizeProject` 对每个 session 透传 `parentId`/`hasChildren`/`depth`/`isFavorite`）。面板顶部为「全部」/「收藏」tab 切换栏：`activeTab` signal 默认 `"all"`，手写 text + `onMouseDown` 实现（不用 @opentui 的 `tab_select` 组件——其无鼠标交互），选中 tab 经 `tabTitleFg` 高亮 `#c0caf5`、未选中 `#565f89`，左键点击经 `switchTab` 纯函数切换（无效 tab 保持原值）；project/session 树在「全部」tab 渲染，收藏列表在「收藏」tab 渲染。「全部」tab 顶部另有状态 chip 过滤栏（`StatusFilterBar`）：单行动态渲染、只显示计数非零的状态 chip（`visibleChips`，3 字母缩写 ERR/ASK/RTY/BSY/IDL/UNK/ARC，PERMISSION 显示 ASK），chip = 状态色圆点 + 标签 + 实时计数，左键点击切换勾选（可多选组合），默认全不勾 = 不过滤，有勾选时**行首**出现 `✕` 一键清除（前置保证多 chip 撑满宽度时重置仍可达；sidebar 宽约 33 列，全量 7 chip 一行放不下，零计数状态过滤结果必空、无点击价值故隐藏）。过滤为纯客户端计算（`statusFilter` signal + `visibleProjects` memo），语义为剪枝保形：命中自身 status 的 session 及其整条祖先链保留（`filterSessionsKeepingAncestors`，visited 防环/悬空 parentId 安全），层级/缩进/折叠状态不丢；过滤后无可见 session 的 project 整组隐藏，全空时提示 `(no matching sessions)`；chip 计数由 `countStatuses` 统计（只看自身 status，不看 rowStatus 聚合）。并支持 project 级与 session 级两级鼠标折叠/展开（▶/▼ 图标，左键触发，非左键不响应）：project 标题显示下属 session 计数 (N)；有子节点的 session 默认折叠仅显示 root 行，折叠行标题显示直接 subsession 计数（格式 "标题 (N)"），展开后 subsession 按 depth 缩进渲染并显示自身状态。扁平 sessions 由 `buildSessionTree` 按 parentId 重建为多级树（悬空 parentId/自引用/depth≤0 按 root 处理）；父节点状态标记使用 daemon 推送的 `rowStatus` 聚合值（含全部后代最高优先级状态），折叠与展开均可见。折叠状态以 projectId / sessionId 为 key 在组件层保持，`ViewMsg` 刷新后不丢失。

**收藏（daemon 权威驱动）**：session 行已拆分为独立 text 子元素——行首展开图标 text（▶/▼，仅绑展开折叠，左键触发）+ 标题 text（点击切换收藏），固定 1 空格 gap；project 行同样在项目名前用 `statusColors` 渲染 `rowStatus` 聚合状态点。标题 hover 高亮（`hoveredId` signal，`onMouseOver`/`onMouseOut` 驱动，变色 `#c0caf5`）。收藏的**权威数据源是 daemon 推送的 `ViewMsg.Favorites` 与每个 session 的 `isFavorite` 字段**：每次收到 `view` 消息时，sidebar 将 `Favorites` 列表同步到本地 `favorites` signal（旧 daemon 无该字段时优雅降级为空收藏）。点击标题触发 `toggleFavoriteAction`：向 daemon 发送 `favorite`/`unfavorite` action（经 `sendAction`），并乐观更新本地 signal，由下一次 `ViewMsg` 确认调和。已收藏标题带 `★` 前缀并以 `#e0af68` 高亮；收藏列表（`★ Favorites`）在「收藏」tab 内显示，空收藏时显示 `(no favorites)` 提示，条目从所有 project 的 sessions 中反查标题/状态（状态点复用 `rowStatus` 聚合渲染父项、自身 `status` 渲染叶子，与「全部」树视图一致），已删除 session 自动跳过，点击条目可取消收藏。

**删除（X 按钮）**：session 行尾 `★` 之后为 ↗ 跳转按钮、再后为 X 删除按钮（`#f7768e` 红色独立 text，左键触发），project 标题行尾同样有 X——global project（哨兵值小写 `"global"`，`isGlobalProject` 防御性规范化比较，移除空白/零宽字符后小写匹配）不显示 X、不可删除。点击 X 打开 Portal 确认浮层（全屏遮罩 `stopPropagation` 阻止事件穿透，点击遮罩或 `[取消]` 关闭，`[确认删除]` 左键触发执行；提示文案含目标计数，如 `删除该 session 及其全部子 session？（共 N 个）`）。确认后经 `executeDelete` 执行：session 用 `collectDescendantIDs` 显式栈 DFS 收集自身+全部后代 sessionId（visited 防循环引用），project 用 `collectProjectSessionIDs` 先按 sessionId 去重再重建树收集全部 session 并附带 `projectId`（`confirmDeleteAction` 纯函数计算参数，global project 返回 null 不可删）；`sendAction` 扩展支持可选 `projectId` 字段（`buildActionPayload`），发送 `delete` action 后乐观清理本地 favorites（`removeIdsFromMap` 不可变删除，不等 daemon push）。daemon 的 `result` 消息携带执行结果——失败显示可见错误（`操作失败: ...`）、成功清除错误；`progress` 消息仅记录日志。

**tmux 跳转（↗ 按钮）**：session 行尾 `★` 与 X 之间为 ↗ 跳转按钮（`#7aa2f7` 蓝色独立 text，左键触发、stopPropagation 防冒泡到展开），收藏 tab 条目行尾同样有 ↗。`ViewSession` 的 `pid`/`tmuxPane`/`tmuxSession` 由 daemon 的 `sessionProcess` 映射注入（hook 事件上报、event-source 断连时按 pid 清理）。点击经 `focusSession` 执行，按点击源是否在 tmux 内（`process.env.TMUX`）分两路：**在 tmux 内**——已附着（pane 与 session 均已知）→ `switch-client`（切 tmux session）+ `select-window`（切到 pane 所在 window，pane id 可直接作 window 目标）+ `select-pane`（激活 pane）三连跳转（**tmuxSession 形如 `$29`，命令中必须单引号包裹**，否则 `$29` 被 sh 展开为位置参数导致 `can't find session: 9`）；未附着 → `makeTmuxSessionName`（title 清理特殊字符保留字母数字与中文、截 15 字，空回退 sessionId/unknown）命名，`tmux has-session` 已存在直接切换，否则 `tmux new-session -d -s <name> -c <directory>` 后以 TUI 模式 `opencode --session <id>` 打开再切换（**不可用 `opencode run --session`**——run 缺 message 会报错退出导致 tmux session 秒死）。**不在 tmux 内**——switch/select 类命令需要 tmux 客户端上下文（执行只会报 `no current client`），故跳过全部切换命令：attached 目标直接提示"目标正在 tmux <session> 的 pane <pane> 运行，无法自动跳转"；未附着目标照常创建/复用 tmux session（`focusSession` 的 `autoSwitch:false` 选项），顶部状态行提示"已创建/复用 tmux session「<name>」，请手动切换"。跳转结果与失败统一走顶部状态行（`error` signal，与 offline/操作失败同通道同位置）；view 消息不清除 error（推送高频会秒冲提示），清除点只有 connect 成功与 result 成功。命令执行器可注入（`CommandRunner`），便于单测验证命令序列。

## 测试

```bash
# 所有 Go 单元测试（使用 modernc.org/sqlite 临时数据库）
go test ./...

# 插件测试（需要 Bun）
cd plugin && bun install && bun test
```

> 注意：`octl-sidebar.test.ts` 会从 `../internal/plugins/templates/` 导入 TSX 模板，bun 从模板位置向上解析 `@opentui/solid` 时依赖仓库根目录的 `node_modules` symlink（指向 `plugin/node_modules`）。全新 clone 后先在仓库根目录执行 `ln -sfn plugin/node_modules node_modules`（CI 已内置此步骤）。

```bash
# 手动检查真实数据库 schema；无 DB 时跳过
go test -run TestRealDBSchema
```

### 测试约定

- `db` 包的所有测试都调用 `setupTestDB(t)`，它会创建临时 SQLite 数据库，包含 `session`/`message`/`part` 表结构并插入测试数据，然后通过 `db.New()` 以只读模式打开。
- 部分测试（如 `TestRealDBSchema`）在真实数据库 `~/.local/share/opencode/opencode.db` 不存在时使用 `t.Skip` 跳过。
- `cmd/dbtest` 提供了针对真实数据库的手动集成测试工具：

  ```bash
  go run ./cmd/dbtest
  ```

### 测试覆盖要点

- `internal/db`：查询正确性、NULL 列的 COALESCE 处理。
- `internal/manage`：导出/创建/fork/发送 JSON 结构、批量操作 Summary、project 删除保护。
- `internal/daemon`：11 类事件处理、`deriveFromDB`、DB 同步规则、空 sessionID 忽略、并发安全、`buildView` 聚合、`ViewMsg` 广播、action 分发、message request 响应；收藏（favorites_test.go / buildview_favorites_test.go / prune_test.go）：`favorite`/`unfavorite` action 幂等批量处理、`buildView` 注入 `IsFavorite` 并按插入顺序组装 `Favorites`（孤儿收藏跳过）、删除联动剪枝三路径（`handleDeleteAction` / `session.deleted` 事件 / `syncFromDB` tombstone 扫描）。
- `internal/tui/views`：从 `ViewMsg` 构建树、展开状态保持、光标恢复、状态颜色/图标映射、action 请求发送、progress/result 处理、Space 多选/批量删除、滚动条窗口渲染与 clamp。
- `internal/daemon`（query_test.go）：`QueryOnce` 三方法 happy path、未知 session 返回空消息、daemon 未运行（`ErrDaemonConnect`）、静默连接超时（`ErrQueryTimeout`）。
- `internal/daemon`（daily_test.go）：`buildDailyDigest` 分类（新增/活跃/归档/project 分组费用/活跃排序/摘录标记）、摘录 rune 截断（120/500 含省略号）、摘录名额（名额外的 HasExcerpt=false，名额按窗口消息数分配）、僵尸规则（48h/30d/归档豁免）、StuckStates（内存态 PERMISSION/ERROR）、`handleRequestMsg` daily 参数校验（from>=to、from=0、正常应答）与未知 method 错误。
- `internal/db`（daily_test.go）：`GetMessageActivity` 窗口边界（半开区间含 from 排 to/空窗口）、`GetSessionExcerpts` 角色定位（user/assistant/缺失角色/不存在 session）、窗口边界（assistant 只受 to 限制）、单 part SQL 预截断。
- `internal/daemon`（action_test.go）：`ActionOnce` 全链路（favorite/unknown action 经真实 StateManager）、daemon 未运行、静默连接超时、`resolveActionDirectory` 显式透传/DB 补全/session 不存在/无 directory 报错、fork 缺 Directory 且 session 不存在时在 exec 前失败、`expandSessionTree` 子树展开表驱动（多级后代/多输入去重/parent 环终止/自引用按叶子/未知 ID 保留/空输入）与 `handleDeleteAction` 级联集成（Cascade=true 时 summary.Results 的 ID 集合覆盖全子树、缺省 false 不展开只删请求 ID——保证既有客户端语义不变）。
- 根包（query_test.go）：`--nums` 解析全语法形态与钳制、消息按 MessageID 分组、模糊匹配（大小写/前缀/零命中/多命中）、ID 展示规范化、相对年龄、ANSI 感知补齐、`reorderQueryArgs` 参数重排（含 daily 值型 flag）、`parseDailyWindow` 全形态（缺省今天/单日/互斥/只给 to/from>=to/时间格式）、daily 用法错误退出码、`renderDaily` 渲染 smoke（均在连接 daemon 前校验）。
- `internal/report`（report_test.go）：raw 渲染（统计注释行/项目分节/骨架时间戳真值防 layout 字面量回归/oneLine 压行/空态）、覆盖写无 tmp 残留、讣告追加语义与顺序、ExpandDir。
- `internal/db`（daily_test.go 之 TestGetUserSkeleton）：role 过滤、多 part 拼接、SQL 层 1000 / Go 层 2000 rune 双重截断、窗口半开区间、空 session。
- `internal/daemon`（report_test.go + testmain_test.go）：writeReport 落盘产物、maybeWriteReports 节流（缺失即写 / 4h 内不重写 / 超 4h 覆盖 / 昨日无活动不建空文件）、handleDeleteAction 前置讣告（CLI 删除失败也不影响讣告已写）、`report` request 参数校验与 dir 覆盖；TestMain 全局注入底片目录防测试污染真实 `~/.local/share/opencode/daily/`。
- 根包（report_test.go）：printReportUsage 帮助回归、runReport 用法错误（均在连接 daemon 前校验）。
- 根包（action_test.go）：fake daemon 端到端（snapshot 候选应答 + action 记录 + result 回放）——delete 模糊匹配/批量去重/零命中/歧义不发 action、create 的 message 与 --dir 默认 cwd/显式覆盖、fork/send 的 SessionID/Message/Directory 留空、result 失败与 result.Error 的退出码、用法错误表驱动、`renderActionResult` 渲染与退出码、`confirmAction` 输入解析、`resolveActionTargets` 去重与整体失败。
- `internal/tui`（nav_test.go）：ViewType 枚举与 NavItems 三视图顺序、数字键 1/2/3 切换、Tab/Shift+Tab 循环、app 层 `FavoritesToggleRequestMsg`/`FavoritesRemoveRequestMsg` 的 toggle 语义（对已收藏发 `unfavorite`、未收藏发 `favorite` action + 乐观更新）、`daemonViewMsg` 以 `ViewMsg.Favorites` 重建 `favoritesMap`（daemon 权威源，陈旧本地项丢弃）并广播 `FavoritesChangedMsg`。
- `internal/tui/views`（favorites_test.go）：`rebuildFavoriteIDs` 排序（按 ViewMsg 出现顺序 + 补全未出现 ID）、光标 clamp、Space 多选、f 取消收藏（多选/单条，发 `FavoritesRemoveRequestMsg`）、d 删除（delete action + 收藏移除的 `tea.Sequence`）、滚动窗口、空态提示、状态色映射。
- `plugin`：事件过滤、buffer FIFO、sidebar helper 函数（含 project/session 折叠、`buildSessionTree` 树重建、`normalizeProject` 字段透传（含 pid/tmux）、`tabTitleFg`/`switchTab` tab 高亮与切换、状态 chip 过滤纯函数（`STATUS_CHIPS` 定义/`visibleChips` 非零筛选/`filterActive`/`sessionMatchesFilter`/`toggleStatusFilter`/`filterSessionsKeepingAncestors` 剪枝保形含祖先链保留/环/悬空 parentId/顺序保持/`countStatuses` 只计自身 status）、`favoritesEmptyHint` 收藏空态提示、`toggleFavoriteImpl` 收藏切换不可变性、`sessionRowColor` 状态点着色、删除相关纯函数 `isGlobalProject`/`buildActionPayload`/`collectDescendantIDs`/`collectProjectSessionIDs`/`confirmDeleteAction`/`removeIdsFromMap`、tmux 跳转 `makeTmuxSessionName`/`focusSession`（注入 runner 验证命令序列）、hook 事件 payload 携带 pid/tmux 字段、`ViewMsg.Favorites` 同步本地 signal 与 isFavorite 兜底）、ViewMsg 解析。

## 代码风格与约定

### 语言

- 源码注释以中文为主，代码标识符、类型名、JSON 字段名使用英文。
- 文档（README、docs）以中文为主。

### 包组织

- `internal/types`：纯结构体，**禁止**包含业务逻辑。
- `internal/db`：只读查询，不允许写操作；所有可写操作在 `internal/manage` 中通过 `db.DB.NewWritable()` 显式开启。
- `internal/manage`：所有破坏性/管理操作集中在此，必要时调用 `os/exec` 执行 `opencode` CLI。
- `internal/daemon`：事件处理、状态管理、视图构建、管理操作执行；`StateManager` 通过单 goroutine `select` 串行化事件和 DB 同步。
- `internal/tui` 与 `internal/tui/views`：Bubble Tea 的 MVU 模型，消息类型和 `Update`/`View`/`Init` 方法保持规范；**不直接访问数据库**。

### 命名

- Go 导出的类型/方法使用 CamelCase，首字母大写。
- 内部类型/辅助函数使用 camelCase。
- 测试函数使用 `Test<功能>_<场景>` 命名，表驱动测试配合 `t.Run`。

### 错误处理

- 使用 `fmt.Errorf("...: %w", err)` 包装错误。
- DB 层定义哨兵错误 `ErrSessionNotFound`。
- daemon 的 `processEvent` 对非法/未知事件静默丢弃，不 panic。

## SQL 与数据访问约定

### 只读原则

- `db.New(path)` 以 `path + "?mode=ro"` 打开数据库，任何 SQL 写入都会被驱动层拒绝。
- 不要显式指定 `_journal_mode=wal`，因为 opencode 可能以不同锁模式持有数据库，WAL 读者会产生 disk I/O error (522)。

### NULL 处理

所有可空的 `int64` / `string` 列都使用 `COALESCE(col, 0)` / `COALESCE(col, '')`，因为 `sql.Rows.Scan` 到 `int64` 在遇到 NULL 时会失败：

```sql
COALESCE(s.parent_id, '') as parent_id,
COALESCE(s.time_compacting, 0) as time_compacting,
```

### JSON 列

- `model` 列存储 JSON：`json_extract(model, '$.id')`、`json_extract(model, '$.providerID')`。
- `message.data` 中抽取 `role`、`agent`、`model`。
- `part.data` 中抽取 `type`、`text`。

### 计算字段

- `MessageCount` 是子查询：`(SELECT COUNT(*) FROM message m WHERE m.session_id = s.id)`，不是 `session` 表列。

### 时间字段

- 数据库中所有 `time_*` 列是 **unix 毫秒整数**。
- Go 中映射为 `int64`；0 值表示"从未发生"。

## Daemon 架构

### 进程模型

- `octl --daemon` 启动独立前台进程。
- 监听 Unix socket：`~/.local/share/opencode/octl.sock`。
- TUI 和 sidebar 通过 `internal/daemon.SocketClient` 连接并订阅 `view` 频道。

### 连接角色

同一 socket 上同时有两种客户端：

| 角色 | 行为 | 示例 |
|------|------|------|
| 事件源（event source） | 连接后直接发送裸事件 JSON，不 subscribe | `octl-hook.js`（由 `octl plugins` 生成） |
| 订阅者（subscriber） | 连接后发送 `{"type":"subscribe","channels":["view"]}`，接收 ViewMsg | `octl` TUI、`octl-sidebar.tsx`（由 `octl plugins` 生成） |

daemon 读取新连接的第一行 JSON 来区分角色。

### 事件类型（11 类）

`session.status`、`session.idle`、`session.created`、`session.deleted`、`session.error`、`permission.asked` / `permission.updated`、`permission.replied`、`question.asked`、`question.replied`、`question.rejected`、`session.compacted`。

> `question.*` 对应 opencode 的 question 工具（agent 向用户提问并阻塞等待）。该等待态**不会**体现在 `session.status`（只有 idle/retry/busy），只能靠 `question.asked` 感知；daemon 将其与权限确认同义映射为 `PERMISSION`（UI 统一显示 🟡 ASK）。

### ViewMsg

daemon 构建并推送的完整视图消息，包含：

- `projects`：按 project 分组的会话树，每个 session 包含 `status`（自身状态）和 `rowStatus`（root session 聚合状态），以及 tmux 跳转所需的 `pid` / `tmuxPane` / `tmuxSession` 附着信息（daemon 的 `sessionProcess` 映射注入）。
- `stats`：全局聚合（TotalSessions / ActiveSessions / TotalCost / TotalTokens）。
- `favorites`：按插入顺序预组装的收藏 session 列表（`ViewSession` 完整对象，含 `isFavorite` 标记）；每个 session 行还带 `isFavorite` 字段，由 daemon 收藏集合注入。

### Action 协议

TUI → daemon：`{"type":"action","action":"delete|export|create|fork|send|favorite|unfavorite",...}`

- `favorite` / `unfavorite`：批量收藏/取消收藏，`SessionIDs`（`json:"sessionIds"`）指定目标；幂等——已收藏的 session 执行 favorite 为 no-op（仍返回成功），未收藏的执行 unfavorite 同理。执行完成后 daemon 重新 `buildView()` 并 `pushView()`。
- `delete`：可选 `Cascade`（`json:"cascade,omitempty"`）为 true 时，daemon 先经 `expandSessionTree` 把请求的 ID 集合按 DB 的 parent_id 关系扩展为"自身 + 全部后代"（显式栈 DFS，visited 去重防环/多输入交集、自引用按叶子、DB 不存在的 ID 原样保留、ListSessions 失败退化为只删指定 ID）再逐个执行。**缺省 false 保持原语义**（只删请求的 ID，级联由调用方决定）：TUI dashboard/sidebar 在客户端自行收集全量后代（`collectSessionIDs`/`collectDescendantIDs`），Favorites 视图只删选中条目本身——既有客户端行为不受影响；CLI `octl delete` 显式传 `cascade=true` 获得服务端级联。
- `fork` / `send`：`Directory` 为空时 daemon 通过 `resolveActionDirectory` 从 DB 查该 session 的 directory 自动补全（查不到 session 或 directory 为空时报错，不向 opencode 传递空 `--dir`）；非空直接透传。
- `create` / `fork` / `send` 在 daemon 侧 `cmd.Start()` 后台启动 `opencode run` 后**立即**返回 result（表示"已启动"而非"已完成"）；`delete` 同步执行，批量耗时与数量成正比。
- `progress` 与 `result` 都写回**发起 action 的连接**（`cl.write`），不广播——这是 CLI `ActionOnce` 能同步等待结果的前提。

daemon → TUI：`{"type":"progress",...}`、`{"type":"result","summary":{...}}`

action 执行完成后，daemon 会重新 `buildView()` 并 `pushView()`，所有订阅者自动刷新。

### DB 同步规则（每 30 秒）

- DB 有、内存无：新建 entry，`status = deriveFromDB()`，`source = DB`。
- 事件态且 `LastEventAt < 60s`：保留事件态，仅更新 title/projectID。
- 事件态且 `LastEventAt >= 60s`：DB 接管，状态由数据库重新推断，`source = DB`。例外：`PERMISSION` 和 `ERROR` 状态豁免接管（`syncFromDB` 与显示路径 `statusForSession` 一致豁免）——权限请求、提问和错误信息只存在于 opencode 内存中，DB 无法还原；`PERMISSION` 保持到 `permission.replied` / `question.replied` / `question.rejected` / `session.idle` / `session.error` 事件解除，`ERROR` 保持到 `session.idle`（清 ErrorMsg）或下一个状态事件解除。`RETRY` 不豁免——重试仍在工作，接管降级为 `BUSY` 语义合理。豁免的代价：若 opencode 进程在等待期间退出且未发解除事件，状态会保持到 daemon 重启 / session 删除 / 该 session 产生新事件为止（已接受的取舍）。
- DB 态：刷新 `status = deriveFromDB()`。
- DB 无、内存有：标记 tombstone；下一周期仍无则删除。

`deriveFromDB` 逻辑：

1. `TimeArchived > 0` → `ARCHIVED`
2. 最后一条消息 role 为 `"assistant"`：`time.completed` 非零 → `IDLE`；缺失/为零 → `BUSY`（消息仍在生成中，如长 shell 命令执行）
3. 否则 → `UNKNOWN`

### Wire Protocol（JSON Lines）

- 插件 → daemon：裸事件，如 `{"type":"session.status","properties":{"sessionID":"...","status":{"type":"busy"},"pid":123,"tmuxPane":"%5","tmuxSession":"$0"}}`（hook 对全部 11 类事件附带 pid/tmux 附着信息；daemon 按 pid 维护 `sessionProcess`/`pidIndex` 映射，event-source 断连时按 pid 清理）
- TUI/sidebar → daemon：`subscribe`、`ping`、`request`（`snapshot|listSessions|messages|daily|report`，daily/report 带 `from`/`to` 窗口毫秒，report 另可带 `dir`）、`action`（`delete|export|create|fork|send|favorite|unfavorite`）
- daemon → TUI/sidebar：`subscribed`、`view`（含 `favorites`/`isFavorite`）、`pong`、`response`（daily 应答带 `daily` 字段）、`progress`、`result`

## TUI 视图与按键

### 视图

| 按键 | 视图 | 文件 |
|------|------|------|
| `1` | Manage — project/session 树 | `internal/tui/views/dashboard.go` |
| `2` | Favorites — 收藏 session 列表 | `internal/tui/views/favorites.go` |
| `3` | Stats — 用量统计 | `internal/tui/views/stats.go` |
| `Tab` / `Shift+Tab` | 切换视图 | `internal/tui/app.go` |

### Manage 视图按键

| 按键 | 作用 | 有效节点 |
|------|------|----------|
| `j`/`k` / `↓`/`↑` | 移动光标（移出可视区时视图自动滚动） | 全部 |
| `h`/`l` / `←`/`→` / `Enter` | 收起/展开 | 全部 |
| `Space` | 多选：session 切换选中，project 全选/反选其全部后代 session（空 project 为 no-op） | 全部 |
| `d` | 删除：有选中时批量删除全部选中 session（跨 project）；无选中时删除当前子树（project=批量删除；global 只删 session 不删 project） | 全部 |
| `e` | 导出 JSON 到 `/tmp/octl-exports` | 全部 |
| `m` | 查看对话历史（h/l 翻消息，j/k 滚动） | 仅 session |
| `n` | 新建 session（project）/ fork（session） | project/session |
| `s` | 发送消息到已有 session | 仅 session |
| `f` | 收藏切换：多选时切换全部选中 session，否则切换光标处 session（发 `FavoritesToggleRequestMsg`，对已收藏发 `unfavorite`、未收藏发 `favorite` action） | 仅 session |
| `r` | 保留按键，提示由 daemon 自动刷新 | 全部 |
| `Ctrl+x` / `Ctrl+C` | 退出 | 全局 |

多选状态以 session ID 为 key 记录，`rebuildTree` 后保持；被选中的行加 `✓ ` 前缀并以紫色（bg 62 + Bold）高亮，优先于光标样式。树视图行数超过可视区时显示垂直滚动条（`█` 表示可视窗口、`░` 表示轨道），光标移出可视区时自动滚动跟随。

### Favorites 视图按键

| 按键 | 作用 |
|------|------|
| `j`/`k` / `↓`/`↑` | 移动光标（滚动窗口跟随） |
| `Space` | 多选：切换光标处 session 选中状态 |
| `f` | 取消收藏：多选时移除全部选中 session，否则移除光标处 session（发 `FavoritesRemoveRequestMsg`，app 层发 `unfavorite` action 并经 daemon 确认） |
| `d` | 删除：先发 `delete` action，再发 `FavoritesRemoveRequestMsg`（`tea.Sequence`），确保收藏列表同步清理（daemon 在删除成功时也会自动剪枝收藏） |

收藏数据流（daemon 权威驱动）：daemon 的 `StateManager` 在内存中维护有序收藏集合（`favorites []string` + `favoritesSet map[string]bool`）。dashboard/favorites 视图通过 `FavoritesToggleRequestMsg` / `FavoritesRemoveRequestMsg` 请求 app 层：app 层先乐观更新共享的 `favoritesMap`（`map[string]bool`）并广播 `FavoritesChangedMsg` 给两个子视图，同时按 toggle 语义向 daemon 发送 `favorite`/`unfavorite` action；daemon 执行后重新 `buildView()` 推送，TUI 以 `ViewMsg.Favorites` 重建 `favoritesMap`（daemon 为权威源，陈旧本地项被丢弃），sidebar 以同一字段同步本地 `favorites` signal——两端收藏经 daemon 保持一致。收藏**持久化到 `~/.local/share/opencode/octl-state.json`**（`internal/daemon/state.go`）：变更即异步落盘 + 30s ticker 全量快照 + SIGTERM 退出 flush，daemon 重启后 `restoreState()` 恢复（孤儿收藏交给既有剪枝机制清理）。同一文件还持久化卡住态（PERMISSION/ERROR 的 session 含 permType/errorMsg）：恢复时 `source=event` 保持豁免接管语义，停机期间的解除事件由 hook 的 FIFO 缓冲补发自愈；pid/tmux 映射不恢复（新事件重建）。

### 创建 / Fork / 发送消息

在 Manage 视图中按 `n` 或 `s` 后，dashboard 会弹出输入框。按 Enter 后，TUI 将 `ManageActionMsg` 通过 socket 发送给 daemon，由 daemon 内部调用 `manage.Manager` 执行：

- project 节点按 `n`：`opencode run <msg> --dir <worktree>`
- session 节点按 `n`：`opencode run --session <id> --fork <msg> --dir <dir>`
- session 节点按 `s`：`opencode run -s <id> <msg> --dir <dir>`

## 安全与风险

| 特性 | 说明 |
|------|------|
| 数据库只读 | `?mode=ro`，驱动层拒绝 SQL 写入 |
| 删除走 CLI | 不直接写 DB；session 删除通过 `opencode session delete` 执行 |
| 确认对话框 | 删除/导出操作需 Enter 确认 |
| 无网络访问 | 完全离线，不发送任何外部数据 |
| 不碰原始文件 | project 删除只清理 DB 记录，不影响 git 仓库和项目文件 |
| session_diff 清理 | `DeleteSession` 成功后会尽力清理 `~/.local/share/opencode/storage/session_diff/<sessionID>` |

## 分支模型与发布

单仓双分支双 remote 模型：

- `master`：私有开发主线，全量历史，只推 `origin`
- `main`：公开快照分支，**orphan 历史**（与 master 无共同祖先），推 `github` 与 `origin`
- **两条历史永不 merge**——把 master merge 进 main 会把私有历史带上 GitHub，是本仓唯一致命禁忌
- 发布 = `./scripts/publish-github.sh --push`：按 `scripts/publish-exclude.txt` 排除清单，把 master 工作区文件同步到 main 的临时 worktree 做快照提交，然后推双 remote
- 发布内容 = **工作区当前内容**；先 commit 到 master 再发布，禁止把未提交的 WIP 发布成公开快照
- 新增私有文件（本机配置、部署文档等）必须同步登记进 `scripts/publish-exclude.txt`
- 防误推：`.git/hooks/pre-push`（源在 `scripts/hooks/pre-push`）对 github remote 只放行 `refs/heads/main` 与 `refs/tags/*`，其余 refspec（含 `--all`/`--mirror`/误推 master）一律 exit 1；钩子不随 clone 传播，新 clone 后按其头部注释重装
- 发版：`git tag -a v0.x.y main && git push github v0.x.y`——README 的 `go install ...@latest` 依赖 tag 存在；tag 推送后 `.github/workflows/release.yml` 自动交叉编译 linux/darwin × amd64/arm64 二进制（`-X main.version` 注入 tag 号）并创建 GitHub Release
- 新机器首次配置：见 `scripts/publish-github.sh` 头部注释（remote/fetch/branch/装钩子四步）

## 常见陷阱

- **不要修改 `docs/architecture.md` 添加 SQL**：架构文档只描述模块关系、数据流、状态机。
- **新增 SQL 时必须处理 NULL**：所有可空列都要 `COALESCE`。
- **不要尝试通过 `db.DB` 直接写入**：写操作使用 `NewWritable()` 并在 `manage` 中完成。
- **不要混淆 `time_*` 单位**：数据库中是 unix 毫秒，不是秒。
- **TUI 现在依赖 daemon**：TUI 启动后必须连接 daemon 才能浏览数据；离线时显示 `🔴 OFFLINE`。
- **hook 插件不 subscribe**：`octl-hook.js`（由 `octl plugins` 生成）只作为 event source 发送事件，不要改动它去 subscribe；sidebar 插件则需要 subscribe `view` 频道。
- **管理操作由 daemon 执行**：TUI 只发送 action 请求，实际调用 `opencode` CLI 在 daemon 内部完成。
- **CLI 动作子命令复用 action 协议**：`octl delete/create/fork/send` 与 TUI 走同一条 action 消息通道（`progress`/`result` 写回发起连接），daemon 侧没有专门的 CLI 路径；fork/send 的 `--dir` 缺省由 daemon 经 `resolveActionDirectory` 查 DB 补全，CLI 不要自己猜目录。
- **收藏由 daemon 统一维护（内存态 + octl-state.json 持久化）**：TUI 的 `favoritesMap` 和 sidebar 的 `favorites` signal 只是 daemon 推送 `ViewMsg.Favorites`/`isFavorite` 的渲染缓存 + 乐观更新层；增删收藏一律通过 `favorite`/`unfavorite` action 发送到 daemon，由 daemon 重新 `buildView()` 推送后调和。收藏不写 SQLite，持久化走 `~/.local/share/opencode/octl-state.json`（变更即写 + 周期快照 + 退出 flush，重启恢复）。不要把收藏直接写进 opencode 的数据库。
- **不要 merge master 与 main**：orphan 双分支模型，merge 即把私有历史泄上 GitHub（详见「分支模型与发布」）。
- **发布只走 `./scripts/publish-github.sh --push`，不要手工 push main**；github remote 永远只收 main 和 tags（pre-push 钩子会拦截其余）。

## 发布与部署

- CI 为 GitHub Actions（`.github/workflows/ci.yml`）：golangci-lint（固定 v2.13.2）、Go build+test 与 Bun 插件测试，push/PR 触发。
- 发版流水线（`.github/workflows/release.yml`）：tag（`v*`）推送触发，交叉编译四平台二进制并自动创建 GitHub Release（附件 tar.gz + 自动生成 release notes）。
- 构建产物为单个静态二进制文件 `octl`（CGO-free）。
- daemon 设计为前台运行，由外部 supervisor/systemd 管理；systemd unit 示例未包含在仓库中。
- sidebar 插件和 hook 插件由 `octl plugins --output=<dir>` 生成，需要随版本一起更新到 opencode 的 plugins 目录。

### 交付闭环（强制）

凡是改动了对用户可见的行为（CLI 子命令、flag、help、wire 协议、daemon 逻辑），交付前必须按顺序完成：

1. `go build ./... && go test ./...` 全绿；改了插件模板或 gen 逻辑时 `cd plugin && bun test` 也要全绿。
2. `octl --help` 输出与代码行为一致（主 help 与各子命令 help 都要查）。
3. 项目目录不保留 `octl` 构建产物（.gitignore 已含；发现残留即删）。

新子命令/方法交付时同步检查 help 回归测试（`main_test.go` / `query_test.go` 的 usage 测试）是否覆盖，缺了先补测试再交付。
