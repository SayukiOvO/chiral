# passkey — WebAuthn（管理员第二因子）

封装 `github.com/go-webauthn/webauthn` 的注册与断言两个仪式，外加凭证的编解码与查找键。

## 关键决策

- **协议本身不自己写**。WebAuthn 要处理 CBOR / COSE 解析、attestation、签名校验，还要把每次仪式绑到 origin 和 RP id 上。手写版本会「看起来能用」——密钥注册得上、登录也成功——同时实际什么都没验证，日常使用中根本暴露不出来。和 `xray -test` 收下一对不匹配的密钥是同一类隐患。
- **RP id 是裸主机名，不带端口**（带 scheme 和端口的是 origin）。passkey 绑定在 RP id 上，它必须和面板实际服务的域名一致——这个绑定正是钓鱼站点无法重放凭证的原因。
- **公网 URL 撑不起 WebAuthn 时 `New` 返回 nil**：没设 `CHIRAL_PUBLIC_URL`、不是 http/https、或者是 localhost 之外的明文 http（浏览器只在安全上下文里放行）。调用方据此把 passkey **整个不提供**，而不是提供了、让用户走到最后一步才失败。
- **注册时 `ResidentKey` 与 `UserVerification` 都请求 `preferred`**：这是第二因子，一个不需要任何用户动作就能认证的密钥算不上因子。
- **`FinishLogin` 返回的凭证带签名计数器，调用方要存下来**：计数器倒退是凭证被克隆的信号。
- 协议错误会被拆成 `Details: DevInfo`，直接点名是 origin 不对还是 RP id 不对——这几乎总是部署问题，不是用户操作问题。
