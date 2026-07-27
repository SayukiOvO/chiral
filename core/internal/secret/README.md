# secret — 静态加密

SQLite 里凡是「泄露了就等于凭证泄露」的字段，都经这里封装后入库。调用点全部集中在 [`../store`](../store) 的 scan / insert 里，其余包拿到的永远是明文。

| 字段 | AAD | 说明 |
|------|-----|------|
| `variable_components.value` | `变量ID:分量名` | 仅 `secret` 分量：REALITY `privateKey`、后量子 seed |
| `node_configs.config` | `node-config:节点ID:版本号` | 渲染后的整份 config，按设计就含私钥明文——**不加密的话上一条等于白做** |
| `nodes.config_skeleton` | `node-skeleton:节点ID` | 可能含上游代理凭证 |
| `credentials.secret` | `credential:凭证ID` | 每用户每接入点的 UUID / password |
| `alert_targets.config` | `alert-target:目标ID` | Telegram bot token 或 webhook URL |
| `mfa_credentials.secret` | `mfa-credential:凭证ID` | TOTP 共享密钥，等价于它守护的那个密码 |
| `user_devices.ip_enc` | `user-device:用户ID:IP哈希` | 一张「每个用户家住哪」的表，正是本包存在的理由 |
| `users.sub_token_enc` | `sub-token:用户ID` | 可恢复的订阅 token，见下 |

- **算法**：AES-256-GCM，密钥 = `SHA-256(CHIRAL_SECRET_KEY)`。
- **AAD 绑定到行**：所以有数据库写权限的人**无法把密文挪到另一行**——包括把旧版本 config 冒充成新版本，或把别人的地址塞进你的 `user_devices` 行。
- **格式自描述**：`enc:v1:<keyID>:<base64>` / `plain:<值>`。`keyID` 是密钥的短指纹——换了 `CHIRAL_SECRET_KEY` 会得到一句明确的「这是用另一把密钥加的」，而不是一个难懂的认证失败。
- **可后开**：没配密钥时明文存（启动告警）；之后配上密钥，旧的明文值仍能正常读出。

## 订阅 token 是唯一的硬性依赖

其余字段没有密钥就退化成明文存储加一句告警，`users.sub_token_enc` 不行：**门户开启（`CHIRAL_PORTAL_MODE`）而 `CHIRAL_SECRET_KEY` 为空时 Core 直接拒绝启动**，`store` 侧也用 `errPlaintextSubToken` 兜了一道。理由是它与别的密文不同——别的密文丢了还能重新生成，而订阅 token 存成可恢复的形式，本来就是为了让人随时看到自己的链接；明文存一张全体订阅链接表，等于把门户的价值和它的代价一起放大。见 CLAUDE.md §4.11。

`user_devices` 的主键不能是密文：AES-GCM 是随机化的，同一个地址每次封出来的字节都不同，upsert 会变成每观测一次插一行。所以另存 `ip_hash = SHA-256(地址)` 作稳定主键——它不是安全边界（地址空间小到可枚举），只是个键。

## 威胁模型

防的是**数据库文件外流**（备份被拿走、磁盘快照）——光有 SQLite 文件没有用。

**不防**已经能在面板机上执行代码的攻击者：他们能读环境变量。

## 待办

- 密钥轮换：换 `CHIRAL_SECRET_KEY` 后批量解密重封的流程。目前没有这条路径，轮换后旧密文一律打不开；只有 `UserDevices` 会跳过打不开的行（丢一个地址好过整页加载不出来），其余读取路径直接报错。
