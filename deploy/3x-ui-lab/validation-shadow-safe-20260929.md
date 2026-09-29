# SHADOW 观测副作用加固验证（2026-09-29）

## 修正内容

SHADOW 曾用 `GET /panel/api/server/getConfigJson` 验证读取权限。官方配置组装路径会调用
`AddTraffic(nil, nil)`，可能执行用户到期、额度和续期维护；请求是 GET 或前后配置相同，
都不能证明没有这类副作用。原始模板读取也可能修复旧存储，不适合作为替代。

现改为 `GET /panel/api/xray/getOutboundsTraffic`。在核对的官方 v3.8.5 源码
`7ef22f94c950ff09f0870e2295fa65ad5968742c` 中，该 handler 只读取已持久化的出站计数表。
它与原接口均不在 monitor/node-sync 令牌白名单内，但只证明这一次读取权限，不证明未来
版本的配置权限或写入语义。保持原有 17 项 OpenAPI 路径/方法检查，不增加或豁免接管门槛。

- 自动观测仅访问状态、OpenAPI、已存储出站计数；冷启动、缓存刷新、版本变化分别测试。
- 权限拒绝和异常 schema 不发布成功契约；故障缓存过期后可重新验证。
- 真实契约测试增加传输层精确请求白名单，配置、模板及所有显式写请求在发送前被拒绝。
- 两个实验运行器不再主动组装配置来验证“无变化”，也不再报告配置等价已验证。
- 新运行器要求 `shadow_request_allowlist_verified=true`，拒绝旧同名测试程序的单独 PASS。
- 候选写入的完整配置比较保留；认证使用时间等上游元数据不属于绝对不变承诺。

源码依据：[配置组装](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/service/xray.go#L191)、
[出站计数读取](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/service/outbound/outbound.go#L74)、
[令牌权限](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/controller/api.go#L78)。

## 官方隔离验证

使用 `write_contract.py --suite shadow-observation`，每轮创建全新空实例：`network=none`、
无宿主机端口、无生产挂载。没有访问 Turin，没有修改 3x-ui，没有在已有实例运行探测。
运行器建立实验数据库和私有令牌属于准备操作；Agent 观测本身不写这些对象。

- 官方镜像：`ghcr.io/mhsanaei/3x-ui@sha256:e0f90c10902e0e74f947d5a9efe017b273804477430233bbfc4918542ffe366c`。
- Linux/amd64：`sha256:be0af3a71e948bab24852b5dc571fd9835834847f086a84b1c4eb5932dd0fe50`。
- 首轮 `/private/tmp/chiral-shadow-safe-20260929-01`：FAIL，0.02 秒，退出码 1；官方监听
  已就绪但首次状态缓存未生成，接口无 object，测试未将其当作成功。随后只增加测试侧
  十秒有界状态等待，不发送修复或生命周期命令。
- 第二轮 `/private/tmp/chiral-shadow-safe-20260929-02`：PASS，0.83 秒，无 SKIP；测试、
  Docker 执行和运行器退出码均为 0。官方状态为 3x-ui 3.8.5、Xray 26.9.9，17 项能力通过。
  日志确认最终配置请求、模板请求、显式写请求均为 0，且明确 `config_equivalence_verified=false`。
  有效令牌读取与无效令牌拒绝均通过，缓存观测保留原契约时间。

两轮六个容器均停止并保留，精确 ID 的独立只读状态检查确认没有本轮容器继续运行。
全量 Go 测试、静态检查、Core/Agent 构建通过；最终包回归通过，55 项运行器安全测试通过。
没有重新执行完整 Core/Agent Compose 部署，因此不将此 HTTP 验收称为部署或生产接管验收。

## 完整目标的审计边界

本次修正消除一条不应存在于自动观测中的维护触发路径，不解决完整接管所需的官方能力差异。
另行复核官方 main 的在线 handlers、IP job 和 OpenAPI 后，仍未发现全体用户同轮在线 IP
扫描的成功、完整性及代次标志：email 缓存、最后在线时间和持久化 IP 记录不能代替它。
单用户 IP 读取还可能把读取错误折为空列表，不能据此确认无人在线。

最新核对依据：[官方 OpenAPI](https://raw.githubusercontent.com/MHSanaei/3x-ui/main/frontend/public/openapi.json)、
[IP 采集任务](https://raw.githubusercontent.com/MHSanaei/3x-ui/main/internal/web/job/check_client_ip_job.go)、
[IP 归属数据](https://raw.githubusercontent.com/MHSanaei/3x-ui/main/internal/web/service/inbound_node_ips.go)。
这不是“没有在线功能”，而是所需完整性证据未通过 HTTP 公开，不能通过缓存或 TTL 补出。

仍可继续的实质工作包括真实下载替换与容器重建验收，以及隔离的全用户订阅比较工具。
前者需要独立空实例的受限出网，当前无网络运行器不能证明；后者最终需要一致备份、对应
密钥、基准版本和确定的外部缓存，不能直接启动带迁移及后台任务的生产 Core 充当导出器。
预安装及离线内核回滚仍暂缓；所有未通过门槛保留，ACTIVE 未启用。
