# 开发里程碑

## M1 — Core ↔ Agent 骨架 ✅ 已完成
- proto 定义与代码生成；单模块 monorepo 构建通。
- 节点注册（join token → 长期凭证）、长连接、心跳、断线重连、在线判定。
- Agent 能拉起 / 停止本机 Xray-core，接收并落盘一份最简 config.json。
- Core 存 SQLite；前端能列出节点与在线状态。
- 前端为「信号控制台」风格（黑灰 + 克制绿，clean minimalism）；节点卡片含实时流量折线、心跳指标、内核状态、新增 / 重启 / 删除。
- 经 79 智能体对抗评审修复 + 真机端到端联调（真实 Xray-core 快照版跑通下发配置）。

## M2 — 模板 / 变量系统 + 配置下发
- 变量池（四作用域 + 生成器）。
- config 模板渲染 → `xray -test` → 下发生效 → 版本号 / 回滚。
- 节点 inbounds / outbounds 数组编辑（Monaco 编辑器）。
- Profile 抽象在此阶段用真实例子演示后定稿（见 template-system.md）。

## M3 — 用户 / 流量 / 订阅
- 用户管理（强隔离凭证）、可访问 inbound 区分。
- 在线 AddUser / RemoveUser；配额 / 到期 / 自动续期。
- 流量统计（增量上报、防重启归零）。
- 订阅 API：clash / xray / v2rayN(vless) / stash 客户端模板渲染 + UA 识别。

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
