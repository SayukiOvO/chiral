# web/src — 前端源码

两个入口共用的源码树：`portal.tsx`（订阅用户门户，`/`）与 `main.tsx`（管理台，`/admin/`）各自挂一棵 React 树，见 [`../vite.config.ts`](../vite.config.ts)。

- `App.tsx` — 管理台外壳：无会话给 `LoginPage`，有会话按 hash 路由渲染 `pages/`
- `portal/` — 门户界面：`PortalApp` 及其自己的 `api.ts` / `router.ts`
- `pages/` — 管理台页面：`NodesPage` `ProfilesPage` `UsersPage` `VariablesPage` `SecurityPage`
- `components/` — 两边共用的原语（`ui` `primitives` `icons` `QRCode` `StatusDot` `QuotaBar` …）与管理台专属组件（`Chart` `MonacoEditor` `NodeRoster` … 门户不引用它们）
- `lib/` — `cn` `theme`（三态）`i18n` `router` `http` `monaco` `template` `webauthn` `clipboard`
- `api.ts` — 管理台的类型化 REST 客户端；`format.ts` — 字节 / 速率 / 时间格式化；`index.css` — 设计 token

## 边界

`api.ts` 与 `portal/api.ts` 是两个客户端，只共享 `lib/http.ts`（它**不知道** token 从哪来，由调用方注入）。两边 token 存在不同的 localStorage 键下：同一浏览器里的管理员和客户不会互相顶掉会话，更要紧的是门户构建若链上管理台的客户端，就会把 token 发去管理端点。

因此 **`portal/` 不 import `api.ts`，也不 import `pages/`**。`components/primitives.tsx` 正是为此从 `pages/VariablesPage.tsx` 里拆出来的：UI 原语留在管理台页面文件里，任何想要一个 Modal 的包都会被拖上整个页面和它背后的 `api.ts`。
