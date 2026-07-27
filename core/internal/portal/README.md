# portal — 端用户侧的作用域视图

买了订阅的人和运维机群的人是**两类主体**。本包提供 `Identity`（已登录的端用户）和 `View`（只回答「这一个用户」的问题）。门户展示的一切机队信息都必须经由 `View`。

## 关键决策

- **`Identity` 不带 Role，也从不进 request context**。管理端 handler 是 `func(w, r)` 并从 context 取 `auth.Identity`，门户 handler 多一个 `portal.Identity` 参数——两种签名不能互换，所以 `requireAdmin(portalMe)` 和 `requireUser(listNodes)` 都是**编译错误**，而不是要靠 review 发现的越权。不进 context 的作用要说准：`rank` 检查在 `require()` 中间件里而不在 handler 体内，所以它拦不住一个绕过中间件的调用；它保证的是不会出现「两处各自声明 `ctxKey`、同一个 key 下的值被读成管理员身份」这类事故，以及审计不会把动作署到一个它其实不认识的身份上。
- **本包够不到 `*store.Store`**。签名那招只挡「挂错守卫」，挡不住「守卫正确、但 handler 自己 `ListNodes()` 把整个机群序列化出去」。`Data` 接口把允许用的方法逐条列出，`View` 的每个方法都已被构造它的 identity 限定，没有任何一个接受「另一个用户的 id」——**在这个包里越权取数是编译错误**。
  边界要说清楚：`package api` 里的门户 handler 是 `*Server` 的方法，仍持有 `s.st`（`portalLogin`、`portalChangePassword` 就在用它读写自己的账号行），在那里写 `s.st.ListNodes()` 能编译过。所以规矩是：**凡是要展示机队信息，一律走 `View`**。
- **一旦有人给 `auth.rank()` 加上端用户等级，上面两条全部失效**——`requireAdmin`（= viewer 档）覆盖的节点列表、用户列表、变量、Profile、流量与审计日志会一起对客户开放。`config/preview` 挂的是 `requireWrite`，不在这一档。
- **对外结构体是手写白名单，不是从管理端结构体删字段**。两个方向的失败不对称：黑名单漏改会静默把新字段发给所有客户，白名单漏改只是 UI 上空一块。`Node` 刻意不含内部 name / 地址 / 版本号 / 负载 / `last_seen_at`，逐条理由写在 `nodes.go` 里。
- **门户发链接，不发配置**：订阅内容仍由 `/sub/{token}` 渲染。`Nodes()` 走的是和 `subscription.Render` 同一条路径（profile → 节点 → 凭证），所以门户显示的和订阅里实际有的不会打架；凭证还没生成时给 `provisioning` 这个诚实的第三态，而不是一个红点。
- **`Devices()` 在共享账号上不只是「我的」**：拿同一套凭证连出去的人也会出现在这里，所以 UI 必须说「地址」而不是「你的设备」。
