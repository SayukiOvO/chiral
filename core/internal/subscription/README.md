# subscription — 订阅渲染

把用户有权限的每个「Profile × 节点」渲染成一个片段，拼成其客户端能直接吃下的整份文件。对外是 `GET /sub/{token}`（handler 在 `../api/subscription.go`）：除了路径里的 token 没有其他鉴权——订阅 URL 是粘进客户端的，客户端没法登录；查找只用 token 的 SHA-256，认不出就一个平的 404，不透露用户是否存在。（token 另有一份 AES-GCM 密封副本，只供门户把链接展示给它自己的主人，见 CLAUDE.md 决策 11。）

四种客户端：`xray-json` / `clash` / `vless-uri` / `stash`。选哪种由 `?client=` 决定，没带就看 User-Agent，都认不出回落 `xray-json`（最完整的格式）。

参考 [`../../../docs/user-management.md`](../../../docs/user-management.md)。

## 关键决策

- **不做格式转换，将来也不做**：每个 Profile 为每种客户端各写一份手写模板。xhttp 上下行分离、后量子握手这类特性写成什么样就发出什么样，不必先削足适履塞进一层通用中间表示。
- **某 Profile 缺某客户端的模板就跳过它**，不整单失败：表达不了某个接入点的客户端，仍应拿到其余的。
- **渲染上下文取自 `profile.ClientContext`**，私钥类分量已被整个删掉，模板引用它是硬错误而非泄露。
- **片段之外的外壳由本包拼**：clash / stash 的 `proxies:` 与 `proxy-groups:`（没有 group 客户端根本没法选节点）、xray-json 的 `outbounds` 数组。YAML 拼接保留片段内部的**相对**缩进——`reality-opts` / `ws-opts` 这类嵌套一旦被拉平，仍是合法 YAML，但键被悄悄挂错了父节点。
- **UA 匹配顺序有讲究**：stash 的 UA 里同时含 clash，更具体的必须先匹配。
- **已停用 / 超额的用户照样拿得到订阅**：其凭证早已从节点上摘掉，返回列表只是免得客户端报些莫名其妙的错；处境通过 `Subscription-Userinfo` 头告知。响应一律 `Cache-Control: no-store`——里面是凭证。
