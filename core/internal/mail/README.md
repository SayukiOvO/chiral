# mail — 事务邮件（SMTP）

发地址验证信和一次性登录码。配置全部来自环境变量：`CHIRAL_SMTP_HOST` / `PORT` / `USER` / `PASSWORD` / `FROM` / `TLS`。

## 关键决策

- **`HOST` 为空 = 邮件功能关闭**，这是合法部署，面板只是不提供邮箱这个因子。`Enabled()` 还要求 `FROM` 非空，未设时回落到 `USER`。
- **默认 `TLS=starttls`；服务器不支持 STARTTLS 就直接报错，绝不静默降级**。默默继续用明文，意味着一次性登录码裸奔过网络。真要明文得显式写 `CHIRAL_SMTP_TLS=none`，那是留给本机 relay 的。
- `implicit` 走 `tls.Dial`（465 那类端口）；两种模式都最低 TLS 1.2，`ServerName` 固定为配置的 host。
- **subject 里的 CR/LF 会被换成空格**：头注入能让调用方偷加收件人。目前 subject 都是我们自己写的，但这行代码很便宜。
- **信封地址剥掉显示名**：`Chiral <a@b>` → `a@b`。
- `dial` 是可替换字段，测试里不会真的发信出去。
