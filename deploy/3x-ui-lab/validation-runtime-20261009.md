# 官方 3x-ui 3.9.0 HTTP Runtime 隔离验收

## 结论与范围

2026-10-09，第 08 次全新本机隔离实验整体 **PASS**：Go 测试、测试容器和运行器均退出 0，
无 SKIP，全部 13 项专属正向标志通过。实际执行 `threexui.Runtime`，不以模拟 runtime
或 HTTP 封装单测代替执行接线。第 06 次较窄套件亦通过；第 08 次另增加真实凭证修改与
精确已有配置确认。所有实验容器均已停止，数据卷和私有日志保留。

**这不是完整迁移或生产接管验收。** 未访问 Turin，未读取或写入生产数据库、凭证、配置、
节点或容器；未修改 3x-ui 源码。实际 Core／Agent 进程的 gRPC 端到端、全协议全字段、
最终结算、完整入站／出站遥测、普通更新调度与全量生产订阅等价均不在此次 PASS 范围。
默认 direct-Xray、SHADOW 白名单与 ACTIVE 闸门不变。

## 输入与隔离

- 官方镜像：`ghcr.io/mhsanaei/3x-ui:v3.9.0`，本次解析摘要
  `sha256:93a7a68e3d0a65be1b02d2bfc1f3ca02d0e800516a403e103cea2d7c7090855d`。
- 实际 Linux amd64 镜像 ID：
  `sha256:81ffb49b3f1b7cc0e15a3c015c2fb8f6723c5cf3f3083d98ef6ebb660abb8a56`。
- `write_contract.py --suite runtime` 与 Linux amd64 契约测试程序；镜像摘要只标识验收输入，
  不恢复产品固定版本策略。
- 本次测试程序 SHA-256：
  `13c27d091541110be7a259d5a972c12e8cef02bdeb3892de4b00b0829b1a776a`。
- 每次均使用独立空实例、随机管理员与 token、随机对象名和全新专属卷。应用为
  `network_mode=none`，测试容器共享其回环命名空间；没有宿主端口、Docker socket、
  生产挂载或外部网络。逻辑公网目标经测试专属自由出站重定向到同命名空间的回环源站，
  不访问真实公网或 DNS。
- 一次性 Xray 客户端只作为数据面仪器；控制 provider 的入站、用户、配置及所有计费读取
  均调用官方 HTTP，不调用被管理内核的 Xray API、不自行安装或管理其二进制。

## 第 08 次正向证据

测试耗时 **19.39 秒**，`TestLive3XUIRuntimeContract` 明确 PASS，Go、Docker 测试与运行器
退出码均为 **0**。运行器同时核验下列标志，缺一不可：

- `runtime_http_mutations_verified=true`：通过正式候选空入站引导，随后新增、临时停用、
  恢复及 `keepTraffic=1` 永久退役；逐操作完整成功响应与精确配置／归属回读。
- `runtime_modification_verified=true`：修改第三条凭证，新凭证真实认证成功，旧凭证拒绝；
  原目标和控制用户已建立响应流继续接收逐段校验的字节。
- `runtime_exact_configuration_confirmation_verified=true`：只确认已等价的完整组装配置，
  使用官方真实 HTTP 探测并确认版本 7；不是任意不同配置的应用能力。
- `runtime_configuration_refusal_verified=true`：不同的任意配置与不可表达的 `level` 在
  写入前拒绝，私有配置检查点不变，两个现有用户仍能传输。
- `existing_streams_survived=true`：停用、恢复、修改及永久退役期间，两条既存响应流
  持续接收受控的精确内容；没有借额外重启弥补字段差异。
- `temporary_disable_new_connections_denied=true`：有界停用窗口内，新的目标连接未到源站，
  控制连接成功；重新启用后同一原始凭证与数字身份再次可用。
- `runtime_stats_receipts_verified=true`、`retained_batch_restart_verified=true`：真实后基线
  流量形成持久批次，关闭／重开 ownership 与 Runtime 后原样重发，精确 ACK 才释放。
  上游首批不可恢复缺口不估算、不补写，未声称计费精确无损。
- `runtime_recent_online_verified=true`：实际缓存取得受管用户近期正向记录，未返回清单外
  身份；与当前设备绝对快照分开。`positive_ip_observation_verified=false`，不伪造 IP 证据。
- `runtime_candidate_only=true`、`http_only_provider_control=true`：模式保持 CANDIDATE，
  不宣告 ACTIVE 或接管就绪。
- `tail_settings_restored_verified=true`、`fixture_reaped=true`：设置恢复并核验，客户端与
  源站关闭回收；运行器只停止精确归属本次运行的容器，卷不删除。

`final_settlement_verified=false`、`core_integration_verified=false` 明确保留。永久退役后的
旧流仍可产生字节，原统计身份保留并由账本继续追踪；本实验不能证明所有尾字节均已结清。

## 前序失败及修复

1. 第 01、02 次：初始化误把官方 API 入站的 `tunnel` 协议判为非空接入对象；现同时接受
   原版 `tunnel`／`dokodemo-door`，仍严格要求 API Tag 与回环监听，不放宽对象收养。
2. 第 03 次：仅发送一次流量后持续等待账本，未产生后基线增量；修正实验持续产生可核对
   的后续流量，以覆盖已接受的上游初始化采集缺口，不估算此前字节。
3. 第 04、05 次：官方 `ClientWithAttachments` 的只读 `traffic` 投影被误判为未知非空
   字段；现明确识别并排除更新请求，绝不把该投影当账务来源。列表 `allowedIPs` 的 CSV
   与更新数组类型也已明确转换，既有元数据保留。
4. 第 06 次：原套件全部通过。
5. 第 07 次：新增 HTTP 探测的预期 URL 与实验预存目标不同，被正确拒绝；修正实验在
   ownership 初始化前设置唯一探测目标，不放宽 Runtime 的目标一致性检查。
6. 第 08 次：扩展套件全部通过。未重新启动或自动重放任何前序失败现场。

## 本地回归与后续门槛

本轮全仓库 Go 回归与 `go vet` 退出 0；关键 Agent Runtime／Client、Core Node／API／Store
竞态测试退出 0。前端 4 个测试文件、32 项测试通过，生产构建退出 0（已有 Monaco 大包
警告仍在）；隔离运行器 81 项 Python 测试通过。协议检查及按既有插件生成通过。

Core 执行回执、UNKNOWN 持久屏障、离线永久撤权、重复撤权、保留个人授予、背景刷新级联
阻断均由临时数据库或模拟流测试验证，不把这些单测称为真实官方端到端。

任意配置原子应用与全字段热运行语义仍是未完成门槛；当前 `Apply` 的预写拒绝不是它们的
替代方案。普通更新仍未接入 Core 调度，最终账务收尾与长期收据策略仍须完成。不得依据
本次 PASS 操作生产、合并为完整迁移完成或启用 ACTIVE。

## 私有现场定位

第 08 次私有输出位于
`/private/tmp/chiral-runtime-20261009.K80W4o/official-390-08`，包含 `state.json`、脱敏及原始
测试日志；目录为 `0700`，文件为 `0600`。原始记录可能包含实验密钥，不提交。
运行标识：`chiral-write-b05d22f4944cfb6bb7713852`。所有资源已停止并保留，不执行系统清理。
