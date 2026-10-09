# Turin 隔离验证记录 — 2026-09-29

## 范围与结论

分支 `codex/3x-ui-provider`（应用代码基于 `167d4e3`）已经在 Turin 完成真实 3x-ui API
只读契约及 Core → Agent 状态链路验证。本次新增实验部署、测试与说明，不切换生产 provider。

实验目录为 `/srv/chiral-3x-ui-lab-20260929`，Compose 项目为
`chiral-3x-ui-lab-20260929`。独立 Core、Agent 和官方 3x-ui 使用全新数据库、节点身份、
管理员凭证与命名卷；不复用生产配置、数据库、凭证或网络。没有宿主机端口映射。

## 验证输入

- 3x-ui：`3.8.5`，官方镜像
  `ghcr.io/mhsanaei/3x-ui@sha256:e0f90c10902e0e74f947d5a9efe017b273804477430233bbfc4918542ffe366c`。
- 3x-ui 内置 Xray：`26.9.9`。
- Core 基础运行镜像：
  `moonwx/chiral-core@sha256:c96e02da8980942411fc2436a04e5c1dae3cba831dea41ad2284b20f0a5db7c2`。
- Agent 基础运行镜像：
  `moonwx/chiral-agent@sha256:84a755007bb63b3c8c5586dd538ae0ab4a88fcf6600dc8e10395eec96ee08333`。
- Core 与 Agent 使用本地交叉编译的当前源码二进制覆盖基础镜像中的应用文件，仅生成新的实验镜像。
- 实时 OpenAPI SHA-256：
  `c8669ba7177ef33d8e9d1ad85e1a949679cc94cc31278469ef8685f5f04fdeba`。

这些 digest 仅记录本次可复现输入，不构成项目长期固定版本策略。

## 正向结果

- 实验 Core 健康检查成功；独立节点注册成功，direct-Xray 配置版本 1 获得 Agent ack。
- `TestLive3XUIReadOnlyContract` 两次输出明确 `PASS`，每次命令退出码均为 0，无跳过。
- 实际检查状态读取、实时 OpenAPI 的 16 项声明、最终配置读取、Observer 新鲜契约证据与无效 token 拒绝。
- 实验观测 token 禁用后，Core 状态变为 `SHADOW / INCOMPATIBLE`；重新启用后恢复
  `SHADOW / READY`。整个测试程序退出码为 0。
- 只读观察和 token 故障验证前后，3x-ui 配置内容相同。
- 三个容器共享独立网络命名空间，与宿主机不同；只有回环接口处于可用状态，IPv4/IPv6
  主路由表为空。内核默认生成的 `sit0` 处于 DOWN、无地址状态，不能作为外部网络路径。
- 实验容器丢弃全部 capabilities，无特权模式；卷归属与唯一只读 token 挂载均检查通过。
- 一次资源采样合计约 94 MiB 内存；各服务均有 CPU、内存、进程数及日志限额。

完整实验记录保存在远端 root 专用目录下的 `artifacts/verification.json` 和
`artifacts/contract-test.log`，凭证不会写入上述验收记录。

## 生产边界核对

只读核对生产 `agent-chiral-agent-1`、`panel-core-1`、`nginx` 的容器 ID、镜像、PID、
启动时间、重启次数均未变化，Xray PID 始终为 `6251`。
生产 `config.json` 与节点身份文件摘要未变；公开 HTTPS 健康检查返回 `ok`。

首次数据库对比中，只有 `credentials` 表摘要不同，该表包含在线业务持续更新的流量计数。
随后明确排除心跳时间和流量计数，重新采集静态基线并完整重复隔离验收；包含用户、凭证、
Profile、节点配置历史、访问授权、用户组、中转和变量的 28 张表静态摘要全部一致。
原始与复核记录均保留在实验目录的 `artifacts/production-*.json`，没有覆盖初次证据。
实验未重启或重新配置生产服务，未向生产 Core 发送写请求，也未修改生产数据库或数据卷。

## 已修正的部署与验收问题

- 原 SHADOW override 会重建现有 Agent，并停止其 direct-Xray 子进程，不能用于要求连接
  不中断的生产节点；部署说明现已明确指向独立实验项目。
- 初始化不仅拒绝已有 Compose 标签资源，也拒绝无标签但同名的五个实验数据卷，防止意外复用。
- token 吊销请求即使响应丢失，恢复操作仍在 `finally` 中执行；HTTP 401/403 的预期状态为
  `INCOMPATIBLE`，不是网络不可达。
- 兼容 Compose v5 将内存限额输出为数字字符串，同时保持严格限额与网络校验。
- 19 项 Python 安全回归测试通过，并加入 CI；相关 Go 包回归测试及静态检查通过。

## 仍未验证或交付

16 项能力匹配表示上游声明了所需接口，不表示已经执行其中的写操作。当前 Agent 仍只读
3x-ui，不具备生产配置调和、用户写入、3x-ui 增量计费、在线绝对快照、升级安装和回滚接管。
实验没有互联网出口，未验证公网代理数据面、内核下载或真实订阅等价性。
不得将 `SHADOW / READY` 当作 ACTIVE 或生产迁移验收。

停止实验且保留所有实验数据：

```bash
ssh turin 'sudo python3 /srv/chiral-3x-ui-lab-20260929/lab.py stop'
```

本次没有生产回滚步骤，因为生产部署未改变。停止实验只影响上述独立项目；不得执行
系统级 Docker 清理或删除生产资源。
