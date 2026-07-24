# Chiral

一个 Xray 管理面板，采用 **Panel + Agent** 分布式架构，集中管理多台节点主机上的 Xray-core，提供节点监控、模板化配置下发、用户 / 流量管理与多客户端订阅。定位类似 Remnawave，全程支持 Docker / docker-compose 部署。

> **状态**：设计与脚手架阶段。核心决策已定，代码逐步实现中。设计细节见 [`docs/`](docs/)，项目约定见 [`CLAUDE.md`](CLAUDE.md)。

## 架构一览

```
┌──────────────────────── Panel (中心) ────────────────────────┐
│  前端: React SPA                                              │
│  Core (Go 后端): REST/WS API · 订阅 API · 模板渲染 · RBAC     │
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
│   ├── internal/       # api / auth / node / template / user / stats / subscription / store
│   └── migrations/     # SQLite 迁移
├── agent/              # Go 节点守护进程
│   ├── cmd/agent/      # 入口
│   └── internal/       # client / xray / collector
├── web/                # React 前端
└── deploy/             # Docker / compose（panel / agent）
```

## 技术栈

Go（Core + Agent）· React + Vite + TailwindCSS + shadcn/ui（前端）· SQLite · gRPC 双向流 · Docker Compose。

## 文档索引

- [架构总览](docs/architecture.md)
- [Core↔Agent 通信协议](docs/communication.md)
- [模板与变量系统](docs/template-system.md)（Profile 抽象待议）
- [用户 / 流量 / 订阅](docs/user-management.md)
- [部署](docs/deployment.md)
- [开发里程碑](docs/roadmap.md)

## 快速开始

> 待 M1 与 deploy/ compose 就绪后补充。届时流程大致为：`docker compose up -d` 起 Panel → 在面板新增节点 → 复制生成的 compose 到节点机器 → 节点 `docker compose up -d` 自动注册。
