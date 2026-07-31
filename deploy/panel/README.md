# deploy/panel — Panel 部署

`core` + 前端 + 可选 `caddy`/`nginx`(TLS) 的 docker-compose。

部署只要这个目录里的两个文件，不需要克隆仓库：

```bash
mkdir -p /srv/chiral && cd /srv/chiral
curl -fsSLO https://raw.githubusercontent.com/SayukiOvO/chiral/main/deploy/panel/docker-compose.yml
curl -fsSL  https://raw.githubusercontent.com/SayukiOvO/chiral/main/deploy/panel/.env.example -o .env
docker compose up -d
```

从本 checkout 构建而不是拉镜像（开发用）：

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

镜像里**同时打包 Xray 二进制**：面板在存储 / 下发前要用 `xray -test` 校验渲染出的 config，并用它派生 Go 标准库没有的 ML-DSA-65 密钥。与 Agent 镜像同走快照通道。

镜像里这一份是**地板**。M6 之后面板会为机队里在跑的**每一个版本**各存一份二进制（`CHIRAL_KERNEL_DIR`，必须在数据卷上），按节点挑选——因为 `xray -test` 只对跑它的那个 build 有效。详见 [`../../docs/xray-upgrade.md`](../../docs/xray-upgrade.md)。

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
| `CHIRAL_WEB_DIR` | **留空** | 前端已编译进二进制（门户 `/`，管理台 `/admin/`）。此变量会**覆盖**内嵌副本，只在自挂目录时才设——指向镜像里没有的路径会让两个入口全部 404，Core 只发一条告警 |

## 待办

- [x] `docker-compose.yml`
- [x] Dockerfile.core（含 Xray；web 前端就绪后并入或单独起容器）
- [x] `.env` 示例（[`.env.example`](.env.example)）

**状态**：M1–M6 已落地。未在本机构建过镜像（开发机没有容器运行时），Dockerfile 与 compose 的环境变量已与代码逐条核对过。
