# @opentui 事件系统研究成果（Keyboard & Mouse Events）

> 本文记录对 `@opentui/solid`（opencode sidebar 插件所用框架）键盘与鼠标事件机制的研究结论，供后续开发直接参考，无需重复下载源码研究。

## 来源

- 仓库：GitHub anomalyco/opentui（`@opentui/solid` / `@opentui/core` / `@opentui/keymap`）
- 关键文件：
  - `packages/core/src/Renderable.ts` — RenderableOptions 事件定义（L97-127）
  - `packages/solid/src/types/elements.ts` — 组件 Props 类型（ElementProps 等）
  - `packages/core/src/lib/KeyHandler.ts` — KeyEvent / KeyHandler
  - `packages/solid/examples/components/keymap-demo.tsx` — 键盘映射 demo

## 1. box/text 支持的事件清单

`RenderableOptions`（`box`/`text` 等所有 renderable 的基础 Options）定义的事件 props：

### 鼠标事件（10 个，参数 MouseEvent，含 button/x/y）

| 事件 | 说明 |
|------|------|
| `onMouse` | 通用鼠标事件 |
| `onMouseDown` | 按下 |
| `onMouseUp` | 释放 |
| `onMouseMove` | 移动 |
| `onMouseDrag` | 拖拽 |
| `onMouseDragEnd` | 拖拽结束 |
| `onMouseDrop` | 释放落下 |
| `onMouseOver` | 悬停进入 |
| `onMouseOut` | 悬停离开 |
| `onMouseScroll` | 滚轮 |

### 键盘事件

- `onKeyDown?: (key: KeyEvent) => void`

### 其他事件支持

- **任意命名事件 `on:xxx`**（ElementProps，Solid 原生 `on:` 指令，类型不限白名单）
- **专用组件事件**：
  - `Input`：`onInput` / `onChange` / `onSubmit`
  - `Textarea`：`onKeyDown` / `onKeyPress` / `onSubmit` / `onContentChange` / `onCursorChange`
  - `Select` / `TabSelect`：`onChange` / `onSelect`

## 2. KeyEvent 形状（lib/KeyHandler.ts）

```ts
export type KeyEventType = "press" | "repeat" | "release"

// KeyEvent（implements ParsedKey）—— 键盘事件对象
export interface ParsedKey {
  name: string          // 键名，如 "j"、"down"、"enter"
  ctrl: boolean         // Ctrl 修饰键
  meta: boolean         // Meta 修饰键
  shift: boolean        // Shift 修饰键
  option: boolean       // Alt/Option 修饰键
  sequence: string      // 原始按键序列
  number: boolean       // 是否为数字键
  raw: string           // 原始输入字节
  eventType: KeyEventType // 事件类型：press / repeat / release
  source: "raw" | "kitty" // 协议来源
  code?: string         // 按键码
  super?: boolean       // Super/Windows 键
  hyper?: boolean       // Hyper 修饰键
  capsLock?: boolean    // Caps Lock 状态
  numLock?: boolean     // Num Lock 状态
  baseCode?: number     // Kitty base-layout codepoint（例 99 = "c"），布局/IME 下恢复 Ctrl+C 匹配用
  repeated?: boolean    // 是否重复
}
```

完整接口定义见 `@opentui/core` 源码 `packages/core/src/lib/parse.keypress.ts` 的 `ParsedKey`（本表为常用字段全量）。

`KeyHandler`：EventEmitter，事件 map 为 `keypress: [KeyEvent]` / `keyrelease: [KeyEvent]`。另有 `PasteEvent`。

## 3. 键盘事件的两层机制

- **机制 A — 组件级 `onKeyDown`**：直接在 `box`/`text` 上监听，需要组件获得焦点（`focused` prop）。
- **机制 B — 应用级 `@opentui/keymap`（推荐）**：`KeymapProvider` 包裹 + `useBindings` 声明按键→命令绑定 + `useKeymapSelector` 切换活跃 keymap；支持 `<leader>` 前缀（如 `ctrl+x`）、`count` 等高级特性（参考 `keymap-demo.tsx`：`createDefaultOpenTuiKeymap`、`@opentui/keymap/addons/opentui`、`formatKeySequence`）。

## 4. 对 octl sidebar 的启示

- 当前 `octl-sidebar.tsx` 只使用 `onMouseDown`（`box`+`text` 双绑定做折叠切换）。
- 可扩展键盘导航（j/k 上下移动、h/l 折叠/展开、Enter 选中），建议采用 `@opentui/keymap` 的 `KeymapProvider`/`useBindings` 模式（比组件级 `onKeyDown` 更完整）。
- 注意：sidebar 运行在 opencode 的 `sidebar_content` slot，焦点与按键事件路由由 opencode 主机控制——键盘事件能否到达 sidebar 组件需在实际 opencode 环境验证。

## 5. 更新记录

- 2026-08-11：创建，基于 anomalyco/opentui 仓库源码研究
