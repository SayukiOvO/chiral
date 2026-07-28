# Chiral

**Xray 管理面板**：Panel + Agent 分布式架构，集中管理多台节点主机上的 Xray-core——节点监控、模板化配置下发、用户与流量管理、多客户端订阅、端用户门户，以及带金丝雀的内核在线升级。

*A self-hosted Xray-core management panel: template-driven config delivery, per-user credentials, multi-client subscriptions, a subscriber portal, and canary kernel upgrades.*

[![ci](https://github.com/SayukiOvO/chiral/actions/workflows/ci.yml/badge.svg)](https://github.com/SayukiOvO/chiral/actions/workflows/ci.yml)

---

## 特点

- **模板 + 变量，而不是表单**。节点配置由「服务端 inbound 骨架 + 每用户 client-entry + 各客户端模板」渲染而成，模板引擎只做 `{{变量}}` 替换。想用什么特性就写什么——xhttp 上下行分离、后量子 REALITY，不受订阅转换器的表达力限制。
- **强隔离凭证**。每个用户在每个 inbound 上有独立的 UUID / password，粒度是「用户 × 接入配置 × 节点」。一条凭证泄露只影响一个接入点。
- **下发前校验**。配置在存版本、下发之前先过 `xray -test`，不过则既不存也不发；每个节点用**它自己那份内核**校验，版本对不上时降级为不精确并如实告知。
- **内核在线升级，追 prerelease**。先升一台金丝雀，**由 agent 用被测的二进制真的当一次客户端上网**，字节回来了才算 ACTIVE；起不来或本来通现在不通就自动回滚，人看过结果再放行到全队。
- **两个界面两类人**。订阅者门户在 `/`，运维控制台在 `/admin/`，鉴权边界在服务端由构造保证。
- **敏感数据静态加密**。REALITY 私钥、后量子种子、订阅令牌、来源地址均以 AES-GCM 封存，密钥来自独立环境变量。
- 中英双语、日 / 夜 / 跟随系统、移动端自适应。

## 功能一览

| | |
|---|---|
| 节点 | 一次性 join token 注册、心跳与实时流量折线、资源历史（分钟采样保留 7 天）、配置版本历史与一键回滚 |
| 接入配置 | 服务端 inbound 骨架 + 每用户 client-entry + 客户端模板；Monaco 编辑器带变量高亮与校验 |
| 变量 | 全局 / 接入配置 / 节点 三作用域；生成器 `uuid` `x25519` `short_id` `password` `mlkem768` `mldsa65`，**与 xray 二进制交叉验证** |
| 用户 | 配额 / 到期 / 自动续期（配额按周期重置）、在线增删（不重启内核）、流量按小时累计保留 90 天 |
| 订阅 | `/sub/{token}`，渲染 `xray-json` `clash` `vless-uri` `stash`，按 UA 自动识别 |
| 门户 | 开放注册（可选共享注册码）或管理员发认领链接；订阅链接、线路状态、用量、并发地址 |
| 管理 | 多管理员 + 三级 RBAC、审计日志（180 天）、节点掉线告警（Telegram / webhook） |
| 登录加固 | TOTP / Passkey / 邮箱验证码 / 恢复码，按 IP 限流 |
| 内核 | 从 GitHub 发现版本（含 prerelease）、SHA-256 校验、金丝雀升级 + 手动放行、失败自动回滚 |

## 架构

```
┌──────────────────────── Panel（中心） ────────────────────────┐
│  前端：门户 /  ·  控制台 /admin/    （同源，两个 Vite 入口）   │
│  Core（Go）：管理 REST · 门户 API · 订阅 API                  │
│              模板渲染 · RBAC · 静态资源 · SQLite (WAL)        │
└───────────────▲───────────────────────────▲──────────────────┘
   gRPC 双向流   │ 配置下发 / 指标 / 流量      │   （over TLS）
        ┌────────┴────────┐          ┌────────┴────────┐
        │  Agent（节点 A）  │          │  Agent（节点 B）  │
        │  └ Xray-core     │          │  └ Xray-core     │
        └─────────────────┘          └─────────────────┘
```

- **Agent 主动外连 Core**，节点只需出站网络、能穿 NAT，不必暴露入站端口。
- **Core 是唯一持久化真相来源**：存储的配置才是重连时的真相，在线增删只是让改动立即生效。
- **Xray-core 是被 Agent 管理的官方内核**，不是本项目的代码。术语上 "core" 专指 Panel 后端。

详见 [`docs/architecture.md`](docs/architecture.md) 与 [`docs/communication.md`](docs/communication.md)。

## 快速开始

需要 Docker 与 Docker Compose。

### 1. 起面板

```bash
cd deploy/panel
cp .env.example .env      # 按注释填写，每一项都写了为什么
docker compose up -d
```

最少要设三个：

```bash
CHIRAL_GRPC_PUBLIC_ADDR=panel.example.com:8443   # Agent 拨回的地址
CHIRAL_ADMIN_TOKEN=$(openssl rand -base64 32)    # 破窗用的管理令牌
CHIRAL_SECRET_KEY=$(openssl rand -base64 32)     # 静态加密密钥，丢了等于丢掉所有私钥
```

**首个管理员**：用户名取 `CHIRAL_ADMIN_USER`（默认 `admin`），密码取 `CHIRAL_ADMIN_PASSWORD`；**留空则随机生成，只在启动日志里打印一次**：

```
level=WARN msg="created the first admin account; this password is shown once" username=admin password=…
```

控制台在 `/admin/`。

### 2. 加节点

控制台「新增节点」→ 复制它生成的 compose 片段到节点机器 → `docker compose up -d`。Agent 自动注册并接管本机 Xray-core。加入令牌一次性，用过即失效。

### 3. 建接入配置

「接入配置」新建 → 用生成器产生 REALITY 密钥等变量 → 写 inbound 骨架与客户端模板 → 绑定节点 → 预览确认 `xray -test` 通过 → 下发。

**关键约定**：服务端与客户端必须引用**同一个变量组的不同分量**（如 `{{reality.private}}` 与 `{{reality.public}}`）。这是唯一可靠的配对保证——`xray -test` 查不出配错的密钥对，只会在运行时静默握手失败。

### 4. 开门户（可选）

```bash
CHIRAL_PORTAL_MODE=open            # off | closed | open
CHIRAL_PUBLIC_URL=https://panel.example.com
```

门户开启时 `CHIRAL_SECRET_KEY` 与 `CHIRAL_PUBLIC_URL` **都必须非空，否则 Core 拒绝启动**——前者因为门户会可恢复地存订阅令牌，后者因为缺了它每个订阅者拿到的都是一条无法使用的相对链接。

## 生产注意

- **gRPC 端点必须配 TLS**（`CHIRAL_TLS_CERT` / `CHIRAL_TLS_KEY`）。开发期 Agent 可用 `--insecure`，生产不要。
- **反向代理**：面板 HTTP 建议由 nginx / Caddy 终结 HTTPS。此时必须设 `CHIRAL_TRUSTED_PROXY` 为该代理地址，否则限流会按代理 IP 归并；设错则任何调用者都能用一个请求头自选桶位。
- **`CHIRAL_SECRET_KEY` 要备份**。丢了就打不开所有私钥、订阅令牌与来源地址。轮换见 `CHIRAL_SECRET_KEY_NEW`。
- **`CHIRAL_KERNEL_DIR` 要落在数据卷上**。面板给每个机队在跑的版本保留一份二进制用于校验；放容器本地路径，重启就忘光。

完整变量表见 [`docs/deployment.md`](docs/deployment.md)，每一项的取舍理由写在 [`deploy/panel/.env.example`](deploy/panel/.env.example)。

## 已知边界

诚实列出，免得你按预期去找：

- **超限不会自动断开已建立的连接**。实测 Xray 的 `rmu` 与 `sib` 都掐不断活着的会话，只拒新连接——所以配额是「下一次连接被拒」，不是「当场断线」。见 [`docs/user-portal.md`](docs/user-portal.md) §8。
- **并发地址是提示，不是限制**。计的是不同源 IP 数：一家人在同一个 NAT 后面算 1，一部手机在 WiFi 与蜂窝之间切换算 2。
- **端用户不提供 MFA**。它守的东西比 `/sub/{token}` 已经免费给出去的还少。
- **没有内置备份 / 恢复**。SQLite 文件与 `CHIRAL_SECRET_KEY` 请自行备份。
- **升级校验会真的取一次 URL**（默认 `http://cp.cloudflare.com/generate_204`，`CHIRAL_PROBE_URL` 可改）。若该地址从你的机房出不去，金丝雀会判 `INCONCLUSIVE`——不会误回滚，但也确认不了任何事。

## 开发

单模块 monorepo，Go 1.25+ / Node 22+。

```bash
go build -o bin/chiral-core  ./core/cmd/core
go build -o bin/chiral-agent ./agent/cmd/agent
go test ./...

cd web && npm ci && npm run dev      # 门户 / ，控制台 /admin/
```

部分测试需要一个真实的 xray 二进制（`CHIRAL_XRAY_BIN` 或 `PATH` 里的 `xray`）——密钥派生与内核行为的断言都跑真内核，缺失时自动跳过。

- 项目权威约定：[`CLAUDE.md`](CLAUDE.md)
- 目录职责：各子目录下的 `README.md`
- 迁移：`core/migrations/`（0001–0014），启动时自动应用

## 文档

| | |
|---|---|
| [架构总览](docs/architecture.md) | 组件、职责、数据流 |
| [Core ↔ Agent 通信](docs/communication.md) | gRPC 帧、重连、协议决策 |
| [模板与变量系统](docs/template-system.md) | 渲染、作用域、生成器、`xray -test` 的能力边界 |
| [用户 / 流量 / 订阅](docs/user-management.md) | 凭证隔离、配额、订阅渲染 |
| [端用户门户](docs/user-portal.md) | 第二个信任域、在线地址记录 |
| [内核在线升级](docs/xray-upgrade.md) | 金丝雀、三值判定、回滚、中继 |
| [部署](docs/deployment.md) | 环境变量全表 |
| [里程碑](docs/roadmap.md) | 进度与验收记录 |

## 状态

M1–M6 完成：节点管理、模板 / 变量系统与配置下发、用户与订阅、UI 与中英双语、端用户门户与在线地址记录、内核在线升级与 Docker 收尾。

已在真实 VPS 上做过带 TLS 的完整部署演练：Docker 部署、真证书校验的 gRPC、VLESS + REALITY + Vision 端到端通信、金丝雀升级全流程与容器重启不降级，均验证通过。演练记录见 [`docs/roadmap.md`](docs/roadmap.md)。

项目仍然年轻，生产使用请自行评估。欢迎 issue 与 PR。

## 许可

尚未选定许可证。在此之前，默认保留所有权利。
