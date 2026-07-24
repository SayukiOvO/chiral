# template — 模板引擎与变量池

变量池（全局 / 节点 / Profile / 用户 四作用域 + 生成器）、config 模板渲染、客户端模板渲染、`xray -test` 校验、config 版本化与回滚。

设计见 [`../../../docs/template-system.md`](../../../docs/template-system.md)（Profile 抽象**已定稿**）。

## 已实现

- `engine.go` — **只做 `{{变量}}` 替换**，不支持条件 / 循环（遍历节点 × 用户由调用方用 Go 完成）。
  - 四作用域合并 `Merge()`，优先级 用户 > 节点 > Profile > 全局。
  - 未定义变量 → 渲染失败且**不返回半渲染结果**（半渲染的 config 可能仍是合法 JSON 且能过 `xray -test`，比直接拒绝危险得多）。
  - `ForClient()` 把私钥类分量**从上下文里整个删掉**，不只是渲染时报错。
  - `Validate()` 一次报出模板的全部问题，供编辑器展示。
- `generator.go` — 变量组生成器：`uuid` / `x25519` / `short_id` / `password` / `mlkem768`。
  - 生成**组**而非单值：服务端引 `{{reality.private}}`、客户端引 `{{reality.public}}`，同一次生成的两个分量，天然配对。

## 为什么生成器必须与 xray 二进制交叉验证

实测：`xray -test` **无法发现「两边都是合法密钥但不是一对」**，只会在运行时静默握手失败。所以自行实现的密钥派生一旦与 Xray 有出入，配置校验不会报任何警。

`generator_test.go` 因此把 Go 生成的私钥喂给 `xray x25519 -i`，逐字节比对公钥。**这条测试在开发时立刻抓到了一个真实的派生不一致**（ML-KEM 的 Hash32 分量对不上），该分量遂改为不产出，而不是产出一个猜的值。

```bash
CHIRAL_XRAY_BIN=/path/to/xray go test ./core/internal/template/
```

未设 `CHIRAL_XRAY_BIN` 且 PATH 里没有 `xray` 时，交叉验证用例会 skip（CI 不因此变红），其余用例照跑。

## 待实现

- ML-DSA-65 生成器（后量子 REALITY 签名，Go 标准库无）。
- 变量在 SQLite 的存储 / 加密（私钥类加密）。
- Profile / 变量 / 绑定的数据模型与渲染编排、`xray -test` 校验与下发。

**状态**：M2 进行中（引擎 + 生成器已完成）。
