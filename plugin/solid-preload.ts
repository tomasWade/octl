// bun test 预加载：把 solid-js 的 SSR 构建（node 条件默认）重写为响应式构建。
// 必须在 preload 阶段执行（import 提升会让测试文件里的注册晚于 solid-js 加载）。
import solidPlugin from "@opentui/solid/bun-plugin";
Bun.plugin(solidPlugin as any);
