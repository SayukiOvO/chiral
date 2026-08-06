# CLAUDE.md — Chiral 项目约定

本文件是 Chiral 项目的权威上下文，供 Claude 在后续会话中快速对齐。人类协作者也应先读本文件。改动重大决策时，请同步更新本文件。

## 1. 项目是什么

Chiral 是一个 Xray 管理面板，定位类似 Remnawave：采用 **Panel + Agent** 分布式架构，集中管理多台节点主机上的 Xray-core 及其 `config.json`，提供节点监控、模板化配置下发、用户与流量管理、多客户端订阅。必须支持 Docker / docker-compose 部署。

## 2. 架构与术语（务必分清）

- **Panel（面板，中心侧）** = 前端 + Core，两者合称。
  - **前端**：两个 Vite 入口的 React 应用——订阅者门户在 `/`，运维控制台在 `/admin/`。
  - **Core**：Go 后端二进制，承载 Panel 的全部后端功能——管理 REST API、门户 API、订阅 API、模板渲染、鉴权 / RBAC、SQLite 持久化、静态资源伺服、与各 Agent 的长连接管理。**没有 WebSocket**（早期草图写过，从未实现，前端一直是轮询）。**本项目里 "core" 专指 Panel 后端，不是 Xray 内核。**
- **Agent（节点侧守护进程）**：Go 二进制，跑在每台节点主机上，主动连回 Core。负责管理本机 **Xray-core** 进程、应用 Core 下发的 config.json、采集指标与流量、上报心跳。
- **Xray-core（被管理的代理内核）**：Xray 官方内核二进制，由 Agent 作为子进程管理，不是我们要写的程序。

要开发的三块代码：**前端(React) + Core(Go) + Agent(Go)**。架构图见 `docs/architecture.md`。

## 3. 技术栈（已定）

- 后端 Core + Agent：Go
- 前端：React + Vite + TailwindCSS。shadcn/ui 曾定为组件底座，实际是手写原语（`web/src/components/ui.tsx` 与 `primitives.tsx`）；下沉到 shadcn 属重构，见 §5
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
8. **Profile 抽象已采纳**（M2 定稿）：Profile = 服务端 inbound 骨架 + 每用户 client-entry 模板 + 各客户端渲染模板。模板引擎**只做 `{{变量}}` 替换**，不支持条件 / 循环——遍历节点 × 用户由 Core 用 Go 完成。详见 `docs/template-system.md`。
9. **Xray 用快照（prerelease）通道**：xhttp 上下行分离、后量子等特性只在快照版有，稳定版发布很稀疏。Panel 与 Agent 镜像都打包 Xray 二进制（Panel 用它做下发前校验 + ML-DSA-65 派生）。
10. **私钥类变量静态加密**：AES-GCM，密钥来自独立环境变量 `CHIRAL_SECRET_KEY`（不设则明文入库并告警）。威胁模型是「数据库文件外流」，不防已能在面板机执行代码的攻击者。
11. **订阅 token 改为可恢复存储**（M5 定稿，**推翻 0003 迁移里「从不存储」的原决策**）：`users.sub_token_enc` 用 AES-GCM 密封，AAD 绑定行。门户的核心价值之一就是随时能看到自己的链接，而「只能重新生成」会炸掉用户已配置的所有客户端。`sub_token_hash` 与查找路径一个字节不变。代价说清楚：从「我们想拿也拿不回」降级为「持有 `CHIRAL_SECRET_KEY` 就能拿回」——面板本来就以同样形式持有每个用户的每一条代理凭证，边际损失很小。配套硬约束：**门户开启且无 `CHIRAL_SECRET_KEY` 时 Core 拒绝启动**，不是告警。
12. **`OnlineReport` 是绝对快照，对增量规则的明确豁免**（M5）：流量计数器因 Xray 重启归零，所以必须报增量；而观测集合归零的含义相反——它意味着「没在观测」，绝不能读成「零设备」。即使没人在线也每轮发一个空的 `complete=true` 帧，让沉默保持有歧义这件事不发生。
13. **Xray 运行时升级：Core 点名版本，Agent 只回滚不前滚**（M6）。三条边界：(a) Core 在通知任何节点安装某版本之前，**先给自己装一份**——否则会出现「节点跑着面板校验不了的内核」，而这正是 `core/internal/kernel` 存在的意义；(b) 每个节点的配置用**它自己那份内核**校验（`xray_installed_version`，不是 running），版本对不上时降级为不精确校验并如实告知，**不拒绝下发**——真正的闸门是 agent 本地那次 `xray -test`，它跑的才是对的二进制；(c) 校验结果是**三值**的，且 **ACTIVE 的判据是「客户能上网」，不是「内核还活着」**：ACTIVE（agent 用**被测的那个二进制**起一个一次性客户端内核，拿 Core 下发的 `probe_outbound_json`——跟订阅同一套 xray-json 模板、同一个真实凭证渲染出来的——经本机 SOCKS 打到本节点自己的 inbound，再取一次 URL，字节真的回来了）/ INCONCLUSIVE（起来了，但没东西可测：没绑 profile、没有 xray-json 模板、没有启用的用户，或升级前的基线本来就不通）/ ROLLED_BACK（起不来或本来通现在不通，旧的已经回去了）。**旧判据是 `xray api statsquery`，实测会在一个没人能上网的节点上照样应答**（`agent/internal/xray/dataprobe_test.go:TestTheAPIAnswersOnANodeThatCarriesNoTraffic`），那正是升级会造成的故障形状——传输层坏掉、API 一切正常、订阅者全黑。**升级前先测一次基线**，判据是「本来通、现在不通」而不是「现在不通」，否则没有用户的节点、egress 被挡的机房都会被误判成回滚。把 INCONCLUSIVE 折进 ACTIVE，金丝雀就成了橡皮图章。中继走**拉模式**，一次一块：节点发送队列只有 16 帧，推 21 MB 会把 config push 挤掉。

14. **中转线路的凭证不属于任何自然人**（M6）：自有节点经自有节点，入口拨出口是**一条 outbound、一份凭证**——Xray 的 outbound 是静态的，它出示什么不可能取决于正在承载谁的连接。所以这份凭证存 `node_relays.secret` 而不是 `credentials`：后者按 `user_id` 建表，没有 user 可填；更要紧的是**计费必须只发生一次**，在订阅者认证的那个入口上，线路自己的字节再计一遍就是同一个 GB 算两遍（一遍算人、一遍算机器）。出口把它当普通 client 收下，`AddCredentialTraffic` 认不出这个 email 直接跳过，这正是对的。入口拨号用的是**出口那个接入配置的 `xray-json` 客户端模板**——从服务端 inbound 反推客户端 outbound 意味着重新推 REALITY 公钥、镜像每个传输参数，而且是第二份必须同步的实现；复用模板则让「入口怎么拨」和「订阅者怎么拨」是同一件事。线路有**自己的**拒绝表：「能直连出口吗」和「能经入口去出口吗」是两个独立的答案，而跑中转的理由通常正是前者为否。详见 `docs/node-relays.md`。

15. **端用户与管理员是两类主体，边界靠构造而非小心**（M5）：`portal.Identity` **永远不带 Role**，`auth.rank()` **永远不新增 `>= 1` 的值**。一旦有人给 rank 加了「user: 1」，`requireAdmin`（= viewer 档）覆盖的节点列表、用户列表、变量、Profile、流量、审计日志就全部对客户开放。（`config/preview` 不在此列——M5-2 已把它提到 `requireWrite`，正因为它返回含 REALITY 私钥与全部凭证明文的完整 config。）
    编译期能保证的部分要说准：**接错守卫是编译错误**（`portalHandler` 多收一个 `portal.Identity`，两种签名不统一）；**`package portal` 内部够不到 store**，所以越权取数在 `View` 这条路径上不可能。但 `package api` 里的门户 handler 是 `*Server` 的方法，仍持有 `s.st`（`portalLogin`、`portalChangePassword` 就在用它读写自己的账号行）——在那里写 `s.st.ListNodes()` 是能编译过的。**规矩是：凡是要展示机队信息，一律走 `View`。**

## 5. 搁置 / 待议

- **shadcn/ui 组件化下沉**：当前是手写原语，功能与观感已达标，属重构而非缺口。
- **端用户 MFA**：不做。它守的东西比 `/sub/{token}` 已经免费给出去的还少。`portal_challenges` 的 CHECK 已预留 purpose，将来加不用重建表。
- **超限自动断线**：做不到，不是不做——实测 `rmu` 与 `sib` 都掐不断已建立的会话（见 `docs/user-portal.md` §8）。
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

- **Go module**：单模块 monorepo，模块路径 `github.com/SayukiOvO/chiral`（以 go.mod 为准；大小写与 GitHub 用户名规范一致）。仓库远端 `git@github.com:SayukiOvO/chiral.git`，提交走 SSH + Bitwarden agent，签名需 Mai 批准。core 与 agent 共享 `proto/` 与根级 `internal/`，**不跨组件互相 import 对方的 `internal/`**。根级 `internal/`（目前只有 `internal/xrayarchive`）是这条规则的窄口子：两边都要做同一件事、谁也不拥有它时放这里——校验并解包 Xray release 归档，core 用它拿到校验用的内核，agent 用它拿到要跑的内核，两份实现必然会漂移。加新包前先问「这真的是双方共有、且不属于任何一方吗」。
- **构建**：`go build -o bin/chiral-core ./core/cmd/core`；`go build -o bin/chiral-agent ./agent/cmd/agent`。
- **代码风格**：`gofmt` + `go vet`；包按职责分（放在各自 `internal/` 下）。
- **配置安全**：config 下发前必须 `xray -test` 校验，通过才生效（不过则**既不存版本也不下发**）；每版 config 存版本号，支持一键回滚。注意 `xray -test` 的能力边界：它**查不出配错的密钥对**，也会静默忽略可选字段的拼写错误——所以服务端 / 客户端必须引用**同一个变量组的不同分量**，这是唯一可靠的保证（详见 docs/template-system.md §6）。
- **自实现密钥派生必须与 xray 二进制交叉验证，而且要比对「我们存的那一份」**：格式差一点，`xray -test` 不会报警，只在运行时静默握手失败。无法验证的分量宁可不产出。**这条规矩曾经在、也曾经失效**：x25519 的测试只把公钥拿去和 `xray x25519` 比，而公钥一直是对的——Go 的 ecdh 在推公钥时内部会 clamp 标量，所以我们存的**未 clamp 的私钥**和公钥不属于同一个标量，两边各自自洽、互相印证，谁也发现不了。上线之后的表现是 REALITY 服务端不认任何客户端，回落去代理真站，客户端只看见 `received real certificate`，面板、`xray -test`、节点日志全都干干净净。所以：**交叉验证要断言 `xray x25519 -i <我们的私钥>` 回显的私钥与我们存的逐字节相同**，而不是只比公钥——见 `TestX25519MatchesTheXrayBinary`（它在旧代码上会失败）。同类修复要优先选**不作废已发出去的订阅**的那种：clamp 只动公钥从不依赖的比特位，所以存量私钥能就地修（`store.RepairX25519Clamping`，启动时跑），重新生成密钥对则会炸掉所有人的客户端。
- **流量统计**：Agent 上报增量而非绝对值，防 Xray-core 重启导致计数器归零。
- **变量泄露防护**：客户端模板只能引用「可公开」变量，私钥类变量在客户端渲染上下文中不可见。
- **前端**：**两个 Vite 入口**——订阅者门户在 `/`（`src/portal/`），运维控制台在 `/admin/`（`src/pages/`）。门户侧不得 import `src/api.ts` 或 `src/pages/`，否则整个管理端会被打进门户包（实测门户 209 kB、Monaco 不可达）。两边共用 `src/components/` `src/lib/` `src/format.ts`，各自持有不同的 localStorage token 键。日 / 夜 / 跟随系统 三态切换，移动端自适应；i18n 中 / 英。视觉方向 **"信号控制台"（clean minimalism，参考 Revolut）**，配色是**传统黑灰夜间基调**：灰阶打底，chrome 全走黑白灰高对比（主按钮 `--signal` = 黑/白反色），**唯一彩色是克制的绿色**（`--online`），只留给"活着的东西"——在线节点、运行中内核、其实时流量折线（黑底绿线的经典监控观感）。**忌用紫色 / 钴蓝**（Mai 明确讨厌，见记忆 mai-frontend-aesthetic）。字体 Space Grotesk（标题）+ Inter（正文）+ Space Mono（遥测数据，自托管不依赖 CDN）；签名元素是每节点实时流量迷你折线。设计 token 在 `web/src/index.css`（`@theme inline` + CSS 变量运行时换肤）。

## 8. 开发里程碑

见 `docs/roadmap.md`。M1–M5 均已完成：骨架、模板/变量系统、用户与订阅、UI 打磨与 i18n、端用户门户与在线地址记录。

## 9. 协作方式

用户（Mai）是中文母语者，用中文沟通，时区 Asia/Shanghai。重大设计先讨论、达成一致再写代码（"逐步完成"）。代码标识符、路径、注释用英文。
