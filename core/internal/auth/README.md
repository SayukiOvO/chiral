# auth — 鉴权原语

管理员身份与角色（role.go）、密码哈希（password.go）、TOTP（totp.go）、恢复码与邮件数字码（recovery.go），以及所有 opaque secret 的生成与哈希（auth.go：join token、节点凭证、会话 / 挑战 token、订阅 token 都由 `NewSecret` / `HashSecret` 产出）。**查找一律只用 SHA-256**；除订阅 token 外都只存哈希，订阅 token 另有一份 AES-GCM 密封的副本供门户展示（见 CLAUDE.md 决策 11）。会话与挑战的存储在 [`../store`](../store)，HTTP 守卫在 [`../api`](../api)。

## 角色

三级，逐级包含：`superadmin`（管理管理员）> `operator`（改状态：节点 / Profile / 变量 / 用户）> `viewer`（只读）。粗粒度是故意的——这个面板只有一个团队，不是组织架构；按资源发权限更灵活，也更容易在不知不觉中配错。`CHIRAL_ADMIN_TOKEN` 走 `EnvTokenIdentity()`，等价 superadmin，但在审计里固定署名 `env-token`，「谁干的」不会静默变成没人。

## 关键决策

- **`rank()` 永远不新增 `>= 1` 的值。** 端用户不是这里的主体，门户身份是 `portal.Identity`，不带 Role。一旦有人给端用户加了 rank 1，`requireAdmin`（其实是 viewer 门槛）覆盖的节点列表、用户列表、变量、Profile、流量与审计日志就全部对客户开放。（`config/preview` 不在此列：它挂的是 `requireWrite`，正因为它返回 REALITY 私钥与全部凭证明文。）未知角色 rank 0，什么都做不了——fail closed。
- **secret 与密码用不同的哈希。** 32 字节随机值没什么可猜的，单次 SHA-256 足够；密码由人挑选，落在可猜空间，所以走 PBKDF2-HMAC-SHA256 210k 轮 + 每密码 salt。参数编码进哈希串本身（`pbkdf2$轮数$salt$key`），日后调高轮数不会作废已有密码。校验用常数时间比较，畸形的哈希行只是校验失败而不报错，攻击者分不出它和「密码错」。
- **`SpendVerification` 必须出现在每条走不到 `VerifyPassword` 就拒绝的路径上。** 否则「用户不存在」只花一次数据库查询，真实用户要花 210k 轮 PBKDF2——差两个数量级，隔着网络也测得出，足够枚举账号。dummy hash 在启动时算一次而不是每次请求算，否则恰好把攻击者能控制的那条路径的开销翻倍。
- **TOTP 自己实现而不引依赖**，唯一的理由是它小到能直接拿 RFC 6238 的测试向量对（totp_test.go）：一个会悄悄接受错误验证码的验证器不值得靠信任来用。校验把每个 skew step 都算完再合并结果，不提前返回，时间不泄露命中的是哪一步。
- 恢复码的字母表剔掉 0/O 与 1/I/l——这些是要手抄下来的；匹配时大小写、空格、分组横线一律无意义。
