# CLAUDE.md — Chiral 项目约定

本文件是 Chiral 项目的权威上下文，供 Claude 在后续会话中快速对齐。人类协作者也应先读本文件。改动重大决策时，请同步更新本文件。

## 1. 项目是什么

Chiral 是一个 Xray 管理面板，定位类似 Remnawave：采用 **Panel + Agent** 分布式架构，集中管理多台节点主机上的 Xray-core 及其 `config.json`，提供节点监控、模板化配置下发、用户与流量管理、多客户端订阅。必须支持 Docker / docker-compose 部署。

## 2. 架构与术语（务必分清）

- **Panel（面板，中心侧）** = 前端 + Core，两者合称。
  - **前端**：React SPA。
  - **Core**：Go 后端二进制，承载 Panel 的全部后端功能——REST/WS API、订阅 API、模板渲染、鉴权 / RBAC、SQLite 持久化、与各 Agent 的长连接管理。**本项目里 "core" 专指 Panel 后端，不是 Xray 内核。**
- **Agent（节点侧守护进程）**：Go 二进制，跑在每台节点主机上，主动连回 Core。负责管理本机 **Xray-core** 进程、应用 Core 下发的 config.json、采集指标与流量、上报心跳。
- **Xray-core（被管理的代理内核）**：Xray 官方内核二进制，由 Agent 作为子进程管理，不是我们要写的程序。

要开发的三块代码：**前端(React) + Core(Go) + Agent(Go)**。架构图见 `docs/architecture.md`。

## 3. 技术栈（已定）

- 后端 Core + Agent：Go
- 前端：React + Vite + TailwindCSS + shadcn/ui（Radix 底座）
- 数据库：SQLite（WAL 模式）
- Core ↔ Agent 通信：gRPC 双向流 over TLS
- 部署：Docker + docker-compose

## 4. 已定关键决策

1. **组件**：前端 + Core + Agent 三块代码；Xray-core 由 Agent 管理。
2. **通信**：方案 A —— Agent 主动外连 Core、保持长连接（gRPC 双向流）。节点只需出站网络、能穿 NAT。详见 `docs/communication.md`。
3. **用户凭证**：强隔离 —— 每个用户在每个 inbound 上使用独立凭证（UUID / password），`用户 × Profile × 节点` 粒度。任一凭证泄露只影响一个接入点。详见 `docs/user-management.md`。
4. **节点 config 组织**：骨架模板 + 可增删的 inbounds / outbounds 数组项（每项可含 `{{变量}}`）。
5. **proto / 代码生成**：`Register` 为 unary RPC（token 换凭证），`Channel` 双向流承载所有帧（首帧 `Hello`）。工具链 buf（remote 插件），生成代码提交入库。定义见 `proto/chiral/v1/agent.proto`。
6. **节点长期凭证**：每节点独立随机 opaque token（Core 只存哈希，走 metadata `x-chiral-credential`），不用 mTLS 客户端证书。开发期 Agent 支持 `--insecure`，生产强制 TLS。
7. **回滚**：非独立指令。版本历史在 Core 侧，回滚 = 把旧版本内容作为新 `ConfigPush` 重推；Agent 无状态、不存历史。

## 5. 搁置 / 待议

- **Profile 抽象**（把 服务端 inbound 模板 + 共享参数 + 各客户端模板 打包成一个单元）：用户暂未采纳，等实现模板系统时用具体例子重新演示后再定。`docs/template-system.md` 现有内容为参考稿，非定稿。
- 其余待补功能见 `docs/roadmap.md`。

## 6. 仓库结构

- `core/` — Go 后端（Panel 后端）
- `agent/` — Go 节点守护进程
- `web/` — React 前端
- `proto/` — Core↔Agent gRPC proto 定义（core 与 agent 共享）
- `docs/` — 设计文档
- `deploy/` — Docker / compose 部署文件

每个目录内都有 README 说明职责与规划，详见根 `README.md`。

## 7. 开发约定

- **Go module**：单模块 monorepo，模块路径 `github.com/SayukiOvO/chiral`（以 go.mod 为准；大小写与 GitHub 用户名规范一致）。仓库远端 `git@github.com:SayukiOvO/chiral.git`，提交走 SSH + Bitwarden agent，签名需 Mai 批准。core 与 agent 共享 `proto/` 包，不跨组件互相 import 对方的 `internal/`。
- **构建**：`go build -o bin/chiral-core ./core/cmd/core`；`go build -o bin/chiral-agent ./agent/cmd/agent`。
- **代码风格**：`gofmt` + `go vet`；包按职责分（放在各自 `internal/` 下）。
- **配置安全**：config 下发前必须 `xray -test` 校验，通过才生效；每版 config 存版本号，支持一键回滚。
- **流量统计**：Agent 上报增量而非绝对值，防 Xray-core 重启导致计数器归零。
- **变量泄露防护**：客户端模板只能引用「可公开」变量，私钥类变量在客户端渲染上下文中不可见。
- **前端**：日 / 夜 / 跟随系统 三态切换，移动端自适应；视觉参考新版 AWS / GitLab 的克制企业风。i18n 中 / 英。

## 8. 开发里程碑

见 `docs/roadmap.md`。当前从 M1（Core↔Agent 骨架）开始。

## 9. 协作方式

用户（Mai）是中文母语者，用中文沟通，时区 Asia/Shanghai。重大设计先讨论、达成一致再写代码（"逐步完成"）。代码标识符、路径、注释用英文。
