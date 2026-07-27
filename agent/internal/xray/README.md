# xray — Xray-core 进程与管理 API

两块职责：**子进程**（config.json 落盘、`xray -test` 预校验、启 / 停 / 重启、崩溃事件回调、读内核版本）与**管理 API**（在线 `AddUser` / `RemoveUser`、流量计数、在线地址）。

管理 API 不走 Go gRPC 客户端，而是 fork 它自己监管的那个二进制（`xray api <sub> --server=...`）：线格式天然与运行中的内核同版本，也不必把 xray-core 拉成 Go 依赖。API 地址从**已应用的 config** 解析（api tag → routing 规则 → inbound 端口），不写死，运维自己写的 api inbound 换个端口也能用。

## 关键决策与坑

- **`xray api` 的退出码只反映传输失败**：请求本身错了照样 exit 0（畸形的 `adu` 打印 `Added 0 user(s)`，删一个不存在的用户打印 rpc error）。所以每次调用都解析输出并核对条数，否则 Core 会以为封禁已经生效。
- **`User ... not found` 与 `handler not found` 必须分开**：前者是「本来就不在」，幂等成功；后者是 inbound tag 写错，必须报错，不然打错的 tag 会伪装成一次完成的封禁。
- **`adu` 需要一个「完整到能被解析」的 inbound 片段**，只给 `{tag, users}` 会被静默忽略；其中的 protocol 从已应用的 config 里查，不能从账号形状猜——shadowsocks 和 trojan 都带 `password`。
- **流量用 `statsquery -reset` 读**：读即清零，增量是构造出来的。内核重启只会让这一轮增量偏小，而不会像「记住绝对值再相减」那样出现负跳变。
- **在线查询依赖 `policy.levels."0".statsUserOnline`，缺了会静默失效**（实测 Xray 26.3.27）：`statsonline` / `statsonlineiplist` 返回 NotFound 且非零退出，而 `statsgetallonlineusers` 打印 `{}` 并 exit 0——与「没人在线」无法区分。两种配置 `xray -test` 都放行，只有真内核能证伪，见 `online_live_test.go`。
- **在线数是不同源地址数，不是会话数**；且回环地址完全不计入，本地测试会看起来像功能坏了。
- **一轮在线枚举可能不完整**：roster 一条命令，每个在线用户再一条，中途有人断开是常态。`OnlineUsers` 因此额外返回「本轮是否枚举完整」。Core 不丢弃这一轮——已经看到的地址是真的——而是把该节点标成 partial，于是并发数被当作下界而不是总数。
- `Apply` 里的 `xray -test` 必须带 `-format json`：临时文件不叫 `*.json`，而 Xray 靠扩展名猜格式。
