# 开发里程碑

## M1 — Core ↔ Agent 骨架 ✅ 已完成
- proto 定义与代码生成；单模块 monorepo 构建通。
- 节点注册（join token → 长期凭证）、长连接、心跳、断线重连、在线判定。
- Agent 能拉起 / 停止本机 Xray-core，接收并落盘一份最简 config.json。
- Core 存 SQLite；前端能列出节点与在线状态。
- 前端为「信号控制台」风格（黑灰 + 克制绿，clean minimalism）；节点卡片含实时流量折线、心跳指标、内核状态、新增 / 重启 / 删除。
- 经 79 智能体对抗评审修复 + 真机端到端联调（真实 Xray-core 快照版跑通下发配置）。

## M2 — 模板 / 变量系统 + 配置下发
- [x] Profile 抽象定稿（真实例子演示后采纳，见 template-system.md）。
- [x] 变量池（全局 / Profile / 节点 三作用域 + 生成器；用户级随 M3）。
- [x] 生成器 uuid / x25519 / short_id / password / mlkem768 / mldsa65，**与 xray 二进制交叉验证**。
- [x] 私钥类变量静态加密（AES-GCM，`CHIRAL_SECRET_KEY`）。
- [x] config 模板渲染 → 装配 → `xray -test` → 存版本 → 下发（校验不过则拒绝落地）。
- [x] REST API：变量 / Profile / 绑定 / preview / apply。
- [ ] 节点 inbounds / outbounds 与 Profile 模板的前端编辑（Monaco）。
- [ ] 回滚的 UI（后端能力 M1 已具备：旧版本作为新版本重推）。

## M3 — 用户 / 流量 / 订阅 ✅ 已完成
- 用户管理 + **强隔离凭证**（用户 × Profile × 节点 各自独立，加密存储）。
- 在线 AddUser / RemoveUser（走 `xray api`，不重启内核）；配额 / 到期 / 自动续期。
- 流量统计（`statsquery -reset` 天然增量，免疫内核重启归零）。
- 订阅 API：`/sub/{token}`，clash / xray-json / vless-uri / stash 渲染 + UA 识别。
- 前端用户页：配额条、订阅链接、接入配置授权。
- 经 132 智能体对抗评审修复五类缺陷 + 真机端到端验证（封禁 / 删除 / 撤销授权均可扛住 Agent 重启）。

**关键不变量**：成员变更后必须重新装配受影响节点——存储的配置才是重连时的真相，只改运行中的内核会被下一次重启还原。

## M4 — UI 打磨与增强
- 视觉语言已在 M1 定型（「信号控制台」：黑灰 + 克制绿，见 CLAUDE.md §7）；本阶段做延展与打磨。
- i18n（中 / 英）；shadcn/ui 组件化下沉。
- 节点资源与流量的历史图表（M1 的实时折线只有客户端内存窗口）。

## Backlog（额外建议，未排期）
- 节点离线告警（Telegram / webhook）。
- 在线设备数 / IP 数限制（防账号共享）。
- 多管理员 + RBAC；审计日志。
- SQLite 定期备份 / 恢复；时序数据保留策略（聚合 + 有限窗口原始数据）。
- 预留 Sing-box 客户端类型。
- 规模上来后评估迁 PostgreSQL（ORM 层留抽象）。
