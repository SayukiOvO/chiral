# Chiral

自托管的 Xray 管理面板。一台面板集中管理多台节点：下发配置、管理用户与流量、生成多客户端订阅，并为订阅者提供自助门户。

*A self-hosted Xray management panel: template-driven config delivery, per-user credentials, multi-client subscriptions, and a subscriber portal.*

[![core](https://img.shields.io/docker/v/moonwx/chiral-core?label=chiral-core&sort=semver)](https://hub.docker.com/r/moonwx/chiral-core)
[![agent](https://img.shields.io/docker/v/moonwx/chiral-agent?label=chiral-agent&sort=semver)](https://hub.docker.com/r/moonwx/chiral-agent)

---

## 安装

### 面板

```bash
curl -fsSL https://raw.githubusercontent.com/SayukiOvO/chiral/main/deploy/install.sh | sudo sh
```

安装程序询问域名与 TLS 方式，随后下载二进制、安装 Xray、生成密钥、写入 systemd 单元并启动，最后输出首个管理员密码。不创建系统用户，不需要手动创建目录。

TLS 两种方式任选：

- **面板自行提供 HTTPS** — 指定证书与私钥路径即可，无需反向代理。
- **反向代理** — 面板在 loopback 上明文监听，由 nginx 或 Caddy 终结 TLS。

两种方式的配置见[部署文档](docs/deployment.md#tls)。本项目不内置 ACME，证书来源不限。

安装程序会输出控制台地址与管理员密码。控制台路径为 `/admin/`。

### 节点

控制台的「新增节点」会生成一条含令牌的命令，在节点主机执行：

```bash
curl -fsSL .../install.sh | sudo sh -s -- --agent --panel-url ... --token ...
```

节点自动注册并接管本机 Xray。

### Docker

```bash
git clone https://github.com/SayukiOvO/chiral && cd chiral/deploy/panel
cp .env.example .env
docker compose up -d
```

镜像 [`moonwx/chiral-core`](https://hub.docker.com/r/moonwx/chiral-core) 与 [`moonwx/chiral-agent`](https://hub.docker.com/r/moonwx/chiral-agent)，提供 amd64 与 arm64。

## 设计

**配置基于模板而非表单。** 节点的 inbound 由用户编写的模板渲染，通过 `{{变量}}` 引用密钥、端口与域名。Xray 支持的任何特性均可直接书写，包括 xhttp 上下行分离与后量子 REALITY，不受订阅转换器表达能力的限制。

**凭证按接入点隔离。** 每个用户在每个接入点上持有独立的 UUID 或密码，粒度为「用户 × 接入配置 × 节点」。单条凭证泄露仅影响一个接入点。

**配置下发前校验。** 渲染结果须通过 `xray -test` 方可存为版本并下发，且使用目标节点自身的内核版本校验。配置历史保留 20 版，支持回滚。

**内核支持在线升级，跟随 Xray 的 prerelease 通道。** 升级先在单台节点执行，由该节点以客户端身份实际建立连接并取回数据，确认链路可用后才判定成功；启动失败或链路由通变为不通则自动回滚。放行至其余节点需人工确认。

**面板为单个二进制。** 前端编译在内，无需单独部署静态资源目录。

**订阅者门户独立于控制台。** 订阅者可注册、查看订阅链接与用量、了解线路状态；节点内部名称、公网地址与配置内容不对其暴露。

**敏感数据静态加密。** REALITY 私钥、后量子种子、订阅令牌与来源地址均以 AES-GCM 存储。

界面支持中英双语、明暗主题与移动端布局。

## 功能

| | |
|---|---|
| 节点 | 实时状态与流量图、资源历史、配置版本与回滚、离线告警（Telegram / webhook） |
| 接入配置 | 服务端 inbound 模板、每用户凭证模板、各客户端模板；编辑器提供变量高亮与校验 |
| 变量 | 全局 / 接入配置 / 节点 三级作用域；内置 REALITY 密钥、UUID、短 ID 与后量子密钥生成 |
| 用户 | 流量配额与周期重置、到期与自动续期、在线增删用户、并发地址统计 |
| 订阅 | 单一链接，按客户端返回 xray-json、Clash、Stash 或分享链接 |
| 门户 | 开放注册，或由管理员发放一次性认领链接 |
| 管理 | 多管理员与三级权限、审计日志、登录多因素认证（TOTP / Passkey / 邮箱验证码 / 恢复码） |
| 内核 | 版本发现（含 prerelease）、校验、金丝雀升级、失败回滚 |

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

节点主动连接面板，因此节点无需暴露额外入站端口，可位于 NAT 之后。

## 使用限制

- 流量超额不会中断已建立的连接，仅拒绝新连接。此为 Xray 内核限制。
- 并发地址为统计与提示，不作为限制条件，不会触发断线。统计单位为不同来源地址：同一 NAT 后的多台设备计为一个，同一设备在不同网络间切换计为多个。
- `CHIRAL_SECRET_KEY` 用于解密全部私钥、订阅令牌与来源地址，须与数据库一并备份。安装程序生成该值并写入 `/etc/chiral/core.env`。
- 数据库为单个 SQLite 文件，默认位于 `/var/lib/chiral/chiral.db`。
- 订阅者门户不提供多因素认证。

## 文档

- [部署](docs/deployment.md) — 二进制与 Docker、TLS、环境变量
- [模板与变量](docs/template-system.md)
- [用户与订阅](docs/user-management.md)
- [订阅者门户](docs/user-portal.md)
- [内核在线升级](docs/xray-upgrade.md)
- [参与开发](docs/development.md)

## 状态

当前 v0.3.x。完整部署、TLS、端到端代理与内核升级均已在真实环境验证。

主版本号为 0 表示接口与数据库结构仍可能变更，升级前请查阅对应 release 说明。

## 许可

尚未选定许可证，在此之前保留所有权利。
