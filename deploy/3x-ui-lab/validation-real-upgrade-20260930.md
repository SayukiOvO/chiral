# 官方在线内核更新隔离验证记录（2026-09-30）

## 结论

六次运行整体均为 **`INCOMPLETE`**，运行器退出码均为 1。第 05 次有两个完整阶段明确 PASS：
真实安装 v26.9.8，并验证更新前数据面、配置／凭证保持和一次容器重建后的卷内版本持久性；
随后 v26.9.9 更新结果未知且服务不可达。第 06 次 prepare 也以未知结果和服务不可达结束。
没有一次完成全部四阶段，因此本记录不是完整普通在线更新通过记录，也不改变 3x-ui provider
的 SHADOW／ACTIVE 边界。

实验只使用官方未修改的 3x-ui 镜像及其 HTTP API。没有连接或操作 Turin，没有接触生产
数据、凭证、容器或网络，没有修改上游源码，没有启用 ACTIVE provider。预安装但不激活与
离线旧内核回滚仍按既有决定暂缓，不属于本次验收。

## 输入与证据

- 官方镜像：`ghcr.io/mhsanaei/3x-ui@sha256:e0f90c10902e0e74f947d5a9efe017b273804477430233bbfc4918542ffe366c`
- Linux/amd64 镜像 ID：`sha256:be0af3a71e948bab24852b5dc571fd9835834847f086a84b1c4eb5932dd0fe50`
- 套件：`TestLive3XUIRealUpgrade`
- 私有证据目录：
  - `/private/tmp/chiral-real-upgrade-20260930-01`
  - `/private/tmp/chiral-real-upgrade-20260930-02`
  - `/private/tmp/chiral-real-upgrade-20260930-03`
  - `/private/tmp/chiral-real-upgrade-20260930-04`
  - `/private/tmp/chiral-real-upgrade-20260930-05`
  - `/private/tmp/chiral-real-upgrade-20260930-06`
- 第三次运行 ID：`chiral-write-cabd0873e7b69d05b73c00b3`
- 第四次运行 ID：`chiral-write-ba30cc553981f4b9a2ea6747`
- 第五次运行 ID：`chiral-write-59c1f22608541086f018d5b7`
- 第六次运行 ID：`chiral-write-26724d7cf68d0412b6afd2ba`

这些目录含管理员凭证、token、原始日志和完整状态，只能保留为本机私有证据，不得提交。
本文只记录脱敏的终态和正向、负向信号。

## 运行记录

| 运行 | 终态 | 已确认 | 未完成原因与边界 |
| --- | --- | --- | --- |
| 01 | `INCOMPLETE`，退出码 1 | 运行器在创建任何容器前执行网络预检 | Docker 自动加入规范值 `com.docker.network.enable_ipv4=true`，初版严格比较拒绝了该额外项；没有容器、没有 HTTP 请求、没有安装派发。运行器随后只对这一明确键值增加兼容，不接受其他网络选项 |
| 02 | `INCOMPLETE`，退出码 1 | 执行 internal isolated 网络、IPv6 与转发关闭、无默认路由、精确卷与容器身份以及更新前 HTTP 检查后，测试继续到版本目录查询 | 前置检查没有独立 PASS 标志；3x-ui 获取 GitHub 官方版本目录时收到下载代理 HTTP 502，prepare 阶段失败，安装未派发，`baseline-rebuilt`、`upgrade`、`target-rebuilt` 未执行 |
| 03 | `INCOMPLETE`，退出码 1 | 执行与 02 相同的前置检查后进入版本目录查询；代理留下实际拒绝日志 | 前置检查没有独立 PASS 标志；3x-ui 获取 GitHub 官方版本目录时收到下载代理 HTTP 502。代理记录 `domain=api.github.com decision=reject reason=dns_non_public bytes=0`，只证明解析结果包含非公网地址，不足以认定具体假 IP 或网段，也不是主动设计并完成的负向 CONNECT 验收。安装未派发，后三阶段未执行 |
| 04 | `INCOMPLETE`，退出码 1 | 新代理显式使用容器级 DNS；停止后再次只读确认 `HostConfig.Dns=["1.1.1.1"]`、状态 `exited` | 仍在官方版本目录查询时遇到代理 `dns_non_public` 拒绝；指定 DNS 不等于实际解析路径得到预期结果。安装未派发，后三阶段未执行，所有本轮容器停止并保留 |
| 05 | `INCOMPLETE`，退出码 1 | `--proxy-dns-over-tls`；`prepare` PASS、退出码 0，真实安装 v26.9.8，`install_dispatches=1`、`replay_dispatches=0`，HTTP 探针及配置／凭证保持有明确标志；`baseline-rebuilt` PASS、退出码 0，新容器继续运行卷内 26.9.8，且该版本不同于镜像内置 26.9.9 | v26.9.9 `upgrade` FAIL、退出码 1，`installation_outcome_unknown`、服务 `unreachable`；`target-rebuilt` 未执行。上游脱敏日志为下载 `context deadline exceeded`，不能推断磁盘或进程终态；同一未知操作未重试 |
| 06 | `INCOMPLETE`，退出码 1 | `--proxy-dns-over-tls`；版本目录与更新前探针执行到真实安装阶段 | v26.9.8 prepare FAIL、退出码 1，结果 unknown、服务 unreachable；其余阶段未执行，同一未知操作未重试 |

02 和 03 的 prepare 阶段最终均为 FAIL。测试执行到版本目录查询，只能推断此前的 HTTP
检查没有使流程提前终止；由于没有独立成功标志，不能把前置检查、整个 prepare 阶段或任何
升级阶段记作 PASS。02 和 03 均没有任何完整阶段通过。表中的 HTTP 502 是 CONNECT 下载代理返回给
3x-ui 上游 GitHub 请求的状态，不是对 Chiral → 3x-ui handler HTTP 状态的断言。

05 的两个 PASS 阶段同时具有测试退出码 0、顶层 PASS 和阶段成功标志，可以作为基线安装与
第一次重建的正向证据；它们不能覆盖随后 upgrade 的未知终态。06 也不能因执行到安装请求而
推定下载、替换或启动成功。

## 隔离与保留结果

02 和 03 的应用只加入本轮 internal、isolated gateway 网络；IPv6 和 IP forwarding 关闭，
应用无默认路由。运行器按不可变 ID 核对本轮网络、容器和命名卷，没有使用宿主网络、生产卷、
生产凭证或 Docker socket。CONNECT 代理是唯一受限出站路径，未替代或修改 3x-ui HTTP API。

本轮创建的所有容器均已停止并保留，命名卷、网络和私有证据也保留用于诊断；没有执行广域
清理。01 在容器创建前终止，因此没有容器可停止。05 和 06 创建的全部容器均已停止并
保留；最终只读复核确认 03、05、06 的全部实验容器均已退出。

## 实验 DNS-over-TLS 边界

DoT 是显式 opt-in，默认关闭，并与 `--proxy-dns 1.1.1.1` 互斥。它只改变本轮新建 CONNECT
代理：固定直连 `1.1.1.1:853`，SNI 为 `one.one.one.one`，最低 TLS 1.2，启用证书验证且
没有明文 DNS fallback；运行器将该代理容器的显式 DNS 设为 `127.0.0.1`，防止环境解析成为
兜底。没有修改宿主、Docker daemon、应用容器、3x-ui、全局 DNS 或任何生产配置。

05 的 DoT 使官方目录和 release 归档可达，并产生真实安装证据；这只证明该次实验路径，
不构成通用网络或产品 DNS 方案。04 的明文容器 DNS 仍为 `dns_non_public`，也不能据此推断
具体假 IP 或网段。

## 安装超时与未知终态

官方提交 `7ef22f94c950ff09f0870e2295fa65ad5968742c` 的
[Web 服务](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/web.go#L651)
将 `WriteTimeout` 设为 30 秒，而
[downloadXRay](https://github.com/MHSanaei/3x-ui/blob/7ef22f94c950ff09f0870e2295fa65ad5968742c/internal/web/service/server.go#L924) 使用
60 秒 HTTP client。05 的脱敏上游日志明确记录下载 `context deadline exceeded`。因此下载
可能先明确失败，但 handler 可能已无法把失败正文送回 Chiral；Chiral 看到的仍是 unknown／
unreachable。该状态不是明确失败，不允许推断安装停止、发起恢复或重试同一操作。

## 实现验证与后续条件

新增受限出网运行器、CONNECT 代理、四阶段真实测试和可选 DoT；相关 Python 测试共 79 项
通过，Go 全量测试与 `go vet ./...` 均通过。这些代码检查不改变六次现场运行整体均为
`INCOMPLETE` 的结论，也不把第 05 次的两个阶段 PASS 扩大为完整升级通过。

下一步不是重试 05 或 06 的未知操作。必须先人工处置并归档未知状态，再以新的操作和全新
实验资源验证 v26.9.9 目标更新；仍须分别取得安装明确成功、运行目标版本、更新后真实数据面、
target rebuild 后卷与版本保持、配置及凭证无漂移，以及所有阶段零退出、明确 PASS、无 SKIP
的证据。任何一项缺失都不能宣称普通在线更新验收通过。
