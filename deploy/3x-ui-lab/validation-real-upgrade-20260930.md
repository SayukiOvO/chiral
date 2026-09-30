# 官方在线内核更新隔离验证记录（2026-09-30）

## 结论

三次运行均为 **`INCOMPLETE`**，运行器退出码均为 1。没有一次派发 Xray 安装；没有验证
真实归档下载、二进制替换、目标版本运行、容器重建后的版本保持或更新后数据面。因此本记录
不是普通在线更新通过记录，也不改变 3x-ui provider 的 SHADOW／ACTIVE 边界。

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
- 第三次运行 ID：`chiral-write-cabd0873e7b69d05b73c00b3`

这些目录含管理员凭证、token、原始日志和完整状态，只能保留为本机私有证据，不得提交。
本文只记录脱敏的终态和正向、负向信号。

## 三次运行

| 运行 | 终态 | 已确认 | 未完成原因与边界 |
| --- | --- | --- | --- |
| 01 | `INCOMPLETE`，退出码 1 | 运行器在创建任何容器前执行网络预检 | Docker 自动加入规范值 `com.docker.network.enable_ipv4=true`，初版严格比较拒绝了该额外项；没有容器、没有 HTTP 请求、没有安装派发。运行器随后只对这一明确键值增加兼容，不接受其他网络选项 |
| 02 | `INCOMPLETE`，退出码 1 | 执行 internal isolated 网络、IPv6 与转发关闭、无默认路由、精确卷与容器身份以及更新前 HTTP 检查后，测试继续到版本目录查询 | 前置检查没有独立 PASS 标志；3x-ui 获取 GitHub 官方版本目录时收到下载代理 HTTP 502，prepare 阶段失败，安装未派发，`baseline-rebuilt`、`upgrade`、`target-rebuilt` 未执行 |
| 03 | `INCOMPLETE`，退出码 1 | 执行与 02 相同的前置检查后进入版本目录查询；代理留下实际拒绝日志 | 前置检查没有独立 PASS 标志；3x-ui 获取 GitHub 官方版本目录时收到下载代理 HTTP 502。代理记录 `domain=api.github.com decision=reject reason=dns_non_public bytes=0`，只证明解析结果包含非公网地址，不足以认定具体假 IP 或网段，也不是主动设计并完成的负向 CONNECT 验收。安装未派发，后三阶段未执行 |

02 和 03 的 prepare 阶段最终均为 FAIL。测试执行到版本目录查询，只能推断此前的 HTTP
检查没有使流程提前终止；由于没有独立成功标志，不能把前置检查、整个 prepare 阶段或任何
升级阶段记作 PASS。本轮没有任何完整阶段通过。表中的 HTTP 502 是 CONNECT 下载代理返回给
3x-ui 上游 GitHub 请求的状态，不是对 Chiral → 3x-ui handler HTTP 状态的断言。

## 隔离与保留结果

02 和 03 的应用只加入本轮 internal、isolated gateway 网络；IPv6 和 IP forwarding 关闭，
应用无默认路由。运行器按不可变 ID 核对本轮网络、容器和命名卷，没有使用宿主网络、生产卷、
生产凭证或 Docker socket。CONNECT 代理是唯一受限出站路径，未替代或修改 3x-ui HTTP API。

本轮创建的所有容器均已停止并保留，命名卷、网络和私有证据也保留用于诊断；没有执行广域
清理。01 在容器创建前终止，因此没有容器可停止。最终只读复核确认 03 的四个容器均为
`Exited`。

## 实现验证与后续条件

新增受限出网运行器、CONNECT 代理和四阶段真实测试；相关 Python 测试共 75 项通过，Go
全量测试与 `go vet ./...` 已有完整通过记录。这些代码检查不改变三次现场运行均为
`INCOMPLETE` 的结论。

下一次尝试前必须先解释并处置 `api.github.com` 的非公网解析结果；不得猜测具体地址、放宽为
任意 IP、绕过代理或自动重试安装。只有版本目录明确成功后才能继续基线安装；仍须分别取得
安装明确成功、运行目标版本、真实数据面、重建后卷与版本保持、配置及凭证无漂移，以及所有
阶段零退出、明确 PASS、无 SKIP 的证据。任何一项缺失都不能宣称普通在线更新验收通过。
