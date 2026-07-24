# web — 前端（React）

Panel 管理界面。**React + Vite + TailwindCSS v4**。视觉方向「信号控制台」：clean minimalism（参考 Revolut），**传统黑灰夜间基调** + 唯一克制绿色（只给在线 / 运行 / 实时流量线，黑底绿线的监控观感）；chrome 全走黑白灰高对比。

- 日 / 夜 / 跟随系统 三态主题；移动端自适应；i18n（中 / 英，待接入）。
- 字体自托管（`@fontsource`，不依赖 Google CDN）：Space Grotesk（标题）+ Inter（正文）+ Space Mono（遥测数据）。
- 设计 token 在 `src/index.css`（`@theme inline` + CSS 变量运行时换肤）。

## 开发

```bash
cd web && npm install
npm run dev   # http://localhost:5173，/api 代理到本地 core:8080
```

需要先跑起 core（`CHIRAL_ADMIN_TOKEN=... ./bin/chiral-core`），用同一个 admin token 登录。

## 结构

- `api.ts` — 类型化 REST 客户端
- `lib/` — `cn`（类名）、`theme`（三态主题 hook）
- `components/` — `TopBar` `LiveRail`（舰队仪表条）`NodeRoster`（节点卡片）`Sparkline`（实时流量活线，客户端环形缓冲）`KernelState` `StatusDot` `AddNodeDialog` `TokenGate` `ThemeToggle` `Mark`（◐ 手性标记）`ui`（Button/IconButton）`icons`

## 现状（M1）

- [x] 工程 + 设计系统 + 三态主题 + 响应式
- [x] token 登录门、节点卡片列表（在线状态、内核状态、实时指标 3s 轮询、每节点实时流量折线）、新增节点（join token + compose）、重启 / 删除（内联确认）
- [ ] shadcn/ui 组件化下沉、i18n、Monaco 模板编辑器（M2 起）

**状态**：M1 节点控制台完成（视觉重做）。
