# 开发

单模块 monorepo，Go 1.25+ / Node 22+。

```bash
go build -o bin/chiral-core  ./core/cmd/core
go build -o bin/chiral-agent ./agent/cmd/agent
go test ./...

cd web && npm ci && npm run dev      # 门户 / ，控制台 /admin/
```

前端由 `//go:embed` 编进 Core 二进制（见 `web/embed.go`）。`web/dist/` 里提交了一个
占位文件，所以没跑过 `npm run build` 的 checkout 也能 `go build ./...`——代价是那样
编出来的二进制没有界面，只在启动日志里说明。开发时设 `CHIRAL_WEB_DIR` 指向 vite 的
产物，或者干脆用 vite 自己的开发服务器。

部分测试需要一个真实的 xray 二进制（`CHIRAL_XRAY_BIN` 或 `PATH` 里的 `xray`）：密钥
派生与内核行为的断言都跑真内核，缺失时自动跳过。这不是可有可无的——`xray -test`
查不出配错的密钥对，只会在运行时静默握手失败，所以生成器必须与二进制交叉验证。

## 结构

```
core/       Panel 后端（"core" 专指它，不是 Xray-core）
agent/      节点守护进程
web/        两个 Vite 入口：门户 src/portal/、控制台 src/pages/
proto/      Core ↔ Agent 的 gRPC 定义，两侧共享
deploy/     安装脚本、systemd 单元、Docker
docs/       设计文档
```

各子目录下有自己的 `README.md` 说明职责。项目约定见根目录 [`CLAUDE.md`](../CLAUDE.md)。

## 数据库

SQLite（WAL），迁移在 `core/migrations/`，启动时自动应用，只增不改。

## 设计文档

| | |
|---|---|
| [架构总览](architecture.md) | 组件、职责、数据流 |
| [Core ↔ Agent 通信](communication.md) | gRPC 帧、重连、协议决策 |
| [模板与变量系统](template-system.md) | 渲染、作用域、生成器、`xray -test` 的能力边界 |
| [外部节点](external-nodes.md) | 别人的订阅、格式识别、链式代理 |
| [中转线路](node-relays.md) | 自有节点经自有节点、机器凭证、权限与计费 |
| [受限目的地](restricted-destinations.md) | 按用户限制目的网段（DN42）、白名单方向、入口继承 |
| [分流规则](routing-rules.md) | ACL4SSR 预设、解析、空组丢弃、provider 下发 |
| [用户 / 流量 / 订阅](user-management.md) | 凭证隔离、配额、订阅渲染 |
| [端用户门户](user-portal.md) | 第二个信任域、在线地址记录 |
| [内核在线升级](xray-upgrade.md) | 金丝雀、三值判定、回滚、中继 |
| [部署](deployment.md) | 环境变量全表 |
| [里程碑与验收记录](roadmap.md) | 做过什么、在真机上验过什么 |

## 发布

打一个 `v*` 标签即可：

```bash
git tag -a v0.3.0 -m "说明"
git push origin v0.3.0
```

`.github/workflows/publish.yml` 会先跑 `go vet` 与 `go test`（不过就不发），解析一次
Xray 版本（两个镜像因此保证打包同一个内核），构建双架构镜像推到 Docker Hub，并把各
平台二进制打包挂到 GitHub Release 上。

手动触发（Actions → publish → Run workflow）不会移动 `latest`，只发 `sha-` 标签，
可用来试跑。
