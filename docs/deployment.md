# 部署

全程 Docker / docker-compose。分两侧：Panel（中心）与节点。

## Panel 侧

单个 `docker-compose.yml`（见 [`../deploy/panel/`](../deploy/panel/)），包含：
- `core` 容器：Go 后端，挂载 SQLite 数据卷。
- 前端静态资源：镜像里已打包构建产物，core 经 `CHIRAL_WEB_DIR` 一并伺服。**门户在 `/`，运维控制台在 `/admin/`**；留空该变量则 core 完全不伺服静态文件，交给反代。
- 可选 `caddy` / `nginx`：TLS 终止（建议 ACME 自动签发证书）。用反代时**必须**设 `CHIRAL_TRUSTED_PROXY`，见下。

关键环境变量：

| 变量 | 说明 |
|---|---|
| `CHIRAL_ADMIN_TOKEN` | 破窗用的 bearer token（必需）。与账号密码并行，密码全丢了也还能进 |
| `CHIRAL_ADMIN_USER` / `CHIRAL_ADMIN_PASSWORD` | **首个管理员账号**。仅在库里一个管理员都没有时生效。用户名默认 `admin`；**密码留空则随机生成并在启动日志里打印一次**，之后再也拿不到——生产部署应显式设置 |
| `CHIRAL_SECRET_KEY` | 静态加密密钥；**不设则私钥类数据明文入库**（启动告警）。开启门户时它是**硬性要求**，见下。`openssl rand -base64 32` |
| `CHIRAL_GRPC_PUBLIC_ADDR` | Agent 拨回的 `host:port`，写进「新增节点」生成的 compose |
| `CHIRAL_AGENT_IMAGE` | 「新增节点」生成的 compose 片段里写哪个镜像。留空用内置默认值。发布到自己的 registry 时必须设，否则运维粘贴的命令指向一个不存在的镜像 |
| `CHIRAL_XRAY_BIN` | 面板侧 Xray 二进制（镜像内已打包）。下发前 `xray -test` 校验、ML-DSA-65 生成都靠它 |
| `CHIRAL_DB_PATH` / `CHIRAL_HTTP_LISTEN` / `CHIRAL_GRPC_LISTEN` | 路径与监听地址 |
| `CHIRAL_TLS_CERT` / `CHIRAL_TLS_KEY` | gRPC 端 TLS（生产必需） |
| `CHIRAL_TRUSTED_PROXY` | **有反代时必需**。逗号分隔的地址或 CIDR，只写 core 前面那一层。不设则限流按对端地址计（反代后就是所有人共用一个桶）；设错则任何调用方一个 `X-Forwarded-For` 就能自选桶，限流形同虚设 |
| `CHIRAL_WEB_DIR` | 前端构建产物目录（镜像内已设）。留空则不伺服静态文件 |
| `CHIRAL_PORTAL_MODE` | 端用户门户：`off`（默认）/ `closed`（仅登录，账号靠认领链接发放）/ `open`（开放注册） |
| `CHIRAL_PORTAL_INVITE_CODE` | 开放注册时的共享注册码；留空则任何人都能注册 |
| `CHIRAL_ONLINE_RECORD` | 记录每个用户的来源地址，默认 `off`。打开会给每个节点注入 `statsUserOnline`，即一次配置版本变更 + 一次 Xray 重启（该节点上的活连接会断一次） |
| `CHIRAL_SMTP_HOST` / `_PORT` / `_USER` / `_PASSWORD` / `_FROM` / `_TLS` | 邮箱验证与登录验证码。`HOST` 留空即不提供邮件因素 |

> **Panel 镜像里也带 Xray 二进制**：不是用来跑代理，而是用来在下发前校验渲染出的 config，以及派生 Go 标准库没有的后量子密钥。

> **`CHIRAL_PORTAL_MODE != off` 时 `CHIRAL_SECRET_KEY` 是硬性要求，缺了 core 会拒绝启动**（不是告警）。门户要能把订阅链接展示给用户，所以订阅 token 是可恢复存储的；那是一条公网可用的 bearer URL，明文入库意味着一份被拖走的 `chiral.db` 直接产出全部用户的可用链接。

### 轮换 `CHIRAL_SECRET_KEY`

密钥泄露、或只是想定期换，都走同一条路。**面板必须停机**——轮换要独占数据库，它会在一个事务里重写每一条密文。

```bash
docker compose down
cp /var/lib/chiral/chiral.db /var/lib/chiral/chiral.db.bak   # 先备份
CHIRAL_SECRET_KEY=<当前密钥> \
CHIRAL_SECRET_KEY_NEW=<新密钥> \
  docker compose run --rm core chiral-core -rotate-secret-key
# 把 .env 里的 CHIRAL_SECRET_KEY 改成新密钥
docker compose up -d
```

覆盖八处密文：变量私钥分量、渲染后的节点 config、config 骨架、用户凭证、告警目标配置、MFA 密钥、来源地址、订阅 token。**全在一个事务里**——半轮换的数据库比任何一端都糟，那会导致没有任何一个 `CHIRAL_SECRET_KEY` 能把面板启起来。

跑第二遍是安全的（已是新密钥的值会被跳过）。轮换到空密钥会被拒绝。

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
- [x] Dockerfile（core / agent；core 镜像含 node 构建阶段，前端两个入口一并打包）
- [x] `.env` 示例（[`../deploy/panel/.env.example`](../deploy/panel/.env.example)）
- [ ] Xray-core 二进制：现按随镜像打包（可 `--build-arg XRAY_VERSION` pin 版本），是否支持运行时拉取 / 在线升级待议

## 发布镜像

`.github/workflows/publish.yml` 把 `chiral-core` 与 `chiral-agent` 推到 Docker Hub，
`linux/amd64` + `linux/arm64` 双架构。

**触发方式**：推送 `v*` 标签（发正式版，会移动 `latest`），或在 Actions 页面手动
运行（可选钉死 Xray 版本、可选附加一个标签，**不会**移动 `latest`）。

刻意不在每次推 main 时发布：镜像里烘焙的是构建时抓取的 Xray 快照，「每次提交都发」
等于为一堆碰都没碰过代理链路的改动，一天给订阅者换好几次内核。打标签是一个决定，
提交不是。

**仓库需要配置**（Settings → Secrets and variables → Actions）：

| 类型 | 名字 | 值 |
|---|---|---|
| Variable | `DOCKERHUB_USERNAME` | Docker Hub 用户名 / 组织名，同时用作镜像命名空间 |
| Secret | `DOCKERHUB_TOKEN` | Docker Hub **访问令牌**（Account Settings → Personal access tokens），权限 Read & Write。不要用账号密码 |

推出来的就是 `<DOCKERHUB_USERNAME>/chiral-core` 和 `<DOCKERHUB_USERNAME>/chiral-agent`。
上游发布在 `moonwx/` 下，这也是代码里的默认值——用上游镜像时什么都不用配。

**fork 或换 registry 的话，面板要设 `CHIRAL_AGENT_IMAGE`**，否则控制台「新增节点」
生成的 compose 片段指向的是上游镜像，而不是你自己那份。

### 跨平台构建

Dockerfile 里 Go / npm / 下载三个阶段都钉在 `$BUILDPLATFORM` 上交叉编译，只有最后
的运行阶段是目标架构。arm64 因此不需要在模拟器里跑编译器——否则一次构建从一分钟
变成十几分钟。

Xray 版本发现会调 GitHub API，匿名限额是每 IP 每小时 60 次、而 CI runner 共享出口
IP。workflow 把 `GITHUB_TOKEN` 作为 **build secret**（不是 ARG，ARG 会留在镜像历史里）
传进去抬高限额。
