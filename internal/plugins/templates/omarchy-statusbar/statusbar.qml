import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui
import "statusbar.js" as Statusbar

// octl.sessions — omarchy bar-widget：订阅 octl daemon 的 view 频道，栏上
// 实时显示非 IDLE session 计数（🔴ERROR/PERMISSION 🟠RETRY 🟡BUSY，配色
// 对齐 herdr：红=需要你、黄=运行中；固定顺序、仅非零、不带标题）；全
// IDLE 或 daemon 未连接时自隐藏。
// 点击弹出分组列表（标题截断、超 8 条折叠 +N more）+ 收藏列表（daemon
// ViewMsg.favorites 原序全量展示，空则整节隐藏），行点击经
// statusbar.js 生成的跳转脚本本地执行（hyprctl 聚焦 + tmux 切换/拉起），
// daemon 与 wire 协议零参与。
//
// 弹层为 Ui.PopupCard 默认的 click 触发（HyprlandFocusGrab 点外关闭，与
// Tray 同款生产路径）。hover 触发两条路线真机均闪烁循环，不可用：
// KeyboardPanel（全屏 layer-shell + 整屏 dismissArea，打开即截走 bar 的
// hover，弹层随 hover 丢失关闭、又随 hover 恢复打开）；PopupCard 的
// triggerMode:"hover" 亦复现同症状。
//
// 版本自愈：subscribe 携带 {{OCTL_MD5}} 协议版本；daemon 拒绝版本不符的
// 订阅时会条件重写本插件目录（alignPlugins，仅已安装过才写）。注意
// omarchy bar-widget 实例**不被任何热重载替换**（file-watch /
// rescanPlugins / 删除重装均不换实例，旧实例的 IPC handler 压住新实例；
// 真机验证）——重写落盘后需 `omarchy-restart-shell` 重启 shell，新实例才
// 以新 MD5 重新订阅（与 opencode TUI 插件同语义：重启是唯一加载途径）。
Panel {
  id: root
  moduleName: "octl.sessions"
  // 自定义 IpcHandler 而非 Panel 内建注册（manageIpc: false）：多一个
  // version 探针，供真机核对运行中的构建（omarchy-shell octl.sessions
  // version）——热重载是否真的替换了实例，一眼可辨。
  ipcTarget: "octl.sessions"
  manageIpc: false

  readonly property string protocolVersion: "{{OCTL_MD5}}"
  readonly property color foreground: barForeground
  readonly property color dim: Qt.darker(foreground, 1.55)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family
  readonly property int barSize: bar ? bar.barSize : Style.bar.sizeHorizontal

  // 连接与数据状态。healthy 在收到 subscribed 后置位——socket 建连只是
  // TCP 层，被版本拒绝的连接不算在线。
  property bool healthy: false
  // 协议版本被拒：停止重连（旧 MD5 重试只会再被拒）。daemon 已条件重写
  // 插件文件；bar-widget 实例不被热重载替换，`omarchy-restart-shell` 后
  // 由新实例以新 MD5 接管。
  property bool versionRejected: false
  property int retryDelayMs: 1000
  property var sessions: []
  property var favorites: []

  readonly property var counts: root.healthy ? Statusbar.groupCounts(root.sessions) : []
  readonly property var tooltipData: Statusbar.groupTooltip(root.sessions, 8)
  readonly property var favoriteData: root.healthy ? Statusbar.favoriteRows(root.favorites) : []

  visible: root.healthy && root.counts.length > 0
  onVisibleChanged: if (!visible) root.close()

  // 仅按横向 bar 设计（纵向 bar 不在目标机器配置内）。
  implicitWidth: countsRow.implicitWidth + Style.spacing.md
  implicitHeight: barSize

  function sendSubscribe() {
    sock.write(JSON.stringify({
      type: "subscribe",
      channels: ["view"],
      version: root.protocolVersion,
    }) + "\n")
    sock.flush()
  }

  function scheduleReconnect() {
    if (root.versionRejected) return
    retryTimer.restart()
  }

  function handleLine(line) {
    var msg
    try {
      msg = JSON.parse(line)
    } catch (e) {
      return
    }
    if (msg.type === "subscribed") {
      root.healthy = true
      root.retryDelayMs = 1000
    } else if (msg.type === "view") {
      root.sessions = Statusbar.collectSessions(msg)
      root.favorites = Statusbar.collectFavorites(msg)
    } else if (msg.type === "response" && msg.ok === false) {
      // daemon 拒绝（典型为协议版本不一致）：隐藏等待对齐自愈。
      root.healthy = false
      if (String(msg.error || "").indexOf("version mismatch") >= 0) {
        root.versionRejected = true
      }
    }
  }

  function runJump(session) {
    var script = Statusbar.buildJumpScript(session)
    if (!script || jumpProc.running) return
    jumpProc.command = ["sh", "-c", script]
    jumpProc.running = true
  }

  // ---------------------------------------------------------------- socket
  //
  // 断连指数退避重连 1s→2s→…→30s 封顶，连上即重置。

  Socket {
    id: sock
    path: Quickshell.env("HOME") + "/.local/share/octl/octl.sock"
    connected: !root.versionRejected
    parser: SplitParser {
      splitMarker: "\n"
      onRead: function(data) {
        root.handleLine(data)
      }
    }
    onConnectionStateChanged: {
      if (connected) {
        root.retryDelayMs = 1000
        root.sendSubscribe()
      } else {
        root.healthy = false
        root.scheduleReconnect()
      }
    }
  }

  Timer {
    id: retryTimer
    interval: Math.min(root.retryDelayMs, 30000)
    onTriggered: {
      if (root.versionRejected || sock.connected) return
      root.retryDelayMs = Math.min(root.retryDelayMs * 2, 30000)
      sock.connected = true
    }
  }

  Process {
    id: jumpProc
  }

  IpcHandler {
    target: root.ipcTarget

    function open(): void { root.open() }
    function close(): void { root.close() }
    function show(): void { root.open() }
    function hide(): void { root.close() }
    function toggle(): void { root.toggle() }
    // 构建探针：协议 MD5 + 触发模式标记，核对热重载是否替换了运行实例。
    function version(): string { return root.protocolVersion + " click-v4-fav" }
  }

  // ---------------------------------------------------------------- bar UI

  WidgetButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: ""
    labelVisible: false
    hasVisualContent: true

    // 点击开关弹层；HyprlandFocusGrab 负责点击外部收起（PopupCard 默认
    // click 模式），行点击负责跳转并收起。
    onPressed: function(buttonCode) {
      if (buttonCode === Qt.LeftButton) root.toggle()
      else root.close()
    }

    Row {
      id: countsRow
      anchors.centerIn: parent
      spacing: Style.spacing.sm

      Repeater {
        model: root.counts

        Row {
          id: countChip
          required property var modelData
          spacing: 2

          Text {
            text: countChip.modelData.icon
            font.pixelSize: Style.font.body
            anchors.verticalCenter: parent.verticalCenter
          }

          Text {
            text: String(countChip.modelData.n)
            color: root.foreground
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            anchors.verticalCenter: parent.verticalCenter
          }
        }
      }
    }

  }

  // ---------------------------------------------------------------- popup

  PopupCard {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    contentWidth: panel.fittedContentWidth(Style.space(380))
    contentHeight: panel.fittedContentHeight(column.implicitHeight, Style.space(480))

    Column {
      id: column
      anchors.fill: parent
      anchors.margins: Style.spacing.md
      spacing: Style.spacing.xs

      Repeater {
        model: root.tooltipData.groups

        Column {
          id: groupBlock
          required property var modelData
          width: parent.width
          spacing: 2

          // 组头：状态图标 + 状态词 + 计数
          Text {
            text: groupBlock.modelData.icon + "  " + groupBlock.modelData.key + " · " + groupBlock.modelData.items.length
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
          }

          Repeater {
            model: groupBlock.modelData.items

            Rectangle {
              id: sessionRow
              required property var modelData
              width: parent.width
              height: sessionRowText.implicitHeight + Style.space(6)
              radius: Style.space(4)
              color: rowMouse.containsMouse ? Style.selectedFillFor(root.foreground, Color.accent) : "transparent"

              MouseArea {
                id: rowMouse
                anchors.fill: parent
                hoverEnabled: true
                cursorShape: Qt.PointingHandCursor
                onClicked: {
                  root.runJump(sessionRow.modelData.session)
                  root.close()
                }
              }

              Text {
                id: sessionRowText
                anchors.left: parent.left
                anchors.leftMargin: Style.spacing.sm
                anchors.verticalCenter: parent.verticalCenter
                text: sessionRow.modelData.title
                color: rowMouse.containsMouse ? root.foreground : root.dim
                font.family: root.fontFamily
                font.pixelSize: Style.font.body
                elide: Text.ElideRight
                width: parent.width - Style.spacing.sm * 2
              }
            }
          }
        }
      }

      // 折叠余量：+N more
      Text {
        visible: root.tooltipData.hidden > 0
        text: "+" + root.tooltipData.hidden + " more"
        color: root.dim
        font.family: root.fontFamily
        font.pixelSize: Style.font.caption
      }

      // 收藏列表：daemon ViewMsg.favorites 原序全量展示（不折叠），
      // 空收藏整节隐藏。行点击与状态行同款跳转。标题黄色 #e0af68
      // 与 sidebar 收藏高亮同色；行不显示状态图标。
      Text {
        visible: root.favoriteData.length > 0
        text: "★ Favorites · " + root.favoriteData.length
        color: "#e0af68"
        font.family: root.fontFamily
        font.pixelSize: Style.font.caption
      }

      Repeater {
        model: root.favoriteData

        Rectangle {
          id: favoriteRow
          required property var modelData
          width: parent.width
          height: favoriteRowText.implicitHeight + Style.space(6)
          radius: Style.space(4)
          color: favMouse.containsMouse ? Style.selectedFillFor(root.foreground, Color.accent) : "transparent"

          MouseArea {
            id: favMouse
            anchors.fill: parent
            hoverEnabled: true
            cursorShape: Qt.PointingHandCursor
            onClicked: {
              root.runJump(favoriteRow.modelData.session)
              root.close()
            }
          }

          Row {
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.leftMargin: Style.spacing.sm
            anchors.rightMargin: Style.spacing.sm
            anchors.verticalCenter: parent.verticalCenter

            Text {
              id: favoriteRowText
              text: favoriteRow.modelData.title
              color: favMouse.containsMouse ? root.foreground : root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.body
              elide: Text.ElideRight
              width: parent.width
            }
          }
        }
      }
    }
  }
}
