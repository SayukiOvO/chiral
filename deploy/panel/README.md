# deploy/panel — Panel 部署

`core` + 前端 + 可选 `caddy`/`nginx`(TLS) 的 docker-compose。

镜像里**同时打包 Xray 二进制**：面板在存储 / 下发前要用 `xray -test` 校验渲染出的 config，并用它派生 Go 标准库没有的 ML-DSA-65 密钥。与 Agent 镜像同走快照通道。

## 关键环境变量

| 变量 | 必需 | 说明 |
|---|---|---|
| `CHIRAL_ADMIN_TOKEN` | 是 | 管理 API 的 bearer token |
| `CHIRAL_SECRET_KEY` | 强烈建议 | 私钥类变量的静态加密密钥；**不设则明文入库**。`openssl rand -base64 32` |
| `CHIRAL_GRPC_PUBLIC_ADDR` | 是 | Agent 拨回的地址，写进生成的 compose |
| `CHIRAL_XRAY_BIN` | 镜像内已设 | 面板侧 Xray 二进制 |
| `CHIRAL_TLS_CERT` / `_KEY` | 生产必需 | gRPC 端 TLS |
| `CHIRAL_TRUSTED_PROXY` | 有反代时必需 | 逗号分隔的地址或 CIDR，只写 Core 前面那层反代。**不设**则限流按对端地址计；**设错**则任何调用方一个请求头就能自选桶，限流形同虚设 |
| `CHIRAL_SMTP_*` | 可选 | 邮箱验证与登录验证码；`HOST` 留空即不提供邮件因素 |
| `CHIRAL_ONLINE_RECORD` | 默认 off | 记录每个用户的来源地址。打开会给每个节点注入 `statsUserOnline`，即一次配置版本变更 + 一次 Xray 重启（该节点上的活连接会断一次） |
| `CHIRAL_PORTAL_MODE` | 默认 off | 用户门户：`off` / `closed`（仅登录，账号靠认领链接发放）/ `open`（开放注册）。**非 off 时必须设 `CHIRAL_SECRET_KEY`**，否则 Core 拒绝启动——门户要可恢复地存订阅 token |
| `CHIRAL_PORTAL_INVITE_CODE` | 可选 | 开放注册时的共享注册码；留空则任何人都能注册 |
| `CHIRAL_WEB_DIR` | 镜像内已设 | 前端构建产物目录。门户在 `/`，管理台在 `/admin/`。留空则 Core 完全不伺服静态文件（交给反代） |

## 待办

- [x] `docker-compose.yml`
- [x] Dockerfile.core（含 Xray；web 前端就绪后并入或单独起容器）
- [x] `.env` 示例（[`.env.example`](.env.example)）

**状态**：草案，随各里程碑修订。
