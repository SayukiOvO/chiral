# 普通更新恢复的隔离验证（2026-09-29）

## 结论与边界

恢复协调器的本地故障回归及官方原版实例的隔离恢复测试通过。测试只验证官方 HTTP
停止、重启、状态读取，以及故障后的真实代理字节传输；安装版本列表与安装响应由测试
传输层注入，**未下载、替换或回退 Xray 二进制**。没有修改 3x-ui，也没有访问 Turin。

此结果不是完整更新、生产迁移或 ACTIVE 接管验收；配置字段保真、计费及在线语义的既有
缺口仍以其他验证记录为准。真实下载、校验、替换及容器重建后的版本保持仍待隔离验收。

## 环境与操作范围

- 本机 Docker Desktop，新建随机标记的空实例，`network=none`，无宿主机端口映射。
- 官方镜像：`ghcr.io/mhsanaei/3x-ui@sha256:e0f90c10902e0e74f947d5a9efe017b273804477430233bbfc4918542ffe366c`。
- 实际选用的 Linux/amd64 镜像：`sha256:be0af3a71e948bab24852b5dc571fd9835834847f086a84b1c4eb5932dd0fe50`。
- 运行入口：`write_contract.py --suite update-recovery`；原有默认 `write` 契约未改变。
- 只使用新建的实例身份、令牌、配置、回环入站、凭证与数据卷，没有导入生产对象。
- 源码中的 3x-ui client 没有增加停止接口；停止仅用于隔离测试注入故障。流量发生器只运行
  自己的临时客户端配置，不读取 Xray 管理 API 或服务端配置文件。

## 实际结果

首轮 `/private/tmp/chiral-update-recovery-20260929-01` 在建立初始运行身份时 FAIL，未进入
故障场景；明确错误为 `cannot establish the official running provider identity`。配置恢复
校验通过，运行器只停止该轮所属容器并保留现场。随后将就绪检查改为有界等待同一响应
中的完整状态、版本和实例标识，新增缓存及超时回归；没有放宽成功标准或复用失败实例。

第二轮 `/private/tmp/chiral-update-recovery-20260929-02`：

- 测试 `TestLive3XUIUpdateRecoveryContract` 明确 PASS，24.24 秒，无 SKIP。
- 测试进程、Docker 执行及运行器退出码均为 0。
- 每次成功探测均新建 SOCKS/VLESS 连接，上传 32768 字节并校验下载 524288 字节；下载
  SHA-256 为 `c4bdaa3624c36bfe9873b245b37013e6e354499483218325a9706811303a44ca`。

| 场景 | 安装请求数（注入） | 官方恢复重启数 | 持久化结果 |
| --- | ---: | ---: | --- |
| 明确安装失败，官方进程已停止 | 1 | 1 | 安装 failed；服务 reachable |
| 官方恢复成功，但丢失恢复响应 | 1 | 1 | 安装 failed；恢复结果 unknown；服务 reachable |
| 丢失安装响应，服务仍在运行 | 1 | 0 | 安装 unknown；服务 reachable；更新闸门仍关闭 |

各场景均检查同 ID 重放、关闭并重新打开记录、继续核对，确认不会重复安装或恢复重启。
第三项没有模拟后台实际安装；后台任务在调用方超时后继续工作的风险由本地 HTTP 故障
测试另行覆盖，不把第三项误报为官方安装任务的终态证明。

最终日志具有 `fixture_process_reaped=true`、`cleanup_verified=true`、`empty_objects=true`、
`template_restored=true` 与 `assembled_config_unchanged=true`。本轮三个容器全部停止，
容器及卷保留。原始日志与令牌只保存在私有目录，仓库仅记录非敏感结果。

## 本地回归

- 全量 `CGO_ENABLED=0 go test -count=1 ./...`：PASS，退出码 0。
- 全量 `go vet`、Core 与 Agent 构建：退出码 0。
- 恢复相关 `TestXrayUpdate*` 连续十轮：PASS，24.606 秒，退出码 0。
- 就绪检查修正后的 `threexui` 整包测试及静态检查：PASS，退出码 0。
- 隔离运行器 Python 安全回归：49 项 PASS，退出码 0。
- 独立只读代码复核与差异检查通过。

协调器仍未接入 Core 自动调度或 ACTIVE provider。持久化的服务观测附带本地时间，是
历史证据，不是无期限的健康保证；未知安装不能因服务可达而自动解除后续更新限制。
