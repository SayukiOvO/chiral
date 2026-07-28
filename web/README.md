# web — 前端（React）

Panel 的两个界面，一套源码、一次构建。**React + Vite + TailwindCSS v4**。视觉方向「信号控制台」：clean minimalism（参考 Revolut），**传统黑灰夜间基调** + 唯一克制绿色（只给在线 / 运行 / 实时流量线，黑底绿线的监控观感）；chrome 全走黑白灰高对比。

- 日 / 夜 / 跟随系统 三态主题；移动端自适应；i18n 中 / 英（`src/lib/i18n.ts`，**键就是中文原文**，缺翻译降级成原文而不是裸 key）。
- 字体自托管（`@fontsource`，不依赖 Google CDN）：Space Grotesk（标题）+ Inter（正文）+ Space Mono（遥测数据）。
- 设计 token 在 `src/index.css`（`@theme inline` + CSS 变量运行时换肤）。
- 源码布局见 [`src/README.md`](src/README.md)。

## 两个入口

`vite.config.ts` 声明两个 input：`index.html` → `src/portal.tsx` 是订阅用户门户，挂在 `/`；`admin/index.html` → `src/main.tsx` 是管理台，挂在 `/admin/`。Core 按同样两个路径伺服构建产物（`core/internal/api/static.go`）。

拆开买到的是**包体积与 URL 归属，不是安全边界**——边界在服务端（`core/internal/api/portal.go`）。而包体积这件事成立的前提是：**`src/portal/` 不 import `src/api.ts`，也不 import `src/pages/`**。任一处引进来，管理台的整个客户端连同 Monaco 就进了客户下载的包；更糟的是门户会拿着自己的 token 去打管理端点。两边只共享 `src/lib/`、`src/format.ts` 与 `src/components/` 里的原语。这是纪律不是结构，静默失效，改动前先想一遍。

`appType: "mpa"`：两个界面都用 hash 路由，谁都不要 SPA fallback。代价是开发时 **`/admin` 不带尾斜杠会 404**。

## 开发

```bash
cd web && npm install
npm run dev   # 门户 http://localhost:5173/ ，管理台 http://localhost:5173/admin/
npm test      # 模板校验逻辑的单测
```

`/api` 与 `/sub` 代理到本地 `127.0.0.1:26080`，门户的调用都在 `/api/portal/` 下，一条规则覆盖两边。先跑起 core：管理台用管理员账号 + 密码登录（账号配了第二因素就再走一步 MFA），门户要 `CHIRAL_PORTAL_MODE` 非 `off`。

## Monaco 模板编辑器

两个约束写在 `lib/monaco.ts` 里，改动前先读：

1. **不能走 CDN**。`@monaco-editor/react` 默认运行时从 CDN 拉 Monaco，在受限网络下直接白屏；这里用 `loader.config({ monaco })` 交给 Vite 打包的副本，worker 也显式接好。
2. **懒加载**。Monaco 约 4 MB，节点仪表盘根本不开编辑器。`TemplateEditor` 用 `lazy()` 引入 `MonacoEditor`——**任何一处对 `./MonacoEditor` 的静态 import 都会把整个 Monaco 拖回主包**，静默让这件事白做。

语言是自定义的 `chiral-template` 而非 JSON：模板里 `{{port}}` 这种不带引号的数字占位在 JSON 模式下会被判成语法错。字符串内的 `{{变量}}` 用单独的 tokenizer 状态着色，否则字符串规则会整体吞掉它（`"{{sni}}:443"` 就是最常见的写法）。

`checkRefs()` 复刻 Go 引擎的规则（未定义变量报错、客户端模板禁用私钥），有单测守着——两边漂移会让编辑器骗人。
