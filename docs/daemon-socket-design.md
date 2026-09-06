# octl daemon/TUI 分离重构 — 统一 Unix socket 设计方案

> **Status**: Implemented — this design matches the current implementation.
> 基于用户最终确认：TUI 和插件都通过 Unix socket 长连接与 daemon 通信；daemon 常驻前台运行；TUI 在界面右上角显示 online/offline 状态。

---

## 1. 为什么统一用 Unix socket

Unix domain socket 与 TCP socket 在通信语义上完全一致：

- **流式（SOCK_STREAM）**、全双工、连接导向
- 支持长连接，对端关闭/崩溃可感知
- 本机-only，无需端口，权限由文件系统控制
- 一个 daemon 只需要维护**一个 listener**

原插件 `plugin/octl-hook/octl-hook.js` 已使用 `Bun.connect({ unix: socketPath })`；TUI 同样可以用 `net.Dial("unix", socketPath)` 连接，无需引入 TCP。

---

## 2. 总体架构

```
┌─────────────────────────────────────────────────────────────┐
│                     octl daemon 进程                         │
│                  (octl --daemon 前台运行)                     │
│                                                             │
│  ┌──────────────────────────────────────────────────────┐   │
│  │              Unix socket listener                     │   │
│  │     ~/.local/share/opencode/octl.sock                │   │
│  └─────────────────────────┬────────────────────────────┘   │
│                            │                                │
│                            ▼                                │
│  ┌──────────────────────────────────────────────────────┐   │
│  │                  StateManager                         │   │
│  │  ┌─────────┐  ┌──────────────┐  ┌─────────────────┐   │   │
│  │  │ eventCh │  │ connRegistry │  │   stateMap      │   │   │
│  │  │ buf=100 │  │ {conn, role} │  │ map[string]*    │   │   │
│  │  └────┬────┘  └──────┬───────┘  │   SessionState  │   │   │
│  │       │              │          └─────────────────┘   │   │
│  │       ▼              ▼                                 │   │
│  │  processEvent()  broadcastSnapshot()                   │   │
│  │       │              │                                 │   │
│  │       └──────────────┘                                 │   │
│  │              ▲                                         │   │
│  │         syncFromDB() (30s)                             │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                            │
           ┌────────────────┼────────────────┐
           │ Unix socket    │ Unix socket    │
           ▼                ▼                ▼
    ┌─────────────┐  ┌─────────────┐  ┌─────────────┐
    │ octl-hook.js │  │   octl TUI  │  │   octl TUI  │
    │  事件源       │  │  订阅客户端  │  │  订阅客户端  │
    │  不 subscribe│  │  subscribe  │  │  subscribe  │
    └─────────────┘  └─────────────┘  └─────────────┘
```

---

## 3. 连接角色区分

同一个 Unix socket 上会有两种客户端：

| 角色 | 行为 | 示例 |
|------|------|------|
| **事件源（event source）** | 连接后直接发送事件 JSON，不发送 `subscribe` | `plugin/octl-hook/octl-hook.js` |
| **订阅者（subscriber）** | 连接后先发送 `subscribe`，然后接收 snapshot | `octl` TUI |

daemon 的处理逻辑：

1. 新连接上来，先尝试读取第一行 JSON。
2. 如果第一行是 `{"type":"subscribe"}` → 标记为 subscriber，加入订阅列表。
3. 否则直接作为 event source，该行 JSON 进入 `eventCh`，后续每行也进 `eventCh`。

> 事件源也可以先发事件；如果它永远不 subscribe，就永远不会收到 snapshot。这样旧插件无需任何改动。

---

## 4. Wire Protocol（JSON Lines）

### 4.1 事件源 → daemon

与当前插件格式完全一致：

```json
{"type":"session.status","properties":{"sessionID":"ses_xxx","status":{"type":"busy"}}}
{"type":"session.idle","properties":{"sessionID":"ses_xxx"}}
{"type":"session.created","properties":{"sessionID":"ses_xxx","title":"...","projectID":"..."}}
```

### 4.2 subscriber → daemon

```json
{"type":"subscribe","channels":["snapshot"]}
```

可选心跳：

```json
{"type":"ping"}
```

一次性请求（`octl query` 子命令走此通道，daemon 侧零推送）：

```json
{"type":"request","method":"snapshot|listSessions|messages","id":"cli-xxx","sessionId":"..."}
{"type":"request","method":"daily","id":"cli-xxx","from":1788192000000,"to":1788451200000}
```

`daily` 携带 `[from, to)` 时间窗口（unix 毫秒），要求 `0 < from < to`；
应答的 `daily` 字段为窗口内聚合事实（按 project 分组的新增/活跃/归档
session、摘录素材、僵尸与卡住列表），摘录按名额规则截断，详见
`internal/daemon/types.go` 的 `DailyDigest`。未知 method 返回
`ok:false` 的错误 response（不静默超时）。

### 4.3 daemon → subscriber

订阅确认：

```json
{"type":"subscribed","channels":["snapshot"]}
```

状态快照（stateMap 变化时主动推送）：

```json
{"type":"snapshot","states":[{"sessionId":"ses_xxx","status":"BUSY","title":"...","projectId":"..."}]}
```

心跳响应：

```json
{"type":"pong"}
```

---

## 5. 启动方式

### 5.1 daemon 模式

```bash
octl --daemon
```

- 前台运行。
- 监听 Unix socket：`~/.local/share/opencode/octl.sock`。
- 启动流程：
  1. 打开 DB（只读）。
  2. 初始 `syncFromDB()`。
  3. 删除残留 socket，启动 listener。
  4. 启动 30s DB 同步 ticker。
  5. 进入事件循环。

### 5.2 TUI 模式

```bash
octl
# 或
octl --tui
```

- 默认行为：启动 TUI 并尝试连接 socket。
- 连接不上：TUI 启动但右上角显示 `🔴 OFFLINE`，状态树不刷新；后台每 5 秒重试。
- 连上后：右上角显示 `🟢 ONLINE`，发送 `subscribe`，开始接收 snapshot。

### 5.3 命令行参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--daemon` | false | 启动 daemon 服务 |
| `--tui` | false | 显式启动 TUI |
| `--socket` | `~/.local/share/opencode/octl.sock` | Unix socket 路径 |
| `--refresh-time` | `5` | Manage 视图自动刷新间隔 |

---

## 6. TUI 连接状态显示

### 6.1 右上角 online/offline

在 `app.go` 的 `View()` 中，header 改为三栏布局：

```
[左：空]  [中：octl — opencode Session Manager]  [右：🟢 ONLINE]
```

实现方式：

```go
var headerStyle = lipgloss.NewStyle().Bold(true)
var onlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))  // 绿色
var offlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // 红色

func (m Model) renderHeader() string {
    title := "octl — opencode Session Manager"
    status := "🟢 ONLINE"
    style := onlineStyle
    if !m.daemonOnline {
        status = "🔴 OFFLINE"
        style = offlineStyle
    }

    left := ""
    center := headerStyle.Render(title)
    right := style.Render(status)

    // 三等分宽度
    w := m.width / 3
    left = lipgloss.NewStyle().Width(w).Align(lipgloss.Left).Render(left)
    center = lipgloss.NewStyle().Width(w).Align(lipgloss.Center).Render(center)
    right = lipgloss.NewStyle().Width(w).Align(lipgloss.Right).Render(right)

    return lipgloss.JoinHorizontal(lipgloss.Top, left, center, right)
}
```

### 6.2 断线重连时的 UI 行为

- daemon 崩溃/重启 → socket 连接断开。
- TUI **不退出**。
- header 右上角切换为 `🔴 OFFLINE`。
- 状态树保留最后已知状态，但不再更新。
- 后台 goroutine 每 5 秒尝试重连。
- 重连成功后发送 `subscribe`，右上角恢复 `🟢 ONLINE`，并请求一次完整 snapshot 同步。

---

## 7. daemon 端实现要点

### 7.1 连接注册表

```go
type connRole int

const (
    roleEventSource connRole = iota
    roleSubscriber
)

type clientConn struct {
    conn   net.Conn
    role   connRole
    enc    *json.Encoder
    mu     sync.Mutex
}

type StateManager struct {
    db              *db.DB
    mu              sync.RWMutex
    stateMap        map[string]*SessionState
    eventCh         chan []byte
    clients         []*clientConn
    dbSyncInterval  time.Duration
    eventStaleAfter time.Duration
    ln              net.Listener
    socketPath      string
}
```

### 7.2 连接处理

```go
func (sm *StateManager) handleConn(conn net.Conn) {
    defer conn.Close()
    sc := bufio.NewScanner(conn)
    sc.Split(bufio.ScanLines)

    cl := &clientConn{conn: conn, enc: json.NewEncoder(conn)}

    // 读第一行判断角色
    if sc.Scan() {
        line := make([]byte, len(sc.Bytes()))
        copy(line, sc.Bytes())

        var msg baseMsg
        if err := json.Unmarshal(line, &msg); err == nil && msg.Type == "subscribe" {
            cl.role = roleSubscriber
            sm.addClient(cl)
            cl.write(subscribedMsg{Type: "subscribed", Channels: []string{"snapshot"}})
            // 立即推送一次当前 snapshot
            sm.pushSnapshotTo(cl)
        } else {
            cl.role = roleEventSource
            sm.eventCh <- line
        }
    }

    // 后续按角色处理
    for sc.Scan() {
        line := make([]byte, len(sc.Bytes()))
        copy(line, sc.Bytes())

        if cl.role == roleSubscriber {
            sm.handleSubscriberMsg(cl, line)
        } else {
            sm.eventCh <- line
        }
    }

    sm.removeClient(cl)
}
```

### 7.3 snapshot 广播

```go
func (sm *StateManager) broadcastSnapshot(states []SessionState) {
    msg := snapshotMsg{Type: "snapshot", States: states}
    for _, cl := range sm.clients {
        if cl.role != roleSubscriber {
            continue
        }
        cl.mu.Lock()
        err := cl.enc.Encode(msg)
        cl.mu.Unlock()
        if err != nil {
            sm.removeClient(cl)
        }
    }
}
```

---

## 8. TUI 端实现要点

### 8.1 替换 channel 为 socket client

当前 `app.go` 的 `snapshotCh <-chan []SessionState` 改为 `daemonOnline bool` + 持有连接。

新增 `internal/daemon/client.go`：

```go
type SocketClient struct {
    socketPath string
    conn       net.Conn
    enc        *json.Encoder
    dec        *json.Decoder
    mu         sync.Mutex
}

func (c *SocketClient) Connect() error { ... }
func (c *SocketClient) Subscribe() error { ... }
func (c *SocketClient) Close() error { ... }
func (c *SocketClient) ReadLoop(msgs chan<- interface{}) { ... }
```

### 8.2 app.go 消息

新增消息类型：

```go
type daemonOnlineMsg struct{}
type daemonOfflineMsg struct{}
type daemonSnapshotMsg struct {
    states []daemon.SessionState
}
```

`Init()`：

```go
func (m Model) Init() tea.Cmd {
    return tea.Batch(
        m.loadStats,
        m.dashboard.Init(),
        m.stats.Init(),
        m.connectDaemon(),
    )
}
```

`connectDaemon()`：

```go
func (m Model) connectDaemon() tea.Cmd {
    return func() tea.Msg {
        client := daemon.NewSocketClient(m.socketPath)
        if err := client.Connect(); err != nil {
            return daemonOfflineMsg{}
        }
        if err := client.Subscribe(); err != nil {
            return daemonOfflineMsg{}
        }
        m.daemonClient = client
        go m.daemonClient.ReadLoop(m.daemonMsgCh)
        return daemonOnlineMsg{}
    }
}
```

重连：收到 `daemonOfflineMsg` 后启动 `time.After(5s)` 再次调用 `connectDaemon()`。

---

## 9. 文件改动清单

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `main.go` | 大改 | `--daemon/--tui/--socket` 分支；TUI 默认连接 socket |
| `internal/daemon/daemon.go` | 大改 | 统一 Unix socket listener、client registry、角色区分、snapshot 广播 |
| `internal/daemon/types.go` | 中改 | 新增 wire message 类型 |
| `internal/daemon/client.go`（新） | 新增 | TUI 侧 socket 客户端 |
| `internal/tui/app.go` | 中改 | 替换 snapshotCh 为 socket client；处理 online/offline 消息；header 显示状态 |
| `internal/tui/statusbar.go` | 不改 | 底部状态栏保持原样 |
| `plugin/octl-hook/octl-hook.js` | 不改 | 继续通过 Unix socket 发送事件 |
| `docs/architecture.md` | 更新 | 架构图改为统一 socket |
| `docs/notification-design.md` | 更新 | 更新传输层描述 |
| `README.md` | 更新 | 启动方式说明 |
| `AGENTS.md` | 更新 | 如有必要 |

---

## 10. 边界场景

| 场景 | 行为 |
|------|------|
| daemon 未启动时启动 TUI | TUI 正常启动，header 显示 `🔴 OFFLINE`，每 5s 重连 |
| daemon 运行中崩溃 | TUI 不退出，header 切换 `🔴 OFFLINE`，保留最后状态，重连后恢复 |
| daemon 重启 | TUI 自动重连并重新订阅，恢复 `🟢 ONLINE` |
| 多个 TUI 同时连接 | 都作为 subscriber 收到 snapshot 广播 |
| 插件在 daemon 离线时发送事件 | 插件 500 条 FIFO 缓冲，daemon 恢复后重放 |
| socket 文件残留 | daemon 启动时删除旧 socket |
| socket 被其他进程占用 | daemon 启动失败，报错退出 |

---

## 11. 实施顺序

1. **Phase 1：改造 daemon 支持统一 socket 与角色区分**
   - 移除 `snapshotCh`，新增 `clients` 注册表。
   - 新连接先读第一行判断是 subscriber 还是 event source。
   - subscriber 接收 snapshot 广播。
   - 验证 `octl --daemon` 运行，旧插件事件仍能写入。

2. **Phase 2：TUI socket client 与 online/offline 显示**
   - 新增 `internal/daemon/client.go`。
   - 改造 `main.go` 命令行分支。
   - 改造 `internal/tui/app.go`：连接、订阅、断线重连、header 状态显示。

3. **Phase 3：文档与测试**
   - 更新 architecture/notification-design/README。
   - 增加 daemon + TUI 集成测试。

---

## 12. 最终确认

- socket 路径仍用 `~/.local/share/opencode/octl.sock`，是否接受？
- TUI 断线重连间隔 5 秒是否可接受？
- online/offline 用 `🟢 ONLINE` / `🔴 OFFLINE` 文案和颜色是否可接受？
