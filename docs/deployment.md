# 部署

全程 Docker / docker-compose。分两侧：Panel（中心）与节点。

## Panel 侧

单个 `docker-compose.yml`（见 [`../deploy/panel/`](../deploy/panel/)），包含：
- `core` 容器：Go 后端，挂载 SQLite 数据卷。
- 前端静态资源：由 core 一并伺服，或独立静态容器。
- 可选 `caddy` / `nginx`：TLS 终止（建议 ACME 自动签发证书）。

关键环境变量（草案）：`CHIRAL_DB_PATH`、`CHIRAL_LISTEN`、`CHIRAL_PUBLIC_URL`、TLS 相关。

## 节点侧

不手写 compose——在 Panel「新增节点」时，Core **自动生成**一段 `docker-compose.yml`（模板见 [`../deploy/agent/`](../deploy/agent/)）+ 一次性 join token，用户复制到节点机器执行：

```bash
docker compose up -d
```

Agent 容器启动后：用 `JOIN_TOKEN` 向 `PANEL_URL` 注册 → 换取长期凭证 → 建立到 Core 的长连接 → 拉起并管理本机 Xray-core。

生成的 compose 通过环境变量注入：`PANEL_URL`、`JOIN_TOKEN`（一次性）。Agent 与 Xray-core 可同容器或同 pod；config.json 走数据卷。

## 加入流程时序

```
Panel: 新增节点 → 签发 token + 生成 compose
用户:  复制 compose 到节点 → docker compose up -d
Agent: 用 token 注册 → 得长期凭证（token 作废）→ 建长连接
Core:  推送首份 config → Agent 落盘 + xray -test + 拉起 Xray-core → ack
```

## 待办

- [x] 起草 `deploy/panel/docker-compose.yml`
- [x] 起草 `deploy/agent/docker-compose.yml.tmpl`（Core 渲染用）
- [x] Dockerfile（core / agent 草案；web 待前端就绪）
- [ ] Xray-core 二进制：草案按随镜像打包（pin 版本），是否支持运行时拉取 / 在线升级待议
