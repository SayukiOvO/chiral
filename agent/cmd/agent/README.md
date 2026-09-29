# cmd/agent — Agent 入口

`main` 包。解析 flag / 环境变量，装配 `collector`、运行时 provider 与 `client`，并运行到
SIGINT / SIGTERM 为止。

| flag | 环境变量 | 默认 |
|------|----------|------|
| `-panel` | `PANEL_URL` | 必填 |
| `-state-dir` | `CHIRAL_STATE_DIR` | `/var/lib/chiral-agent` |
| `-xray-bin` | `CHIRAL_XRAY_BIN` | `xray` |
| `-insecure` | `CHIRAL_INSECURE=1` | 关（生产强制 TLS） |
| `-heartbeat-interval` | — | 10s |

### 运行时 provider

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| `CHIRAL_RUNTIME_PROVIDER` | `direct-xray` | 可取 `direct-xray` 或 `3x-ui-shadow`。后者目前仅做只读观测。 |
| `CHIRAL_3XUI_URL` | — | `3x-ui-shadow` 使用的 3x-ui API 基址；其他模式忽略。 |
| `CHIRAL_3XUI_TOKEN_FILE` | — | `3x-ui-shadow` 使用的 bearer token 文件；只接受文件路径，token 不得放入命令行或环境变量。 |
| `CHIRAL_3XUI_ALLOW_PUBLIC` | 关闭 | 设为 `1` 时才允许非回环地址，且非回环必须使用 HTTPS；默认只连接节点本机。 |

`3x-ui-shadow` 并不是可写的 3x-ui provider。启用后，Agent 只读取 3x-ui 状态、OpenAPI
能力和最终 config（用于验证 token 的只读权限）；配置下发、用户增删、重启、流量与在线
快照、安装和回滚仍全部交给 `direct-xray`。
因此不得将其设为部署默认值，也不得把它作为 3x-ui 已完成接管的依据。切换到未来的
`ACTIVE` 模式前，必须通过 [`docs/3x-ui-integration.md`](../../../docs/3x-ui-integration.md)
列出的全部功能等价门槛。

## 关键决策

- **`JOIN_TOKEN` 只读环境变量，不给 flag**：命令行参数会经 `ps` 和 shell history 泄露。
- **3x-ui token 只读文件**：`CHIRAL_3XUI_TOKEN_FILE` 指向受限权限文件；不提供 token flag，
  也不接受携带 token 内容的环境变量，避免经进程列表、shell history、容器元数据或日志泄露。
- **磁盘上有 config 就先拉起 Xray，再去连 Core**：不让代理在面板不可达期间掉线。此时 config 版本号未知，无害——Core reconcile 时内容相同会跳过重启。
- **事件回调后绑**：`xray` 只拿到一个闭包间接引用 `client`，避免两者互相持有。
