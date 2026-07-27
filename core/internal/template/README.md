# template — 模板引擎、变量生成器与 config 装配

`{{变量}}` 渲染、密钥组生成、节点 config.json 装配、`xray -test` 校验。本包是纯逻辑，不认识 `store`——读库、存版本、下发由 [`../profile/`](../profile/) 编排。变量的持久化与私钥分量加密见 `../store/template.go` + [`../secret/`](../secret/)。

设计见 [`../../../docs/template-system.md`](../../../docs/template-system.md)。

| 文件 | 职责 |
| --- | --- |
| `engine.go` | `{{变量}}` 替换；四作用域 `Merge()`（用户 > 节点 > Profile > 全局）；`ForClient()`；`Validate()` 一次报全部问题 |
| `generator.go` | 变量组生成器：`uuid` / `x25519` / `short_id` / `password` / `mlkem768` |
| `xray.go` | 包裹 xray 二进制：`-test` 校验、`mldsa65` 派生 |
| `assemble.go` | 骨架 + 各 Profile 渲染出的 inbound + clients 数组 → 完整 config.json |
| `api.go` | 往 config 里补 Xray **自己的** gRPC 管理 API、stats 与 policy（不是面板的 REST API） |

## 关键决策

- **引擎只做替换，没有条件与循环**：遍历 节点 × 用户、拼 clients 数组由调用方用 Go 完成，模板因此无法「被编程」。
- **渲染失败绝不返回半成品**：未定义变量、或客户端模板引用私钥类变量，一律报错。半渲染的 config 往往仍是合法 JSON、能过 `xray -test`，比直接拒绝危险得多。
- **`ForClient()` 把私钥分量从上下文里整个删掉**，不是渲染时才拦——别处写出 bug 也读不到。
- **生成的是「组」不是单值**：服务端引 `{{reality.private}}`、客户端引 `{{reality.public}}`，是同一次生成的两个分量。
- **`EnsureAPI` 只补缺的那部分**：操作员手写了 `api` 块就保留，但 `policy.levels.0` 的三个 stats 开关照样补齐。少了 `statsUserOnline`，`statsgetallonlineusers` 会返回 `{}` 并退出 0，和「没人在线」一模一样。

## `xray -test` 查不出密钥对不匹配

实测：两边都是合法密钥但**不是一对**时，`xray -test` 一声不吭，只在运行时静默握手失败。所以服务端与客户端必须引用**同一个变量组的不同分量**——这是唯一可靠的保证，不是风格偏好。

同理，自实现的密钥派生与 Xray 有出入也不会有任何告警。`generator_test.go` 因此把 Go 生成的私钥喂给 `xray x25519 -i` 逐字节比对公钥；这条测试当场抓到 ML-KEM 的 `Hash32` 分量派生对不上，该分量遂**不产出**，而不是产出一个猜的值。ML-DSA-65 索性整个交给二进制（`xray mldsa65 -i <seed>`），只有 seed 仍出自我们自己的 CSPRNG。

```bash
CHIRAL_XRAY_BIN=/path/to/xray go test ./core/internal/template/
```

未设 `CHIRAL_XRAY_BIN` 且 PATH 里没有 `xray` 时，交叉验证用例会 skip，其余用例照跑。
