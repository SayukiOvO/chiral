# profile — 编排层

把变量池、模板引擎和节点下发串起来：解析某节点的变量 → 渲染绑定在它上面的每个 Profile 的 inbound → 把有权限用户的凭证渲染进 `settings.clients` → 装配成完整 config.json → `xray -test` → 存版本 → 推给 Agent。`enforce.go` 管另一半：配额 / 到期的周期扫描，与成员变化时的在线增删。

存在的理由是**分层**：`template/` 保持纯逻辑（好测、不依赖 store），`store/` 只管持久化，两者互不认识，由本包居中编排。

## 关键行为

- **作用域优先级**：节点 > Profile > 全局。用户级不是第四个存储作用域，而是渲染时用 `user.CredentialVars` 叠一层（`{{user.uuid}}` / `{{user.password}}` / `{{user.email}}`），凭证按 用户 × Profile × 节点 一份，由 `user.EnsureCredentials` 铸出（幂等：已有的原样返回，所以停用再恢复不会换掉 UUID）。当前不被允许的用户不进 clients 数组。
- **节点元数据不可被遮蔽**：`node.name` / `node.address` / `node.hostname` 在合并之后写入——它们描述的是机器本身，不是可配置偏好。
- **校验不过就不落地**：`xray -test` 失败时**既不存版本也不下发**，错误原样返回。
- **推送失败不算失败**：版本照存，Agent 重连时由心跳对账自动补推。
- **降级模式**：没配 `CHIRAL_XRAY_BIN` 时跳过面板侧校验并告警——Agent 侧仍会校验，所以是降级而非不安全。`Preview` 因此带 `Tested` 字段：没校验过的预览不能当「已验证」展示。

## 在线增删只是加速，正确性在存储的 config 里

`SyncUser` 发的在线 add / remove 只改运行中的内核。Agent 重启、Xray 重启、重连都会重放最后一版存储 config，所以每次成员变化都得 `ApplyUserNodes` 重新装配，否则会复活已封的用户或漏掉刚授权的。

`users.active` 记的是「节点最后被告知的状态」，与 `enabled / quota / expiry` 这组「应该如何」刻意分开：只推差值，未变的机群一条都不发；部分成功保持「还没做完」，留给下一轮重试——宁可重发一次多余的移除，也不要记下一个只落到半个机群的封禁。
