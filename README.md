# Chiral

自托管的 Xray 管理面板。一台中心面板管理多台节点：下发配置、管理用户与流量、生成多客户端订阅，并提供订阅者自助门户。

*A self-hosted Xray management panel — template-driven config delivery, per-user credentials, multi-client subscriptions, and a subscriber portal.*

[![core](https://img.shields.io/docker/v/moonwx/chiral-core?label=chiral-core&sort=semver)](https://hub.docker.com/r/moonwx/chiral-core)
[![agent](https://img.shields.io/docker/v/moonwx/chiral-agent?label=chiral-agent&sort=semver)](https://hub.docker.com/r/moonwx/chiral-agent)

---

## 安装

在面板机器上跑一条命令，回答一个问题（你的域名）：

```bash
curl -fsSL https://raw.githubusercontent.com/SayukiOvO/chiral/main/deploy/install.sh | sudo sh
```

它会下载二进制、装好 Xray、生成密钥、写好 systemd 服务并启动，最后打印管理员密码。**不新建系统用户，不需要手动建目录。**

装完在面板前面放一个反向代理，`https://` 与证书由它负责：

```
你的域名 {
    reverse_proxy 127.0.0.1:8080
}
你的域名:8443 {
    reverse_proxy h2c://127.0.0.1:8443
}
```

这是 Caddy 的写法，证书它自己签。nginx 的写法见[部署文档](docs/deployment.md)。

然后打开 `https://你的域名/admin/` 登录。

### 加节点

控制台点「新增节点」，它给你一条命令，在节点机器上跑：

```bash
curl -fsSL .../install.sh | sudo sh -s -- --agent --panel-url ... --token ...
```

节点会自动注册、接管本机 Xray，几秒后出现在控制台里。

### 用 Docker

```bash
git clone https://github.com/SayukiOvO/chiral && cd chiral/deploy/panel
cp .env.example .env      # 按注释填
docker compose up -d
```

镜像：[`moonwx/chiral-core`](https://hub.docker.com/r/moonwx/chiral-core) 与 [`moonwx/chiral-agent`](https://hub.docker.com/r/moonwx/chiral-agent)，amd64 / arm64。

## 特点

**配置用模板，不用表单。** 节点的 inbound 由你自己写的模板渲染，模板里用 `{{变量}}` 引用密钥、端口、域名。想用什么特性就写什么——xhttp 上下行分离、后量子 REALITY 都可以，不受订阅转换器表达力的限制。

**每个用户在每个接入点上都有独立凭证。** 粒度是「用户 × 接入配置 × 节点」，一条凭证泄露只影响一个接入点。

**配置下发前先校验。** 渲染出的 config 要通过 `xray -test` 才会存版本、才会下发，而且是用目标节点自己那份内核校验。配置历史保留 20 版，可一键回滚。

**内核可在线升级，追 Xray 的 prerelease。** 先升一台，让它**真的当一次客户端上网**——字节回来了才算成功；起不来、或者本来通现在不通，就自动回滚。你看过结果再决定放行到全队。

**面板是单个二进制。** 界面编译在里面，没有静态目录要部署，也不会出现二进制更新了界面没更新。

**订阅者有自己的门户。** 注册登录、查看订阅链接与用量、了解线路状态。运维信息（内部节点名、公网 IP、配置内容）不会展示给他们。

**私钥静态加密。** REALITY 私钥、后量子种子、订阅令牌、来源地址都以 AES-GCM 存储。

界面中英双语，日 / 夜 / 跟随系统，移动端自适应。

## 功能

| | |
|---|---|
| 节点 | 实时状态与流量折线、资源历史、配置版本与回滚、掉线告警（Telegram / webhook） |
| 接入配置 | 服务端 inbound 模板 + 每用户凭证模板 + 各客户端模板，编辑器带变量高亮与校验 |
| 变量 | 全局 / 接入配置 / 节点 三级作用域；一键生成 REALITY 密钥、UUID、短 ID、后量子密钥 |
| 用户 | 流量配额与按周期重置、到期与自动续期、在线增删（不重启内核）、并发地址提示 |
| 订阅 | 一条链接，按客户端自动返回 xray-json / Clash / Stash / 分享链接 |
| 门户 | 开放注册，或由管理员发放一次性认领链接 |
| 管理 | 多管理员三级权限、审计日志、登录 MFA（TOTP / Passkey / 邮箱验证码 / 恢复码） |
| 内核 | 从 GitHub 发现新版本（含 prerelease）、校验、金丝雀升级、失败自动回滚 |

## 架构

```
              ┌────────── 面板（一台） ──────────┐
              │  门户 /        控制台 /admin/     │
              │  Core：API · 模板渲染 · SQLite   │
              └─────▲──────────────────▲─────────┘
   gRPC over TLS    │                  │
             ┌──────┴───────┐   ┌──────┴───────┐
             │ 节点 A        │   │ 节点 B        │
             │ Agent + Xray │   │ Agent + Xray │
             └──────────────┘   └──────────────┘
```

节点**主动连接**面板，所以节点只需要能出网，不必额外暴露入站端口，也能待在 NAT 后面。

## 使用须知

- **超出配额不会掐断正在进行的连接**，只拒绝新连接。这是 Xray 内核层面的限制。
- **「并发地址」是提示，不是限制**，也不会自动断线。它计的是不同来源 IP：一家人在同一个 NAT 后面算 1 个，一部手机在 WiFi 和蜂窝之间切换算 2 个。
- **`CHIRAL_SECRET_KEY` 一定要备份。** 它解密所有私钥、订阅令牌与来源地址，丢了就都拿不回来。安装脚本会生成它，写在 `/etc/chiral/core.env`。
- **数据库是单个 SQLite 文件**（`/var/lib/chiral/chiral.db`），备份时连同上面那个密钥一起备。
- 订阅者门户不提供 MFA。

## 文档

- [部署](docs/deployment.md) — 二进制与 Docker、反向代理、环境变量全表
- [模板与变量](docs/template-system.md) — 怎么写接入配置
- [用户与订阅](docs/user-management.md) — 凭证、配额、订阅格式
- [订阅者门户](docs/user-portal.md)
- [内核在线升级](docs/xray-upgrade.md)
- [参与开发](docs/development.md)

## 状态

当前 v0.2.x。功能完整，完整部署、TLS、端到端代理与内核升级都在真实环境验证过。

版本号是 0.x 而不是 1.0，意思是接口与数据库结构仍可能变动。生产使用请自行评估，升级前看一眼 release 说明。

## 许可

尚未选定许可证。在此之前，默认保留所有权利。
