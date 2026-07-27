# core/internal — 后端内部包

按职责拆分，遵循 Go `internal` 不对外导出规则。Core 与 Agent **不互相 import 对方 internal**，共享只通过 `proto/`。

| 包 | 职责 |
|----|------|
| `api/` | HTTP handler：路由、请求校验、鉴权中间件、未鉴权路由的限流、可选的前端静态文件伺服 |
| `auth/` | 与 Agent 共享的秘密（join token、每节点长期凭证，库里只存 SHA-256）；管理员密码、会话、RBAC（superadmin / operator / viewer）、TOTP 与恢复码 |
| `passkey/` | 管理员的 WebAuthn 注册与断言。协议本身委托给 `go-webauthn`，不自己实现——手写版会「看起来能用」却其实什么都没验证 |
| `mail/` | 事务邮件（地址验证、一次性登录码）。没配 SMTP 就是「不提供邮件因子」，是合法部署 |
| `node/` | 节点注册表；与各 Agent 的 gRPC 长连接管理（在线判定、断线处理）；配置推送；心跳与指标接收入库 |
| `online/` | 「此刻谁从哪些地址连着」——纯内存：它是关于当下的问题，几秒后就不对了。持久的那份（曾经用过哪些地址）在 `store.user_devices` |
| `alert/` | 节点可用性变化的去抖与投递。抖动的链路若每次心跳都发一条，就把人训练成忽略这个频道 |
| `template/` | 模板引擎、变量生成器、config 装配、`xray -test` 封装（**纯逻辑，不碰 store**） |
| `profile/` | 编排层：变量池 → 渲染 → 装配 → 校验 → 下发；配额 / 到期 / 续期的对账（`enforce.go`）也在这里 |
| `secret/` | 私钥类变量、渲染后的节点 config、config 骨架的静态加密（AES-GCM，密钥来自 `CHIRAL_SECRET_KEY`） |
| `user/` | 用户、**强隔离**凭证（`用户 × Profile × 节点` 各自独立）、配额 / 到期字段，以及「为什么这个账号不该能用」的唯一实现（`user.Reason`） |
| `subscription/` | 多客户端订阅渲染（`/sub/{token}`） |
| `portal/` | 端用户视角。`Identity` **不带 Role**，`View` 只答关于自己的问题，且**拿不到 `*store.Store`** |
| `store/` | SQLite（WAL）持久化，唯一碰数据库的地方；指标采样粒度与各类保留期也定在这里 |

## 关键决策

端用户与管理员是两类主体，边界靠构造而非小心：门户 handler 多带一个 `portal.Identity` 参数，管理员 handler 从 context 读 `auth.Identity`，两种签名不通用，所以接错守卫是编译错误。但签名只挡得住「挂错守卫」，挡不住一个守卫正确的 handler 去 `ListNodes()` 把整个机队序列化出去——所以 `portal` 拿不到 store，越权取数同样是编译错误。
