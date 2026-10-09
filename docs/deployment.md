# 部署

两种形态，同一套配置变量：

- **二进制**：`chiral-core` 与 `chiral-agent` 两个静态可执行文件，各自旁边一个
  Xray-core。**前端编译在 Core 二进制里**，没有静态目录。见下面「二进制部署」。
- **Docker / docker-compose**：见「Panel 侧」与「节点侧」。

分两侧：Panel（中心）与节点。

## Panel 侧

单个 `docker-compose.yml`（见 [`../deploy/panel/`](../deploy/panel/)），包含：
- `core` 容器：Go 后端，挂载 SQLite 数据卷。
- 前端：**编译在 core 二进制内**，无需部署静态目录。**门户在 `/`，运维控制台在 `/admin/`**。设 `CHIRAL_WEB_DIR` 可改为从磁盘目录伺服（开发时指向 vite 产物，或让 nginx / Caddy 接管静态内容）。
- 可选 `caddy` / `nginx`：TLS 终止（建议 ACME 自动签发证书）。用反代时**必须**设 `CHIRAL_TRUSTED_PROXY`，见下。

关键环境变量：

| 变量 | 说明 |
|---|---|
| `CHIRAL_ADMIN_TOKEN` | 破窗用的 bearer token（必需）。与账号密码并行，密码全丢了也还能进 |
| `CHIRAL_ADMIN_USER` / `CHIRAL_ADMIN_PASSWORD` | **首个管理员账号**。仅在库里一个管理员都没有时生效。用户名默认 `admin`；**密码留空则随机生成并在启动日志里打印一次**，之后再也拿不到——生产部署应显式设置 |
| `CHIRAL_SECRET_KEY` | 静态加密密钥；**不设则私钥类数据明文入库**（启动告警）。开启门户时它是**硬性要求**，见下。`openssl rand -base64 32` |
| `CHIRAL_GRPC_PUBLIC_ADDR` | Agent 拨回的 `host:port`。**可选**，默认取 `CHIRAL_PUBLIC_URL` 的主机名 + gRPC 监听端口；只在 agent 需要拨到别处时设置 |
| `CHIRAL_AGENT_IMAGE` | 「新增节点」生成的 compose 片段里写哪个镜像。留空用内置默认值。发布到自己的 registry 时必须设，否则运维粘贴的命令指向一个不存在的镜像 |
| `CHIRAL_XRAY_BIN` | 面板侧 Xray 二进制（镜像内已打包）。下发前 `xray -test` 校验、ML-DSA-65 生成都靠它 |
| `CHIRAL_DB_PATH` / `CHIRAL_HTTP_LISTEN` / `CHIRAL_GRPC_LISTEN` | 路径与监听地址 |
| `CHIRAL_TLS_CERT` / `CHIRAL_TLS_KEY` | 设置后，HTTP 与 gRPC **两个监听**都提供 TLS；留空则都是明文，由前面的反代终结。两者必须同时设置 |
| `CHIRAL_TRUSTED_PROXY` | **有反代时必需**。逗号分隔的地址或 CIDR，只写 core 前面那一层。不设则限流按对端地址计（反代后就是所有人共用一个桶）；设错则任何调用方一个 `X-Forwarded-For` 就能自选桶，限流形同虚设 |
| `CHIRAL_WEB_DIR` | 前端构建产物目录（镜像内已设）。留空则不伺服静态文件 |
| `CHIRAL_PORTAL_MODE` | 端用户门户：`off`（默认）/ `closed`（仅登录，账号靠认领链接发放）/ `open`（开放注册） |
| `CHIRAL_PORTAL_INVITE_CODE` | 开放注册时的共享注册码；留空则任何人都能注册 |
| `CHIRAL_ONLINE_RECORD` | 记录每个用户的来源地址，默认 `off`。打开会给每个节点注入 `statsUserOnline`，即一次配置版本变更 + 一次 Xray 重启（该节点上的活连接会断一次） |
| `CHIRAL_SMTP_HOST` / `_PORT` / `_USER` / `_PASSWORD` / `_FROM` / `_TLS` | 邮箱验证与登录验证码。`HOST` 留空即不提供邮件因素 |

> **Panel 镜像里也带 Xray 二进制**：不是用来跑代理，而是用来在下发前校验渲染出的 config，以及派生 Go 标准库没有的后量子密钥。

> **`CHIRAL_PORTAL_MODE != off` 时 `CHIRAL_SECRET_KEY` 是硬性要求，缺了 core 会拒绝启动**（不是告警）。门户要能把订阅链接展示给用户，所以订阅 token 是可恢复存储的；那是一条公网可用的 bearer URL，明文入库意味着一份被拖走的 `chiral.db` 直接产出全部用户的可用链接。

### 轮换 `CHIRAL_SECRET_KEY`

密钥泄露、或只是想定期换，都走同一条路。**面板必须停机**——轮换要独占数据库，它会在一个事务里重写每一条密文。

```bash
docker compose down
cp /var/lib/chiral/chiral.db /var/lib/chiral/chiral.db.bak   # 先备份
CHIRAL_SECRET_KEY=<当前密钥> \
CHIRAL_SECRET_KEY_NEW=<新密钥> \
  docker compose run --rm core chiral-core -rotate-secret-key
# 把 .env 里的 CHIRAL_SECRET_KEY 改成新密钥
docker compose up -d
```

覆盖八处密文：变量私钥分量、渲染后的节点 config、config 骨架、用户凭证、告警目标配置、MFA 密钥、来源地址、订阅 token。**全在一个事务里**——半轮换的数据库比任何一端都糟，那会导致没有任何一个 `CHIRAL_SECRET_KEY` 能把面板启起来。

跑第二遍是安全的（已是新密钥的值会被跳过）。轮换到空密钥会被拒绝。

## 节点侧

不手写 compose——在 Panel「新增节点」时，Core **自动生成**一段 `docker-compose.yml`（模板见 [`../deploy/agent/`](../deploy/agent/)）+ 一次性 join token，用户复制到节点机器执行：

```bash
docker compose up -d
```

Agent 容器启动后：用 `JOIN_TOKEN` 向 `PANEL_URL` 注册 → 换取长期凭证 → 建立到 Core 的长连接 → 拉起并管理本机 Xray-core。

生成的 compose 通过环境变量注入：`PANEL_URL`、`JOIN_TOKEN`（一次性）。Agent 与 Xray-core 可同容器或同 pod；config.json 走数据卷。

M8 第一阶段可选的 3x-ui 只读观测不写进默认生成文件，避免未完成的 provider 被误当成
生产后端。测试节点可在生成的 compose 上叠加
[`docker-compose.3x-ui-shadow.yml`](../deploy/agent/docker-compose.3x-ui-shadow.yml)，并把不授予
group/other 权限（通常为 `0400`/`0600`）的 admin token 文件只读挂载给 Agent。3x-ui 应在同一节点通过回环地址访问；
若必须使用非回环地址，Agent 要求 HTTPS 且需显式设置 `CHIRAL_3XUI_ALLOW_PUBLIC=1`。
`3x-ui-shadow` 只读取状态、OpenAPI 和最终 config，所有写操作仍由 direct-Xray 执行。完整命令
见 [`deploy/agent/README.md`](../deploy/agent/README.md#可选3x-ui-shadow-观测)。

## 加入流程时序

```
Panel: 新增节点 → 签发 token + 生成 compose
用户:  复制 compose 到节点 → docker compose up -d
Agent: 用 token 注册 → 得长期凭证（token 作废）→ 建长连接
Core:  推送首份 config → Agent 落盘 + xray -test + 拉起 Xray-core → ack
```

## 待办

- [x] 起草 `deploy/panel/docker-compose.yml`
- [x] 起草 `deploy/agent/docker-compose.yml.tmpl`（Core 渲染用）
- [x] Dockerfile（core / agent；core 镜像含 node 构建阶段，前端两个入口一并打包）
- [x] `.env` 示例（[`../deploy/panel/.env.example`](../deploy/panel/.env.example)）
- [x] Xray-core 二进制：随镜像打包（`--build-arg XRAY_VERSION` 可 pin），运行时在线升级见 [`xray-upgrade.md`](xray-upgrade.md)
- [x] 二进制部署：systemd 单元与配置样例在 [`../deploy/systemd/`](../deploy/systemd/)

## 二进制部署

### 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/SayukiOvO/chiral/main/deploy/install.sh | sudo sh
```

问你装面板还是装节点、面板的域名，其余自动：下载对应平台的 release、装 Xray 与 geo
资源、生成 `CHIRAL_ADMIN_TOKEN` 与 `CHIRAL_SECRET_KEY`、写 `/etc/chiral/core.env`、
装 systemd 单元并启动，最后打印首个管理员密码。

节点侧由控制台「新增节点」给出带令牌的完整命令，无需交互：

```bash
curl -fsSL .../install.sh | sudo sh -s -- --agent --panel-url panel.example.com:26443 --token <令牌>
```

**不建系统用户，不用手动建目录。** 面板服务用 systemd 的 `DynamicUser=yes` 跑在一个
临时 UID 上，`StateDirectory=chiral` 负责创建并保管 `/var/lib/chiral`——重启后目录
仍在，UID 换了也会重新授权。Agent 以 root 运行，因为它要监管绑 443 的 Xray；给
xray 二进制加 `CAP_NET_BIND_SERVICE` 是替代方案，但内核升级装了新二进制之后那个能力
不会跟过去，静默失效比明摆着以 root 跑更糟。

### 端口

默认 HTTP `127.0.0.1:26080`、gRPC `:26443`，两个都不是常用端口，这是有意的：面板与
其他服务共存于同一台主机，而 443、80、8080、8443 在典型机器上通常已被 Web 服务器或
Xray 本身占用。默认值撞上它们，结果是首次启动时在别人的生产服务上报 bind 错误。两个
端口都低于 32768，不会与出站连接的源端口冲突。

改端口用 `CHIRAL_HTTP_LISTEN` 与 `CHIRAL_GRPC_LISTEN`。安装脚本在写配置前会检查这两个
端口是否已被占用。

HTTP 监听默认绑在 loopback 上，因为常规部署由反向代理对外；面板自行提供 HTTPS 时改为
`:<端口>`。gRPC 监听必须对节点可达。

### TLS

两种形态，选一种。区别只在证书由谁持有。

**A. 面板自己提供 HTTPS**

一对证书同时用于 HTTP 与 gRPC 两个监听，两者必须同时设置。证书从哪来不限（Let's
Encrypt、商业 CA、企业内部 CA 均可），本项目不内置 ACME；续期后重启服务即可。

`DynamicUser` 下的动态 UID 读不了 root 权限的私钥。私钥不必因此放宽权限——由 systemd
把它作为凭据递进来即可。安装脚本会写好这个 drop-in，手动配置时对应内容为：

```ini
# /etc/systemd/system/chiral-core.service.d/tls.conf
[Service]
LoadCredential=tls-cert:/etc/chiral/tls/fullchain.pem
LoadCredential=tls-key:/etc/chiral/tls/privkey.pem
```

`core.env` 随之指向 systemd 提供的凭据路径，而不是证书的原始位置：

```
CHIRAL_TLS_CERT=%d/tls-cert
CHIRAL_TLS_KEY=%d/tls-key
CHIRAL_HTTP_LISTEN=:26080
CHIRAL_GRPC_LISTEN=:26443
```

`%d` 是 systemd 的凭据目录说明符。systemd 只在单元指令里展开说明符，`EnvironmentFile`
的内容原样传递，因此这里的展开由面板自己完成（依据 `$CREDENTIALS_DIRECTORY`）——写
绝对路径同样可用，只是私钥得让动态 UID 读得到。

监听端口低于 1024 时另需一行 `AmbientCapabilities=CAP_NET_BIND_SERVICE`。默认的
26080 / 26443 不需要，安装脚本也只在你指定了低端口时才写这行。

**B. 反向代理终结 TLS**

`core.env` 里不写任何证书，两个监听留在 loopback 明文：

```
CHIRAL_HTTP_LISTEN=127.0.0.1:26080
CHIRAL_GRPC_LISTEN=127.0.0.1:26443
CHIRAL_TRUSTED_PROXY=127.0.0.1
```

Caddy：

```
panel.example.com {
    reverse_proxy 127.0.0.1:26080
}
panel.example.com:26443 {
    reverse_proxy h2c://127.0.0.1:26443
}
```

`h2c://` 不可省略。gRPC 走 HTTP/2，而后端是明文；缺了它 Caddy 会以 HTTP/1.1 连接
后端，症状是节点始终连不上，而两侧日志都没有明显错误。

nginx：

```nginx
server {
    listen 443 ssl;
    server_name panel.example.com;
    ssl_certificate     /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;
    location / { proxy_pass http://127.0.0.1:26080; }
}
server {
    listen 26443 ssl http2;
    server_name panel.example.com;
    ssl_certificate     /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;
    location / { grpc_pass grpc://127.0.0.1:26443; }
}
```

用反代时必须设 `CHIRAL_TRUSTED_PROXY`，否则限流会把所有请求归到反代那一个地址上；
设成过宽的范围则任何调用方都能用一个 `X-Forwarded-For` 自选桶位。

**两种形态下 agent 都使用 TLS**，因为它由 `CHIRAL_PUBLIC_URL` 的 scheme 决定，与
证书由谁持有无关。只有把该值写成 `http://` 时 agent 才明文连接，这仅适用于开发环境。

### 手动安装

安装脚本做的事就是下面这些，想自己控制每一步的话：

```bash
# 从 Releases 下载对应平台的压缩包
tar xzf chiral-v0.2.0-linux-amd64.tar.gz && cd chiral-v0.2.0-linux-amd64

sudo install -m755 chiral-core /usr/local/bin/
sudo install -m644 chiral-core.service /etc/systemd/system/
sudo install -D -m600 core.env.example /etc/chiral/core.env   # 编辑它
sudo systemctl daemon-reload && sudo systemctl enable --now chiral-core
```

节点侧把 `core` 换成 `agent` 即可。`/var/lib/chiral` 不用建，systemd 会处理。

两侧都还需要一个 Xray-core：

```bash
VER=$(curl -fsSL "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=1" | jq -r '.[0].tag_name')
curl -fsSL -o /tmp/xray.zip "https://github.com/XTLS/Xray-core/releases/download/${VER}/Xray-linux-64.zip"
unzip -o /tmp/xray.zip -d /tmp/xray
sudo install -m755 /tmp/xray/xray /usr/local/bin/xray
sudo install -D -m644 -t /usr/local/share/xray /tmp/xray/geoip.dat /tmp/xray/geosite.dat
```

geo 资源不能省：用到 `geosite:` / `geoip:` 的路由规则少了它们会加载失败，面板会把
完全合法的 config 判成不合法。

节点侧这一份只是**起点**——内核在线升级会把新版本装到
`CHIRAL_STATE_DIR/kernels/<版本>/` 并切过去，这一份是回滚的地板。

## 发布镜像

`.github/workflows/publish.yml` 把 `chiral-core` 与 `chiral-agent` 推到 Docker Hub，
`linux/amd64` + `linux/arm64` 双架构。

**触发方式**：推送 `v*` 标签（发正式版，会移动 `latest`），或在 Actions 页面手动
运行（可选钉死 Xray 版本、可选附加一个标签，**不会**移动 `latest`）。

刻意不在每次推 main 时发布：镜像里烘焙的是构建时抓取的 Xray 快照，「每次提交都发」
等于为一堆碰都没碰过代理链路的改动，一天给订阅者换好几次内核。打标签是一个决定，
提交不是。

**仓库需要配置**（Settings → Secrets and variables → Actions）：

| 类型 | 名字 | 值 |
|---|---|---|
| Variable | `DOCKERHUB_USERNAME` | Docker Hub 用户名 / 组织名，同时用作镜像命名空间 |
| Secret | `DOCKERHUB_TOKEN` | Docker Hub **访问令牌**（Account Settings → Personal access tokens），权限 Read & Write。不要用账号密码 |

推出来的就是 `<DOCKERHUB_USERNAME>/chiral-core` 和 `<DOCKERHUB_USERNAME>/chiral-agent`。
上游发布在 `moonwx/` 下，这也是代码里的默认值——用上游镜像时什么都不用配。

**fork 或换 registry 的话，面板要设 `CHIRAL_AGENT_IMAGE`**，否则控制台「新增节点」
生成的 compose 片段指向的是上游镜像，而不是你自己那份。

### 跨平台构建

Dockerfile 里 Go / npm / 下载三个阶段都钉在 `$BUILDPLATFORM` 上交叉编译，只有最后
的运行阶段是目标架构。arm64 因此不需要在模拟器里跑编译器——否则一次构建从一分钟
变成十几分钟。

Xray 版本发现会调 GitHub API，匿名限额是每 IP 每小时 60 次、而 CI runner 共享出口
IP。workflow 把 `GITHUB_TOKEN` 作为 **build secret**（不是 ARG，ARG 会留在镜像历史里）
传进去抬高限额。
