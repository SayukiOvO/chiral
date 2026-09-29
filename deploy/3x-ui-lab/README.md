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

由于整个项目没有主机端口，不能直接访问宿主机的 `127.0.0.1:2053` 或 `:26080`。
验证程序通过 `docker exec` 或宿主机 root 使用 `nsenter` 进入实验网络命名空间后访问 API。
仅进入网络命名空间不授予测试服务访问宿主机目录的权限；验证程序仍须只读写实验资源。

验收应同时检查：

1. 容器实际没有宿主机端口映射、生产挂载、外部卷或可用默认路由。
2. 实验 Core 健康检查成功，实验 Agent 已注册并持续上报心跳。
3. Core 返回的运行时状态为 `SHADOW`，包含实际 3x-ui 版本、OpenAPI 摘要和能力检查结果。
4. 实验观测令牌失效后状态变为 `INCOMPATIBLE`；恢复令牌后重新进入 `SHADOW / READY`。
5. 实验前后生产容器未重建、未重启，生产数据库和配置未被实验程序写入；生产健康检查持续正常。

必须记录检查的成功信号和命令退出状态。实验无互联网出站，不能据此宣称真实公网代理、
内核下载或完整迁移等价性已经通过。16 项能力来自实时 OpenAPI 声明匹配，不代表已经执行
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
