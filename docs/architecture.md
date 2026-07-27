# 架构总览

## 组件与术语

| 名称 | 是什么 | 语言 | 我们要写吗 |
|------|--------|------|-----------|
| **Panel** | 面板整体（前端 + Core） | — | 是（下面两项） |
| ├ 前端 | 两个入口：订阅者门户在 `/`，运维控制台在 `/admin/` | React/TS | 是 |
| └ **Core** | Panel 后端，全部 API / 业务逻辑 | Go | 是 |
| **Agent** | 节点侧守护进程 | Go | 是 |
| **Xray-core** | 被 Agent 管理的官方代理内核 | — | 否（外部二进制） |

> **术语约定**：本项目里 "core" 专指 **Panel 后端**；Xray 官方内核一律写作 **Xray-core**。

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

## 各组件职责

**Core（Panel 后端）**
- 对控制台提供管理 REST API；对订阅者提供门户 API（`/api/portal/*`）；对外提供订阅 API（`/sub/{token}`）。三者的鉴权边界见下。
- 唯一持久化真相来源（SQLite）：节点、模板、变量、用户、流量、审计、登录身份、来源地址。
- 模板渲染引擎：把 config 模板 + 变量渲染成节点 config.json；把客户端模板渲染成订阅。
- 与各 Agent 维护长连接，推送配置、下发用户增删指令与在线轮询策略、接收指标与在线快照。
- 鉴权与 RBAC、节点加入 token 签发。
- `CHIRAL_WEB_DIR` 非空时伺服前端构建产物（留空则交给反代）。

> **没有 WebSocket。** 早期草图里写过 "REST/WS API"，但从未实现，前端一直是轮询。仓库里没有任何 websocket 依赖。

**两类主体**
- **管理员**：账号在 `admins`，会话在 `admin_sessions`，三档角色（superadmin / operator / viewer）。
- **端用户（订阅者）**：登录身份在 `user_accounts`，会话在 `portal_sessions`，**没有角色**。

两者的隔离靠构造而非小心——门户 token 物理上无法解析成管理员身份，接错守卫与越权取数都是编译错误。详见 [user-portal.md](user-portal.md) §2。

**Agent（节点侧）**
- 启动时用 join token 注册，换取长期凭证，建立到 Core 的长连接。
- 管理本机 Xray-core 进程：拉起 / 重启 / 停止；把 Core 下发的 config.json 落盘并生效。
- 采集系统指标（gopsutil：CPU / 内存 / 磁盘 / 网速）与 Xray-core 流量（调 `xray api statsquery -reset` 子进程，不直连 gRPC）。
- 上报心跳与增量流量；执行在线 AddUser / RemoveUser。
- Core 开启在线记录时，按下发的 `OnlinePolicy` 周期上报 `OnlineReport`（绝对快照，见 [user-portal.md](user-portal.md) §7）。

**Xray-core**
- 官方内核，由 Agent 作为子进程管理。通过其 gRPC API（StatsService / HandlerService）做流量统计与在线增删用户，避免频繁重启。

## 关键数据流

1. **节点接入**：Core 建节点记录并签发一次性 join token → 页面给出 compose → 节点启动 Agent → Agent 注册换长期凭证 → 建立长连接。
2. **配置下发**：Core 侧编辑模板 / 变量 → 渲染 config.json → `xray -test` 校验 → 通过则推送给对应 Agent → Agent 落盘 + reload → 回 ack（版本号 + 生效状态）。失败可回滚到上一版本。
3. **指标采集**：Agent 周期采集 → 沿长连接上报 → Core 聚合写 SQLite → 前端展示。
4. **用户订阅**：用户访问订阅链接 → Core 按 URL 参数 / User-Agent 选客户端模板 → 用该用户的独立凭证渲染 → 返回对应格式（clash YAML / xray JSON / vless:// 列表）。
5. **端用户门户**（M5，默认关闭）：订阅者注册 / 登录 → 会话落 `portal_sessions` → `GET /api/portal/me` 一次返回账户状态、订阅链接、他有权限的线路及自己的 24 小时流量。看得到什么走白名单投影，不是在管理端视图上做减法。详见 [user-portal.md](user-portal.md)。

## 部署形态

- **Panel**：单个 `docker-compose.yml`。镜像里已打包前端构建产物与 Xray 二进制，core 容器自行伺服静态资源；可选 caddy/nginx 做 TLS 终止（那时必须设 `CHIRAL_TRUSTED_PROXY`，否则限流会把所有人算成同一个来源）。
- **节点**：Core 在「新增节点」时生成 compose 片段 + token，节点机器上 `docker compose up -d` 即可。

详见 [deployment.md](deployment.md)。
