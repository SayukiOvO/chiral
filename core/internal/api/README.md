# api — HTTP 接口层

Core 对外唯一的 HTTP 面，`Handler()` 一处注册四类路由：

| 面 | 路径 | 鉴权 |
| --- | --- | --- |
| 管理台 REST | `/api/*`（节点、Profile、变量、用户、管理员、MFA、审计、告警、流量） | Bearer 会话 token 或 `CHIRAL_ADMIN_TOKEN`，按角色分 `requireAdmin`(viewer) / `requireWrite`(operator) / `requireSuperadmin` |
| 订阅 | `GET /sub/{token}` | 只认路径里的 token（客户端没法登录） |
| 门户 | `/api/portal/*` | 三条要会话（`requireUser`），五条免鉴权（config / register / login / claim / claim-lookup，全部挂限流）；`CHIRAL_PORTAL_MODE=off` 时整棵树根本不注册 |
| 静态文件 | `/`（门户）、`/admin/`（管理台） | 无；`CHIRAL_WEB_DIR` 不设就一个文件都不伺服 |

**没有 WebSocket**（旧标题写过，实际从未实现，也没有对应依赖）。前端靠轮询刷新。

## 关键决策

- **凡是「在校验任何凭证之前就会作答」的路由，都必须挂 `throttle`**（ratelimit.go），按「路由名 + 客户端 IP」分桶。一次登录要跑 210k 轮 PBKDF2，而 SQLite handle 是 `MaxOpenConns(1)`，和 config 装配、流量入库、清扫共用——不限流就是任何人不用账号就能打的 CPU 与写锁 DoS。限流器是进程内的：Chiral 是单进程 + 单个 SQLite 文件，这个前提一旦不成立就得挪进数据库。
- **`X-Forwarded-For` 只在 `CHIRAL_TRUSTED_PROXY` 认得对端时才读**，且取最右一项（clientip.go）。无条件相信它等于让攻击者每个请求换一个桶，所有限流当场归零。
- **门户守卫的 handler 签名故意不与管理台统一。** `portalHandler` 多带一个 `portal.Identity` 参数，于是 `requireAdmin(s.portalMe)` 与 `requireUser(s.listNodes)` 都是编译错误。
  机队信息一律经 `portal.View` 取——`package portal` 够不到 store，在那里越权取数也是编译错误。但本包的门户 handler 是 `*Server` 的方法，仍持有 `s.st`（`portalLogin`、`portalChangePassword` 就在用），所以第二条是规矩不是类型。
- **`requireUser` 不往 context 里放任何东西**，于是不会出现「两处各自声明 `ctxKey`、同一个 key 下的值被读成管理员身份」这类事故。真正拦住越权的是中间件里的 rank 检查，以及两个中间件里各一次的路由前缀断言——注册错了是一条明显坏掉的路由，而不是一个安静的洞。
- **注册与登录不做存在性回答。** 门户注册无论地址是否已被占用都返回同一个 202，并在已存在的分支上照样花掉一次密码哈希的时间；否则面板就是个公开的客户邮箱枚举器。
- **在线信息按问题性质分守卫**：数量（`/online`）是运维问题，viewer 即可；具体地址（`/devices`）回答的是「这人住哪」，要 superadmin，且读一次写一条审计。
- **静态 catch-all 不会遮蔽 `/api/`**（Go 1.22 的 mux 最具体优先），但拼错的 `/api/nodez` 只匹配得上 `/`，会把 HTML 首页以 200 丢给等 JSON 的 fetch，所以额外注册了 `/api/` 兜底返回 JSON 404。没有 index.html 的目录一律 404，否则 `dist/assets` 就是一份可浏览的构建清单。
