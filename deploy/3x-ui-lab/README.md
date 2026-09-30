# 3x-ui 隔离验证环境

此 Compose 项目同时运行独立的官方 3x-ui、当前源码构建的 Chiral Core 和 Chiral Agent，
用于验证 `3x-ui-shadow` 的真实 API 契约与状态上报。该模式只读取 3x-ui；配置下发、用户、
流量和运行时操作仍使用 Agent 的 `direct-xray` 路径。验证通过不代表 3x-ui 已接管生产流量。

## 隔离边界

3x-ui 使用 `network_mode: none`；Core 和 Agent 共享其网络命名空间。三个服务之间只能通过
该命名空间内的回环接口通信，不能访问宿主机回环地址、生产网络或互联网。没有宿主机端口
映射、Docker 桥接网络或 Docker socket。3x-ui 的管理接口在初始化后仅监听
`127.0.0.1:2053`；Core 的 HTTP 和 gRPC 分别监听 `127.0.0.1:26080` 与
`127.0.0.1:26443`。这些地址均属于实验容器的网络命名空间，不是宿主机地址。

每次验证必须使用新的、明确指定的 Compose 项目名。五个命名卷均由项目名自动限定，
不使用外部卷或生产目录。唯一文件挂载是实验目录内只读的 `secrets/3x-ui.token`。
Core 使用全新数据库和独立管理员凭证；Agent 只注册到实验 Core。不得导入生产数据库、
加入令牌、节点凭证、订阅或配置文件。

三个服务均移除全部 Linux capabilities，启用 `no-new-privileges`，限制 CPU、内存、进程数
和日志大小。3x-ui 直接启动上游 `/app/x-ui`，不启动上游容器入口中的 fail2ban 或 cron。
服务不自动重启，便于保留失败现场。镜像下载、构建和容器生命周期仍会消耗宿主机资源；
运行前应确认宿主机具有足够余量。

## 准备

Core 和 Agent 的 Linux 二进制分别放入 `bin/chiral-core` 与 `bin/chiral-agent`。
在工作站按目标宿主机架构交叉编译，并为 Core 包含已构建的前端资源。两个 Dockerfile
仅将二进制覆盖到独立实验镜像中，不在宿主机安装或运行 Go、npm 工具链，也不改变基础镜像。
必需的契约测试二进制放在 `bin/threexui-contract.test`，供验证程序执行；它不进入镜像构建上下文。
`.dockerignore` 仅允许两个 Dockerfile 和 Core、Agent 二进制进入构建上下文。

例如，在仓库根目录为 Linux amd64 构建（其他架构应调整 `GOARCH`）：

```bash
mkdir -p deploy/3x-ui-lab/bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o deploy/3x-ui-lab/bin/chiral-core ./core/cmd/core
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o deploy/3x-ui-lab/bin/chiral-agent ./agent/cmd/agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o deploy/3x-ui-lab/bin/threexui-contract.test ./agent/internal/threexui
```

宿主机需要 Docker Compose v2 或更新版本、Python 3、`nsenter`、`ip` 和 `curl`。
将此目录和三个二进制放入新的 root 专用目录，并将目录权限设为 `0700`。

准备官方 `ghcr.io/mhsanaei/3x-ui` 镜像及 Core、Agent 基础镜像的已解析 digest。
基础镜像只提供运行环境、Xray 和 geofile，不导入任何生产容器状态。初始化程序负责生成
`.env`、独立管理员与加密密钥、实验加入令牌、3x-ui API token 和随机管理路径，无须手工生成
或提供生产凭证。`secrets/` 目录权限为 `0700`，文件权限为 `0600`。不得将这些文件提交到 Git，
也不要将完整 Compose 展开结果或容器环境输出到共享日志。

## 启动与验证

在此目录运行以下命令，将占位值替换为实际镜像 digest，并为本次实验选择新的项目名：

```bash
sudo python3 lab.py init \
  --project 'chiral-3x-ui-lab-<unique>' \
  --three-x-ui-image 'ghcr.io/mhsanaei/3x-ui@sha256:<resolved-digest>' \
  --core-base-image 'moonwx/chiral-core@sha256:<resolved-digest>' \
  --agent-base-image 'moonwx/chiral-agent@sha256:<resolved-digest>'
sudo python3 lab.py verify
sudo python3 lab.py status
```

初始化程序生成本次实验配置、构建独立镜像并创建项目专属数据卷。在 3x-ui 管理服务首次
启动前，通过上游 CLI 写入随机管理员凭证、随机基础路径和回环监听地址。
随后启动实验 3x-ui 和 Core，通过实验 API 关闭 3x-ui 的订阅服务，
在实验 Core 创建节点与一次性加入令牌，最后启动实验 Agent。`/app/bin` 的新命名卷使用 Docker
默认复制机制保留官方镜像内置的 Xray 与 geofile；不要将其配置成空目录 bind mount 或 `nocopy`。
初始化校验要求各服务使用明确的挂载源、目标与读写权限；内核卷必须完整、可写且仅由 3x-ui
挂载，Core 与 Agent 不得访问。容器重建会继续使用该卷内的内核，不会自动改为新镜像内置版本。

由于整个项目没有主机端口，不能直接访问宿主机的 `127.0.0.1:2053` 或 `:26080`。
验证程序通过 `docker exec` 或宿主机 root 使用 `nsenter` 进入实验网络命名空间后访问 API。
仅进入网络命名空间不授予测试服务访问宿主机目录的权限；验证程序仍须只读写实验资源。

验收应同时检查：

1. 容器实际没有宿主机端口映射、生产挂载、外部卷或可用默认路由。
2. 实验 Core 健康检查成功，实验 Agent 已注册并持续上报心跳。
3. Core 返回的运行时状态为 `SHADOW`，包含实际 3x-ui 版本、OpenAPI 摘要和能力检查结果。
4. 实验观测令牌失效后状态变为 `INCOMPATIBLE`；恢复令牌后重新进入 `SHADOW / READY`。
5. 实验前后生产容器未重建、未重启，生产数据库和配置未被实验程序写入；生产健康检查持续正常。

SHADOW 契约测试只允许状态、OpenAPI 和已存储出站计数的固定 GET；不再主动调用
`getConfigJson` 作前后比较，因为该接口可能执行用户生命周期维护。运行器必须看到明确
的请求白名单验证标志才接受 PASS；旧同名测试程序不能充当加固验收。报告不再声称通过
该观测证明了配置逐字节不变，配置等价性仍由明确授权的候选写入实验另行验收。

必须记录检查的成功信号和命令退出状态。上述 SHADOW Compose 实验无互联网出站，不能据此宣称真实公网代理、
内核下载或完整迁移等价性已经通过。当前按完整名称集合核对 17 项能力（包含只读版本列表
`xray_versions`），而非仅检查数量；这些能力来自实时 OpenAPI 声明匹配，不代表已经执行
其中的配置、用户、计费或内核升级写入操作。生产状态比较应区分静态凭证、授权与正常增长
的流量计数，不能要求在线生产数据库所有字节保持不变。验收记录见 [Turin 实验结果](validation-20260929.md)。

停止实验时，仅操作本次明确的 Compose 项目；
默认保留其命名卷和验证记录，不执行系统级清理或删除生产资源：

```bash
sudo python3 lab.py stop
```

`verify`、`status` 和 `stop` 均使用 `artifacts/state.json` 中保存的实验项目信息，无须重新指定
项目或镜像。该文件只属于本次实验，不能复制其他环境的状态覆盖它。更多参数可查看
`python3 lab.py --help`。

## 本机独立写入与数据面契约测试

`write_contract.py` 可在本机 Docker（包括 Docker Desktop）新建一个独立空实例，不需要
连接 Turin，也不启动或修改已有 Core、Agent。它仅接受已拉取的官方镜像 digest 和已编译的
Linux amd64 契约测试二进制。镜像摘要是本次验收输入，不是产品固定版本策略。

```bash
python3 deploy/3x-ui-lab/write_contract.py \
  --image 'ghcr.io/mhsanaei/3x-ui@sha256:<resolved-digest>' \
  --binary '/absolute/path/chiral/deploy/3x-ui-lab/bin/threexui-contract.test' \
  --output '/private/tmp/chiral-write-<new-unique-directory>'
```

输出目录必须不存在。运行器拒绝远程 Docker context、已有同名资源、外部挂载和公开端口；
bootstrap、应用和测试容器均采用随机专属标记、资源限制和权限收缩。应用只有自身回环网络，
测试容器共享该网络。测试的 token 与二进制只读挂载，使用本机用户 UID 读取私有 token。

写入测试要求空 inbound / client 列表，并保存原模板。只创建随机标识的回环测试入站与用户，
通过官方 HTTP 验证模板、用户凭证、启用状态、附着关系和删除。独立临时 Xray 客户端仅作为
测试流量发生器，经 SOCKS → VLESS → 隔离 HTTP 源站上传 32 KiB、校验下载 512 KiB；读取
用量仍只调用 3x-ui HTTP，不读 Xray 管理接口。严格字段保真子测试会检查合法的 client
`level` 字段是否保留，不能用基本 CRUD 成功替代完整模板表达力。

测试成功必须同时具有零退出状态、明确 PASS、无 SKIP、临时对象清理和配置恢复证据。
任何失败都不自动重试写入，也不自动删除现场；运行器只停止归属本次运行的容器，保留卷和
私有日志。`state.json` 记录精确容器标识与结果，`contract-test.redacted.log` 供诊断；
原始日志、token 和管理员凭证均在 0700 目录下以 0600 保存，不得提交到 Git。

这个测试不覆盖全部生产 Profile、所有用户订阅等价性、完整实时在线集合或离线内核回滚；
即使通过，仍不能启用生产接管。

首次官方写入及真实流量验收的结果为整体 FAIL，冷启动漏计与 client 字段保真仍有缺口；
详见 [本机写入验证记录](validation-write-20260929.md)。冷启动和后续流量分别检查，后者
通过不能抵消前者失败。

## 普通更新恢复的隔离故障测试

只验证本机新空实例的 SHADOW HTTP 边界时，可使用 `write_contract.py --suite shadow-observation`。
它与其他套件使用同样的无网络、无宿主端口和不接触生产卷约束；创建私有实例及 token 是
运行器的准备操作，不属于 Agent 旁路观测。该套件不替代 Core/Agent 心跳与令牌撤销恢复的
完整 Compose 验收。
本次请求边界加固的官方实测结果见 [SHADOW 加固验证记录](validation-shadow-safe-20260929.md)。

同一运行器提供 `--suite update-recovery`，使用新的输出目录和全新空实例。默认仍为上述
完整写入测试，两者分别判定，不得用恢复测试的 PASS 替代配置及计费契约结果。

```bash
python3 deploy/3x-ui-lab/write_contract.py \
  --suite update-recovery \
  --image 'ghcr.io/mhsanaei/3x-ui@sha256:<resolved-digest>' \
  --binary '/absolute/path/threexui-contract.test' \
  --output '/private/tmp/chiral-update-recovery-<new-unique-directory>'
```

此测试仍没有外网、宿主机端口或生产挂载。测试传输层注入可安装版本列表和安装失败／
响应丢失，**不调用真实安装下载接口**；故障所需的停止以及后续恢复、状态读取均调用
官方 HTTP API，恢复后的代理可用性以独立客户端的新连接和实际字节验证。故障注入代码
仅存在于测试中，不是部署组件、兼容服务或修改版 3x-ui。

验收重点是明确失败后只恢复一次、原安装失败与服务恢复分别保留、响应不明时不追加重启
或安装，以及临时配置清理与模板恢复。真实公网下载、校验替换和容器重建后的版本保持
不在此项测试证明范围内；这些仍须另行隔离验收，不能将此结果称为完整升级通过。

本机官方原版的三个恢复故障场景已通过，详见
[恢复验证记录](validation-update-recovery-20260929.md)。

## 官方真实在线更新隔离实验

`real_upgrade.py` 是独立的受限出网运行器，只在全新本机空实例上通过官方未修改的 3x-ui
HTTP API 验证版本目录、在线安装、容器重建与真实回环数据路径；它不放宽上述无网络运行器。
完整边界见[验收计划](real-upgrade-plan.md)。该运行器不连接 Turin，不接触生产数据或凭证，
不启用 ACTIVE provider，也不验证已暂缓的预安装不激活或离线旧内核回滚。

先构建 Linux/amd64 契约测试和受限 CONNECT 代理两个二进制。镜像必须已按精确 digest
存在于本机；`DOCKER_HOST` 必须固定为本机 Unix socket，输出目录必须是尚不存在的新私有目录：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c \
  -o '/absolute/private/path/threexui-contract.test' ./agent/internal/threexui
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o '/absolute/private/path/chiral-egress-proxy' ./deploy/3x-ui-lab/egress-proxy

DOCKER_HOST='unix:///absolute/path/to/local-docker.sock' \
python3 deploy/3x-ui-lab/real_upgrade.py \
  --image 'ghcr.io/mhsanaei/3x-ui@sha256:<resolved-digest>' \
  --binary '/absolute/private/path/threexui-contract.test' \
  --proxy-binary '/absolute/private/path/chiral-egress-proxy' \
  --proxy-dns-over-tls \
  --output '/private/tmp/chiral-real-upgrade-<new-unique-directory>'
```

运行器拒绝远程 Docker endpoint、已有输出目录、镜像拉取、外部资源接管及不精确的挂载或
网络身份。它只为版本目录及官方 release 下载域名提供受限 CONNECT 出站，保留 TLS 校验，
并拒绝非公网解析结果。测试输出包含管理员凭证、token、完整状态和原始日志；目录须保持
`0700`、文件须保持 `0600`，不得提交或复制到共享日志。运行器只停止本轮容器，保留容器、
命名卷、网络及私有证据供诊断。

如果宿主继承的解析路径为 GitHub 返回非公网地址，可在另一全新实验中显式传入
`--proxy-dns 1.1.1.1`。此选项只为新建的 CONNECT 代理设置容器级 DNS，并在创建和运行时
核对；不修改宿主、Docker daemon、应用容器或既有实例。默认仍不覆盖 DNS，也不在失败后
自动切换解析器。该地址是 [Cloudflare 公共解析器](https://developers.cloudflare.com/1.1.1.1/ip-addresses/)，
仅用于解析允许名单中的公开下载域名；容器级选项见
[Docker DNS 文档](https://docs.docker.com/engine/network/#dns-services)。网络环境仍可能拦截 DNS，
因此设置该参数不等于解析或下载验收成功；非公网结果仍被拒绝，TLS 校验和下载目标限制不变。

`--proxy-dns-over-tls` 是另一个互斥的 opt-in，不能与 `--proxy-dns` 同时使用。它只让新建
CONNECT 代理通过固定的 `1.1.1.1:853` 解析，TLS SNI 为 `one.one.one.one`，最低 TLS 1.2，
启用正常证书验证且没有明文 DNS fallback。该模式仅把代理容器的显式 DNS 设置为
`127.0.0.1`，避免 Docker 环境解析成为隐式兜底；不修改全局 DNS、宿主、Docker daemon、
应用容器、3x-ui 或生产配置。默认不启用任何 DNS 覆盖。
固定服务地址与 TLS 名称依据 [Cloudflare 官方 DoT 文档](https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-tls/)。

2026-09-30 的六次现场运行整体均以 `INCOMPLETE`、退出码 1 结束。第 04 次即使显式使用
`--proxy-dns 1.1.1.1` 仍得到 `dns_non_public`，没有安装派发。第 05 次使用 DoT 后，
`prepare` 与 `baseline-rebuilt` 均明确 PASS、退出码 0：从内置 26.9.9 的镜像真实安装
v26.9.8，一次派发且零重放，HTTP 数据探针、配置和凭证保持及新容器的卷内版本持久性均通过。
随后 v26.9.9 更新成为 unknown／unreachable，`target-rebuilt` 未执行；第 06 次 prepare 也以
unknown／unreachable 结束。两次未知操作都未重试，本轮创建的所有容器已停止并保留。本轮没有
四阶段整体 PASS，不能宣称目标更新、更新后版本保持或完整在线更新验收通过；详见
[验证记录](validation-real-upgrade-20260930.md)。

## 原始模板与计费记录的候选投影实验

`write_contract.py --suite raw-projection` 使用另一全新空实例，检验能否将完整 inbound/client
保留在原始模板，同时用永久停用的同 tag 受管 inbound 和永久停用的 client 建立官方计费
索引。停用记录的额度、到期和续期均为零，不通过其启用状态授予访问权；此方案只是实验，
不是已采用的生产架构。

完整回读比较包含管理接口、统计策略、路由顺序及 client 字段，拒绝通过忽略字段获得通过。
数据面与 HTTP 计费分别检查冷启动和后续请求，首轮失败仍使整项验收失败。热新增用户使用
全新凭证与 email，避免旧计数和默认停用重启混淆结果；撤销连接的反向验证也需同一入站
的有效控制用户成功，不能把进程未就绪或服务整体故障当作撤权成功。

清理先移除原始模板里的实验运行对象，再删除归属本轮的停用元数据，最后再次恢复并比较
原始模板与最终配置。删除停用的受管 inbound 仍可能重写同 tag 路由，因此不得将其称为
无副作用操作。实验容器只停止并保留，不触及其他实例。真实验证结果单独记录，不能用
本地模拟或子测试成功替代整体验收。

两轮官方实例的整体结果均为 FAIL：完整回读及基础授权检查通过，但冷启动和新用户
计费仍有缺口。详见 [候选投影验证记录](validation-projection-20260929.md)。

## 官方 HTTP 数据探测验收

`write_contract.py --suite http-probe` 在全新空实例中执行真正的官方 `testOutbound` 请求，
不注入探测响应，也不由测试程序启动另一个 Xray 客户端。测试只通过 HTTP 写入自有原始
模板对象及测试 URL；临时探测内核由原版 3x-ui 自行启动。

上游拒绝私网或回环测试 URL。夹具使用公开字面 IP 作为逻辑目标，避免 DNS；只有自有
回环 VLESS 入站的专用出站才将它重定向到同一隔离命名空间内的 HTTP 源站。`network=none`
且没有非回环的活动接口，因此该逻辑地址不会产生公网访问。此设置只属于实验，不能复制
到生产或作为外部连通性验收。

检查正常响应确实命中源站、错误凭证不能命中源站且前后有效探测仍可用，以及 HTTP 500
虽然被上游视为传输成功、仍被 Chiral 拒绝。验收后必须恢复原模板与原测试 URL，并完整
比较最终配置。任何失败、跳过或缺少明确 PASS 均使整项失败；运行器不重试，保留现场。

同一套件还用真实 HTTP 探测器验证候选更新恢复的三个故障场景；安装目录和返回由测试
注入，所有真实安装请求被拦截，只有停止、重启、状态及探测使用官方 HTTP。不得将其称为
真实下载验收。第二轮严格负向检查与恢复场景已通过，详见
[官方 HTTP 探测验证记录](validation-http-probe-20260929.md)。

此项仅验证 HTTP 探测器和隔离回环代理路径，不证明公网可用、完整业务正文、真实升级、
计费或在线快照等价，也不改变生产接管门槛。

## 私有探针预热与首笔计费

`write_contract.py --suite billing-bootstrap` 在新空实例中只开放私有探针，必须先观察到
真实计费增量，再增加拥有全新凭证的完整用户入站。新用户仅请求一次，不使用其凭证发送
任何 readiness 或预热流量。源站成功写出 51,200 字节、用户计数、独立账户不串账、完整
配置回读与恢复分别验收；只计入第二笔真实用户流量不能算通过。

该组合在隔离实例中已通过，见 [首笔计费验证记录](validation-bootstrap-20260929.md)。
这不是生产接管方案：Panel／采集对象重建后先于预热开放既有用户的问题仍未解决，完整
新入站也不能替代同一入站热增用户的验收。不得通过修改启动顺序或自动启用生产实例来
外推此有限结果。

## 近期在线 HTTP 读取器验收

`write_contract.py --suite recent-online` 在全新 `network=none` 空实例中，使用官方 HTTP
探测产生真实流量，再调用项目中的 `ReadRecentOnline` 读取器。先确认两个测试账户确实具有
近期观测，再缩小请求名单，证明未请求的活跃账户没有泄露；未使用账户只标记为未观测，
不据此判断离线。统计策略、完整配置、实例身份及清理恢复均单独核对。

该套件无需测试程序自行启动 Xray，不访问 Xray 管理接口。运行器必须同时看到精确测试
PASS、零退出、无 SKIP、六项成功标记与清理完成。仅验证官方 HTTP 读取器，不能外推为
当前设备快照、正向 IP 记录或 Agent→Core→界面端到端通过，也不扩大 SHADOW 自动读取权限。
真实结果见 [近期在线读取器验证记录](validation-recent-online-20260930.md)。

## 撤权后的持续连接与尾部统计

`write_contract.py --suite tail-accounting` 仅用于全新 `network=none` 空实例。它通过官方 HTTP
完整回读设置，仅关闭 `restartXrayOnClientDisable`，并在实验后核对和恢复原设置。测试程序
另起一个私有 Xray **客户端**作为数据面仪器，不调用 Xray 管理 API、不管理官方服务进程。

两个独立账号各保持一条 HTTP 响应流。每个 64 KiB 数据块都必须由读端逐字节收到；目标
账号停用、保留流量记录删除后，分别验证既存连接与对照连接，以及新目标连接拒绝、新对照
连接成功。统计只以 `inbounds/list` 中的本机原始计数为依据，另一路诊断接口不作账务回退。

运行器的 PASS 表示这一有界实验完成，不表示尾部字节已经完整结算。基线前缺口、全部配置
字段保真、任意坏配置的原子性和 Agent→Core 集成都不在此结果内。不会通过稳定计数或
近期在线缓存推断连接已结束，也不会清空账务行；实验容器停止后连同数据卷及记录保留。
最终真实结果见 [尾部统计验证记录](validation-tail-accounting-20260930.md)。
