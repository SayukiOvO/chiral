# Chiral

一个 Xray 管理面板，采用 **Panel + Agent** 分布式架构，集中管理多台节点主机上的 Xray-core，提供节点监控、模板化配置下发、用户 / 流量管理与多客户端订阅。定位类似 Remnawave，全程支持 Docker / docker-compose 部署。

> **状态**：M1–M5 已完成——节点管理、模板 / 变量系统与配置下发、用户与订阅、历史图表与中英双语、端用户门户与在线地址记录。设计细节见 [`docs/`](docs/)，项目约定见 [`CLAUDE.md`](CLAUDE.md)，进度见 [`docs/roadmap.md`](docs/roadmap.md)。

## 架构一览

```
┌──────────────────────── Panel (中心) ────────────────────────┐
│  前端: 门户 /  ·  控制台 /admin/   (同源，两个 Vite 入口)     │
│  Core (Go 后端): 管理 REST · 门户 API · 订阅 API              │
│                 模板渲染 · RBAC · 静态资源                    │
│                         SQLite (WAL)                          │
└───────────────▲───────────────────────────▲─────────────────┘
        长连接 │ 配置下发 / 指标上报           │ 长连接
        ┌───────┴────────┐            ┌───────┴────────┐
        │   Agent (节点A) │            │   Agent (节点B) │
        │  ├ 管理 config   │            │  ├ 管理 config   │
        │  └ Xray-core ◀──┘  gRPC 采集 │  └ Xray-core     │
        └────────────────┘            └────────────────┘
```

- **Panel** = 前端(React) + Core(Go 后端)。Core 是唯一持久化真相来源。
- 前端有**两个界面两类人**：订阅者的门户在 `/`，运维的控制台在 `/admin/`；两者的鉴权边界在服务端，见 [`docs/user-portal.md`](docs/user-portal.md)。
- **Agent** 跑在每台节点上，主动连回 Core，管理本机 Xray-core。
- **Xray-core** 是被 Agent 管理的官方内核，不是本项目代码。

详见 [`docs/architecture.md`](docs/architecture.md)。术语中 "core" 专指 Panel 后端，"Xray-core" 才是内核。

## 目录结构

```
chiral/
├── CLAUDE.md            # 项目权威约定（先读这个）
├── README.md            # 本文件
├── go.mod              # 单模块 monorepo
├── docs/               # 设计文档
├── proto/              # Core↔Agent gRPC proto（共享）
├── core/               # Go 后端（Panel 后端）
│   ├── cmd/core/       # 入口
│   ├── internal/       # api · auth · node · template · profile · user · subscription
│   │                   # store · secret · portal · online · alert · mail · passkey
│   └── migrations/     # SQLite 迁移（0001…0009）
├── agent/              # Go 节点守护进程
│   ├── cmd/agent/      # 入口
│   └── internal/       # client / xray / collector
├── web/                # React 前端
│   ├── index.html      # 门户入口（/）
│   ├── admin/          # 控制台入口（/admin/）
│   └── src/portal/     # 门户专属代码，不得 import src/api.ts 或 src/pages/
└── deploy/             # Docker / compose（panel / agent）
```

## 技术栈

Go（Core + Agent）· React + Vite + TailwindCSS（前端，手写组件原语）· SQLite（WAL）· gRPC 双向流 · Docker Compose。

## 文档索引

- [架构总览](docs/architecture.md)
- [Core↔Agent 通信协议](docs/communication.md)
- [模板与变量系统](docs/template-system.md)
- [代理用户 / 流量 / 订阅](docs/user-management.md)
- [端用户门户与在线地址记录](docs/user-portal.md)
- [部署](docs/deployment.md)
- [开发里程碑](docs/roadmap.md)

## 快速开始

**起面板**（见 [`deploy/panel/`](deploy/panel/)）：

```bash
cd deploy/panel
export CHIRAL_GRPC_PUBLIC_ADDR=panel.example.com:8443   # Agent 拨回的地址
export CHIRAL_ADMIN_TOKEN=$(openssl rand -base64 32)    # 破窗用
export CHIRAL_SECRET_KEY=$(openssl rand -base64 32)     # 静态加密，别丢
docker compose up -d
```

**首个管理员账号**：默认用户名 `admin`。密码取 `CHIRAL_ADMIN_PASSWORD`；**留空则随机生成并在启动日志里打印一次**——

```
level=WARN msg="created the first admin account; this password is shown once" username=admin password=…
```

生产部署建议显式设置它。控制台在 `/admin/`。

**加节点**：控制台「新增节点」→ 复制生成的 compose 到节点机器 → `docker compose up -d`，Agent 自动注册并接管本机 Xray-core。

**开门户**（可选）：设 `CHIRAL_PORTAL_MODE=open`（或 `closed`）。它要求 `CHIRAL_SECRET_KEY` 非空，否则 Core 拒绝启动。完整变量表见 [`docs/deployment.md`](docs/deployment.md)。
