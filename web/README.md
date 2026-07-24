# web — 前端（React）

Panel 管理界面。**React + Vite + TailwindCSS v4 + shadcn/ui（Radix 底座）**。

- 日 / 夜 / 跟随系统 三态主题；移动端自适应；i18n（中 / 英）。
- 视觉参考新版 AWS / GitLab 的克制企业风。
- 模板编辑用 Monaco（`{{变量}}` 高亮 + `xray -test` 校验提示）。

## 开发

```bash
cd web && npm install
npm run dev   # http://localhost:5173，/api 代理到本地 core:8080
```

需要先跑起 core（`CHIRAL_ADMIN_TOKEN=... ./bin/chiral-core`），用同一个 admin token 登录。

## 现状（M1）

- [x] Vite + React + TS 工程
- [x] Tailwind v4 设计 token（颜色 / 暗色，见 `src/index.css`）
- [x] token 登录门 + API 客户端（`src/api.ts`）
- [x] 节点列表页：在线状态、Xray 状态、实时指标（3s 轮询）、新增节点（join token + compose）、重启 / 删除
- [ ] shadcn/ui 组件化下沉、i18n、Monaco 模板编辑器（M2 起）

**状态**：M1 最简节点列表页已完成。M4 打磨。
