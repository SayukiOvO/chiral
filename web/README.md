# web — 前端（React）

Panel 管理界面。**React + Vite + TailwindCSS v4**。视觉方向「信号控制台」：clean minimalism（参考 Revolut），**传统黑灰夜间基调** + 唯一克制绿色（只给在线 / 运行 / 实时流量线，黑底绿线的监控观感）；chrome 全走黑白灰高对比。

- 日 / 夜 / 跟随系统 三态主题；移动端自适应；i18n（中 / 英，待接入）。
- 字体自托管（`@fontsource`，不依赖 Google CDN）：Space Grotesk（标题）+ Inter（正文）+ Space Mono（遥测数据）。
- 设计 token 在 `src/index.css`（`@theme inline` + CSS 变量运行时换肤）。

## 开发

```bash
cd web && npm install
npm run dev   # http://localhost:5173，/api 代理到本地 core:8080
npm test      # 模板校验逻辑的单测
```

需要先跑起 core（`CHIRAL_ADMIN_TOKEN=... ./bin/chiral-core`），用同一个 admin token 登录。

## 结构

- `api.ts` — 类型化 REST 客户端
- `lib/` — `cn`（类名）、`theme`（三态主题 + `useIsDark`）、`router`（hash 路由）、`monaco`（编辑器与语言注册）
- `pages/` — `NodesPage` `ProfilesPage`（列表 + 编辑器）`VariablesPage`
- `components/` — `TopBar`（含导航）`LiveRail` `NodeRoster` `Sparkline` `KernelState` `StatusDot` `NodeConfigDialog`（骨架 + 装配预览 + 下发）`TemplateEditor` / `MonacoEditor` `AddNodeDialog` `TokenGate` `ThemeToggle` `Mark`（◐）`ui` `icons`

## Monaco 模板编辑器

两个约束写在 `lib/monaco.ts` 里，改动前先读：

1. **不能走 CDN**。`@monaco-editor/react` 默认运行时从 CDN 拉 Monaco，在受限网络下直接白屏；这里用 `loader.config({ monaco })` 交给 Vite 打包的副本，worker 也显式接好。
2. **懒加载**。Monaco 约 4 MB，节点仪表盘根本不开编辑器。`TemplateEditor` 用 `lazy()` 引入 `MonacoEditor`——**任何一处对 `./MonacoEditor` 的静态 import 都会把整个 Monaco 拖回主包**，静默让这件事白做（主包会从 190 KB 涨到 4.2 MB）。

语言是自定义的 `chiral-template` 而非 JSON：模板里 `{{port}}` 这种不带引号的数字占位在 JSON 模式下会被判成语法错。字符串内的 `{{变量}}` 用单独的 tokenizer 状态着色，否则字符串规则会整体吞掉它（`"{{sni}}:443"` 就是最常见的写法）。

`checkRefs()` 复刻 Go 引擎的规则（未定义变量报错、客户端模板禁用私钥），有单测守着——两边漂移会让编辑器骗人。

## 现状

- [x] M1：节点列表（实时状态 / 指标 / 折线）、新增节点、重启、删除
- [x] M2：导航；变量管理（生成器 / 静态、私钥遮蔽）；接入配置编辑（inbound 骨架 / client-entry / 各客户端模板、绑定节点、一键下发）；节点 config 骨架 + 装配预览 + `xray -test` 状态 + 下发
- [ ] i18n、shadcn/ui 组件化下沉、订阅相关界面（M3）

**状态**：M2 前端完成。
