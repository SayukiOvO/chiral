# migrations — SQLite 迁移

按序号命名的迁移脚本，用 `embed.FS` 打进二进制，Core 启动时由 [`../internal/store`](../internal/store) 按**文件名排序**逐个应用，每个脚本跑在自己的事务里。

| 文件 | 加了什么 |
|------|---------|
| `0001_init.sql` | `nodes`（join token 哈希 + 长期凭证哈希）、`node_configs`（按节点版本化，`applied` 为 0/1/-1） |
| `0002_template.sql` | `profiles`、`profile_client_templates`、`profile_nodes`、`variables`、`variable_components`；给 `nodes` 加 `config_skeleton` |
| `0003_users.sql` | `users`、`user_profiles`、`credentials`（每 用户 × Profile × 节点 一条，`email` 全局唯一） |
| `0004_history.sql` | `node_samples`（定频采样的 gauge）、`traffic_buckets`（增量累加的 counter） |
| `0005_admins.sql` | `admins`、`admin_sessions`、`audit_log` |
| `0006_alerts.sql` | `alert_targets`、`node_alert_state`（已宣告状态 + 抖动去抖） |
| `0007_mfa.sql` | `mfa_credentials`、`mfa_recovery_codes`、`auth_challenges`；给 `admins` 加 `email` / `email_verified` |
| `0008_online.sql` | `user_devices`（观测到的来源地址）；给 `users` 加 `device_limit` |
| `0009_portal.sql` | `user_accounts`、`portal_sessions`、`portal_challenges`；给 `users` 加 `sub_token_enc`、`nodes` 加 `display_name`、`audit_log` 加 `actor_type` |

## 关键约束

**已应用的迁移按文件名记账（`schema_migrations`），不存校验和。** 直接后果：改一个已应用脚本的**注释是安全的**（老库不会重跑，新库跑到的是同样的 SQL）；改它的 **SQL 不安全**——老库永远不会再跑一遍，于是新老库的 schema 从此分叉，而且没有任何东西会报错。加表、加列、改约束一律新开一个文件。

注释改写是真在用的手段：`0003_users.sql` 原先写着订阅 token「从不存储」，`0009_portal.sql` 推翻了这条，于是 0003 的注释被就地改成指向 0009，而它的 SQL 一个字节没动。

另外 SQLite 不能 `ALTER` 一个 CHECK 约束，所以 `portal_challenges` 的 purpose 取值把暂时还没用到的也一并列进去了——不然将来加一种就得重建整张表。
