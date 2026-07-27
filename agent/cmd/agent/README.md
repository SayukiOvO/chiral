# cmd/agent — Agent 入口

`main` 包。解析 flag / 环境变量，装配 `collector` + `xray` + `client` 并运行到 SIGINT / SIGTERM 为止。

| flag | 环境变量 | 默认 |
|------|----------|------|
| `-panel` | `PANEL_URL` | 必填 |
| `-state-dir` | `CHIRAL_STATE_DIR` | `/var/lib/chiral-agent` |
| `-xray-bin` | `CHIRAL_XRAY_BIN` | `xray` |
| `-insecure` | `CHIRAL_INSECURE=1` | 关（生产强制 TLS） |
| `-heartbeat-interval` | — | 10s |

## 关键决策

- **`JOIN_TOKEN` 只读环境变量，不给 flag**：命令行参数会经 `ps` 和 shell history 泄露。
- **磁盘上有 config 就先拉起 Xray，再去连 Core**：不让代理在面板不可达期间掉线。此时 config 版本号未知，无害——Core reconcile 时内容相同会跳过重启。
- **事件回调后绑**：`xray` 只拿到一个闭包间接引用 `client`，避免两者互相持有。
