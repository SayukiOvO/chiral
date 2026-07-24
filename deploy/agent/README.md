# deploy/agent — 节点部署模板

Core 在「新增节点」时渲染的 compose 模板，注入 `PANEL_URL` / `JOIN_TOKEN`（一次性）。

## 待办

- [x] `docker-compose.yml.tmpl`（草案；与 `core/internal/api` 的 composeSnippet 保持同步）
- [x] Agent Dockerfile（草案）
- [ ] Xray-core 二进制：草案按**随镜像打包**（Dockerfile 里 pin 版本下载，可复现、离线可用）；如需运行时拉取 / 在线升级内核再议。

**状态**：草案，随 M1 联调修订。
