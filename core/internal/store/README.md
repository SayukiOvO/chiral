# store — 持久化

SQLite（WAL）访问层，**唯一碰数据库的地方**，其余包一律走它。启动时自动应用 [`../../migrations`](../../migrations)。

连接池刻意设成 `SetMaxOpenConns(1)`：SQLite 同时只允许一个写者，多连接只会把并发写变成 `SQLITE_BUSY` 重试风暴。DSN 里开了 `foreign_keys(1)`，所以迁移里的 `ON DELETE CASCADE` 是真会级联的。

| 文件 | 覆盖的表 |
|------|---------|
| `store.go` | `nodes`、`node_configs`；迁移执行、`NewID()`、`IsNotFound()` |
| `template.go` | `profiles`、`profile_client_templates`、`profile_nodes`、`variables`、`variable_components` |
| `user.go` | `users`、`user_profiles`、`credentials`，以及 Agent 增量流量入账 |
| `history.go` | `node_samples`（gauge，7 天）、`traffic_buckets`（counter 累加，90 天） |
| `admin.go` / `mfa.go` | `admins`、`admin_sessions`；`mfa_credentials`、`mfa_recovery_codes`、`auth_challenges` |
| `audit.go` | `audit_log`（180 天） |
| `alert.go` | `alert_targets`、`node_alert_state` |
| `device.go` | `user_devices`（30 天） |
| `portal.go` | `user_accounts`、`portal_sessions`、`portal_challenges`，以及门户作用域的取数 |

## 关键决策

- **密文进出都封在这一层**：`Component.Value`、`Credential.Secret`、`Node.ConfigSkeleton` 等字段对调用方永远是明文，`box.Seal` / `box.Open` 只出现在 scan 与 insert 里。`Store.box` 永不为 nil——没配密钥时它是一个明文存储的 Box。加密清单见 [`../secret`](../secret)。
- **`sub_token_enc` 不进 `userCols`**，由 `SubToken()` 单独取。放进公共列会让每次 `ListUsers` 都白解一次密，而且只要有一行是用轮换前的密钥封的，整个查询就会失败。
- **端用户与管理员没有共用表**：门户会话在 `portal_sessions`，而 `AdminBySession` 只 join `admins`，所以门户 token 在物理上无法解析成管理员身份。这个保证是一个表名，不是一句需要人记住的 `WHERE`。
- **`traffic_buckets.user_id` 不设外键，`user_devices` 反而设 `ON DELETE CASCADE`**：字节总数在用户被删后对整个 fleet 仍有意义（落到 `''` 哨兵），而地址是「某个具体的人住在哪」，留在哨兵下只会没有 UI 能看见、也就没有人会去清。
- **流量只接受非负增量**：`AddCredentialTraffic` 拒绝负值。Agent 用 `statsquery -reset` 读计数器，Xray 重启只会让某一轮的增量偏小，不会出现「记住上次值」方案那种负跳变。
- **`PutCredential` 幂等**：已存在就原样返回，不换新 secret——重新生成会让用户已配置的客户端在无人察觉的情况下失效。
