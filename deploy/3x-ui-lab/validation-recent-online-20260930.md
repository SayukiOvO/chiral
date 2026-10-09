# 官方近期在线 HTTP 读取器隔离验证（2026-09-30）

## 结论与范围

`TestLive3XUIRecentOnlineContract` 在全新、无外网的官方未修改实例中 **PASS**，测试容器、
Docker 调用及运行器的退出码均为 0，无 SKIP。该结果证明 Chiral 的实际 HTTP 读取器（非模拟）
`ReadRecentOnline` 可以取得真实流量形成的近期在线缓存，并限制到调用方请求的凭证名单。

此项不是 Agent→Core→界面的端到端验收，不证明完整当前在线集合、设备数、最后连接时间、
正向 IP 观测、持续会话或公网连通性。`ObservedOnline=false` 仅代表未在近期缓存中观测到，
不证明离线。没有实现或启用可写 3x-ui Runtime，没有扩大 SHADOW 自动读取白名单，也没有
改变配置字段保真、用户操作不中断和坏配置保留旧服务的接管门槛。

## 输入与私有证据

- 官方镜像：`ghcr.io/mhsanaei/3x-ui@sha256:e0f90c10902e0e74f947d5a9efe017b273804477430233bbfc4918542ffe366c`
- Linux/amd64 镜像 ID：`sha256:be0af3a71e948bab24852b5dc571fd9835834847f086a84b1c4eb5932dd0fe50`
- 套件：`write_contract.py --suite recent-online`
- 最终运行 ID：`chiral-write-670b6320c32696e91cc963e7`
- 最终测试二进制 SHA-256：`8b373bf556648d61e5f03b0e5d81cbf85b727d09e7598b5963977f29f8af66b9`
- 最终私有证据目录：`/private/tmp/chiral-recent-online-20260930-02`
- 最终 Go 测试耗时：15.48 秒。

初次运行 `/private/tmp/chiral-recent-online-20260930-01` 同样 PASS，耗时 18.18 秒。审查后将
未由测试动态核验的 SHADOW 白名单标记改为明确未验证，并以实际官方 HTTP 探测成功标记
代替可能被误解为进程清点的表述；随后在另一全新实例完成上述最终复验，未重用初次实例。

证据目录权限为 `0700`，`state.json` 与原始日志为 `0600`。目录含实验凭证与原始记录，
不得提交或公开；本文件仅记录脱敏结果。没有读取生产数据库、凭证或订阅，没有连接 Turin，
没有操作此前结果未知的升级实例，没有修改 3x-ui 源码或镜像。

## 验证方法与正向信号

在空实例中通过官方 HTTP 建立三个随机、独立的测试账户及回环入站。原始模板承载测试
流量，停用的受管对象仅用于元数据索引；这只是测试夹具，不是已采纳的生产配置投影方案。
夹具启用统计策略，并通过官方 `testOutbound` 对前两个账户反复发起真实认证请求。第三个
账户从未用于流量。临时探测内核仅由官方 3x-ui 管理，测试程序不启动 Xray，也不调用其
管理接口。

| 检查 | 已取得的证据 |
| --- | --- |
| 冷态基线 | 三个新账户均无近期观测与 IP 记录 |
| 真实正向 | 两个有流量账户均被官方近期缓存观测到；每次探测均验证源站命中及响应写出 |
| 请求名单隔离 | 先确认第二个账户确实有近期观测，再从请求名单排除它；读取结果仅包含指定的第一、第三账户 |
| 缺失语义 | 未使用账户仍明确返回未观测值；不声称离线、不构造实时快照 |
| 配置与对象 | 实验前后核对实例身份、自有对象集合与完整配置；清理后原模板、测试 URL 与配置恢复，实验元数据移除 |
| 退出与清理 | 顶层 PASS、无 SKIP、测试及运行器退出 0；三个精确实验容器 ID 均已停止 |

运行器要求以下六项成功标记全部存在，同时检查精确测试名称、顶层 PASS、无 SKIP 和
零退出状态。单独出现 PASS 不足以通过：

```text
recent_online_positive_verified=true
recent_online_whitelist_verified=true
recent_online_absence_is_unknown=true
http_only_control=true
official_http_probe_used=true
cleanup_verified=true
```

日志同时明确记录 `current_device_snapshot_verified=false`、
`positive_ip_observation_verified=false`、`core_integration_verified=false` 与
`shadow_allowlist_verified=false`。
回环 IP 不一定进入官方持久化记录，因而本次没有通过启用 fail2ban 或修改上游制造 IP 证据。
跨后代节点的 GUID 排除、地址权限与管理端展示仍依赖既有本地测试，未在本次实际实例中验证。
本测试使用实验管理 token，不证明最小只读 API 权限足够。

## 隔离与保留

初始化及应用容器均为 `network=none`，测试容器仅共享本轮应用的网络命名空间。没有宿主
端口、默认外网路径、生产挂载或 Docker socket；所有容器均使用 `restart=no`。
逻辑探测目标是公开字面 IP，但自有 VLESS 入站的专用出站将它重定向到同一隔离空间的
回环 HTTP 源站，不构成公网访问。

运行器清理后，再按三个不可变容器 ID 只读核对，均为 `running=false`、`exit=0`、
`ports={}`。自有随机测试账户和入站已移除；容器、命名卷与私有记录保留，未执行删除卷、
广域清理或任何生产操作。

## 尚未完成

仍须在具备完整写入条件的 provider 上完成 Agent→Core→界面全链路验收及正向地址观测。
本项不能替代最终流量结算、普通在线升级、真实生产副本迁移与全量订阅等价验收；整体 M8
仍未完成，生产接管保持关闭。
