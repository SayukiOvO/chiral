# 模板与变量系统

> **状态：已定稿（M2）。** Profile 抽象已用真实例子（VLESS + Reality + Vision）演示并采纳，含两条决定：客户端凭证拆成独立的 **client-entry** 模板；模板引擎**只做 `{{变量}}` 替换**，遍历节点/用户由 Core 完成，不引入条件/循环。

## 需求背景

两个诉求：服务端 config 要模板化 + 变量单独管理；客户端订阅也要模板化，两边自动适配。难点在于用户的 Xray 配置很复杂（xhttp 上下行分离 + 后量子加密），没有现成订阅转换能胜任——所以**不走「解析服务端 inbound 反推客户端配置」的转换路线**，那有表达力上限。改用「共享变量 + 手写模板」：两边引用同一份变量，天然对齐，无转换器。

## 1. 变量池

每个变量有：名字（用作 `{{name}}`）、作用域、取值方式、**是否可公开**。作用域四层，渲染时按 **用户 > 节点 > Profile > 全局** 就近取值：

- **全局**：所有节点 / 模板共享，如 `{{panel.domain}}`。
- **节点级**：随节点走，如 `{{node.address}}`、`{{node.region}}`（多来自节点元数据，也可手填覆盖，如给某节点指定 CDN 域名）。
- **Profile 级**：一套接入配置内共享，如端口、SNI、xhttp 路径、Reality / 后量子密钥。
- **用户级**：每用户独立且唯一，如 `{{user.uuid}}`、`{{user.password}}`、`{{user.email}}`。

**取值方式**：
- **静态值**：直接填。
- **算法生成**：内置生成器覆盖 Xray 常用密钥——UUID、X25519 密钥对（Reality，成对产出 `.private` / `.public`）、shortId、随机密码、ML-KEM 后量子密钥对等。成组产出，模板里用 `{{reality.private}}` / `{{reality.public}}` 分别引用，公私钥天然配对。
- **引用 / 派生**：引用节点或其他变量。

**核心机制**：同一个 Reality / PQ 密钥对，服务端和客户端引用的是**同一个变量的不同分量**——两边能对上靠共享同一份变量，而非转换。

**可公开子集（泄露防护）**：每个变量标记是否可公开。客户端渲染上下文**只暴露可公开变量**；私钥类（如 `{{reality.private}}`）在客户端模板中引用会直接报错，私钥永不出服务端。私钥类变量在 SQLite 中加密存储。

## 2. Profile：把服务端与客户端绑成一个单元（已定）

一个 **Profile（接入配置）** 持有三样：

1. **服务端 inbound 骨架模板**（带 `{{}}`）——渲染后作为一项进节点 config.json 的 `inbounds` 数组。**不含 `clients`**：clients 由 Core 按绑定用户注入。
2. **每用户 client-entry 模板**——`clients` 数组里单个用户对象的模板（如 `{"id":"{{user.uuid}}","email":"{{user.email}}","flow":"{{flow}}"}`）。Core 为每个绑定用户渲染一条组装进 clients。**M3 的在线 `AddUser` / `RemoveUser` 增删的就是这一条**，所以从服务端骨架里拆出来。
3. **各客户端渲染模板，每种客户端一份**——`clash` / `xray-json` / `vless-uri`(v2rayN) / `stash` 各一段自由文本模板。

**绑定关系**：Profile → 一或多个节点；用户 → 一或多个 Profile。

订阅时，Core 遍历 `该用户的每个 Profile × 该 Profile 绑定的每个节点`，逐对代入变量渲染客户端片段，再按客户端类型组装成完整订阅。

## 3. 完整例子（VLESS + Reality + Vision）

### 变量表

| 变量 | 作用域 | 取值 | 可公开 |
|---|---|---|---|
| `{{node.address}}` | 节点 | 公网 IP/域名（元数据，可覆盖） | ✅ |
| `{{node.region}}` | 节点 | 地区标签 | ✅ |
| `{{port}}` | Profile | 静态 `443` | ✅ |
| `{{sni}}` | Profile | 静态 `www.microsoft.com` | ✅ |
| `{{flow}}` | Profile | 静态 `xtls-rprx-vision` | ✅ |
| `{{reality.private}}` | Profile | 生成器 X25519 · 私钥分量 | ❌ 仅服务端 |
| `{{reality.public}}` | Profile | 同一密钥对 · 公钥分量 | ✅ |
| `{{reality.shortId}}` | Profile | 生成器 shortId | ✅ |
| `{{user.uuid}}` | 用户 | 生成器 UUID（用户×Profile×节点唯一） | ✅ |
| `{{user.email}}` | 用户 | 统计键 `用户名@接入标识` | ✅ |

### ① 服务端 inbound 骨架（不含 clients）

```json
{
  "tag": "reality-vision", "listen": "0.0.0.0", "port": {{port}},
  "protocol": "vless",
  "settings": { "clients": [], "decryption": "none" },
  "streamSettings": { "network": "tcp", "security": "reality",
    "realitySettings": {
      "dest": "{{sni}}:443", "serverNames": ["{{sni}}"],
      "privateKey": "{{reality.private}}", "shortIds": ["{{reality.shortId}}"]
    } }
}
```

### ② 每用户 client-entry

```json
{ "id": "{{user.uuid}}", "email": "{{user.email}}", "flow": "{{flow}}" }
```

### ③ 客户端模板

`xray-json`（一个 outbound）：

```json
{ "protocol": "vless",
  "settings": { "vnext": [ { "address": "{{node.address}}", "port": {{port}},
    "users": [ { "id": "{{user.uuid}}", "flow": "{{flow}}", "encryption": "none" } ] } ] },
  "streamSettings": { "network": "tcp", "security": "reality",
    "realitySettings": { "serverName": "{{sni}}", "publicKey": "{{reality.public}}",
      "shortId": "{{reality.shortId}}", "fingerprint": "chrome" } } }
```

`vless-uri`（分享链接）：

```
vless://{{user.uuid}}@{{node.address}}:{{port}}?security=reality&sni={{sni}}&pbk={{reality.public}}&sid={{reality.shortId}}&flow={{flow}}&fp=chrome&type=tcp#{{node.region}}
```

## 4. 为什么高度自动化

- **不做转换**：客户端片段是手写模板，xhttp 分离、后量子这类写法怎么写就怎么输出，无转换器上限。
- **参数只定义一次**：服务端用全集（含私钥），客户端用可公开子集 + 每用户凭证；客户端模板每类只写一次，对所有绑定节点自动迭代。
- **加节点**：绑上 Profile，所有用户订阅自动多出该节点。**加用户**：自动在所有绑定节点拿到各自独立 uuid（强隔离凭证）。
- **两档灵活度**：常规接入用参数化模板自动迭代；极端 case 在该 Profile 的客户端模板里写死，互不影响。

## 5. 扩展到 xhttp 分离 + 后量子

结构一模一样：inbound 骨架与客户端模板里多写 xhttp 上下行分离字段、`security` 换对应设置；后量子加一个 `{{pq.*}}`（ML-KEM）生成器变量，服务端引私钥、客户端引公钥，与 Reality 同理。没有任何转换器需要理解 xhttp 分离——这正是「模板而非转换」的意义。定稿具体字段时按线上真实配置逐字核对。

## 6. 渲染与安全保障

### `xray -test` 的能力边界（实测，快照版 v26.7.11）

| 错误类型 | `xray -test` 能否发现 |
|---|---|
| JSON 语法错 | ✅ |
| 字段**值**格式非法（如 seed 不是合法 base64） | ✅ |
| **必填**字段名拼错（等价于缺字段） | ✅ 报 `empty "xxx"` |
| **可选**字段名拼错 / 多余字段 | ❌ 静默忽略（无严格 schema） |
| 服务端私钥与客户端公钥**不配对**（两边都是合法密钥，但不是一对） | ❌ **查不出**，只在运行时握手失败 |

> 最后一行是**「同一变量的不同分量」机制的真正理由**：`xray -test` 根本无法发现配错的密钥对，靠人工核对必然出错。让两边引用同一个生成组的不同分量，是唯一能从机制上排除它的办法。
>
> 同理，Core 自己实现密钥派生时**必须与 xray 二进制交叉验证**（见 `core/internal/template/generator_test.go`：拿 Go 生成的私钥喂 `xray x25519 -i`，比对公钥是否逐字节一致）。这条测试在开发时立刻抓到过一个真实的派生不一致。

- **应用前校验**：渲染出 config.json 后，下发前 `xray -test -format json` 校验，不过就拒绝——但要清楚上表的边界。
- **版本化与回滚**：每版生效 config 存版本号，一键回滚（回滚 = 把旧版本作为新 `ConfigPush` 重推，见 CLAUDE.md 决策 7）。
- **变量泄露防护**：见 §1「可公开子集」。

## 7. 引擎边界（已定）

- **只支持 `{{变量}}` 替换**，不支持条件 / 循环。遍历节点 × 用户、组装 clients 数组、按客户端类型拼装订阅——都由 Core 用代码完成，不进模板。心智负担最小、最难写错、无注入面。
- 变量名合法字符 `[a-zA-Z0-9_.]`；`{{ name }}` 允许内部空白；未定义变量或客户端上下文引用私钥 → 渲染报错。

## 7. 实现现状（M2）

| 部件 | 位置 | 说明 |
|---|---|---|
| 模板引擎 | `core/internal/template/engine.go` | `{{变量}}` 替换、四作用域合并、未定义变量报错且**不返回半渲染结果**、`ForClient()` 剥离私钥 |
| 生成器 | `template/generator.go` | uuid / x25519 / short_id / password / mlkem768，**与 xray 交叉验证** |
| ML-DSA-65 + 校验器 | `template/xray.go` | 种子自生成、派生交给 xray 二进制；`TestConfig` 跑 `xray -test` |
| config 装配 | `template/assemble.go` | 骨架 + 各 Profile 渲染出的 inbound 追加进 `inbounds`；手写 inbound 保留 |
| 私钥加密 | `core/internal/secret/` | AES-GCM，密钥来自 `CHIRAL_SECRET_KEY`，AAD 绑定到具体行 |
| 数据模型 | `core/migrations/0002_template.sql` | profiles / profile_client_templates / profile_nodes / variables / variable_components |
| 编排 | `core/internal/profile/` | 解析变量池 → 渲染 → 装配 → `xray -test` → 存版本 → 下发 |
| REST API | `core/internal/api/template.go` | 变量、Profile、绑定、preview / apply |

**关键行为**：

- `xray -test` 不通过的配置**既不存版本也不下发**（实测验证），错误把 Xray 的诊断原样返回给操作者。
- API 返回变量时**私钥分量一律遮蔽**为 `••••••••`。
- **加密覆盖到渲染产物**：私钥分量、`node_configs.config`、`nodes.config_skeleton` 三者都加密。渲染后的 config 按设计含私钥明文，只加密变量表等于白做——拿到库文件就能 `select config from node_configs` 读出每个节点的密钥。
- **面板侧校验环境是钉死的**：`xray -test` 子进程只拿到固定的 `XRAY_LOCATION_ASSET`（geo 资源必须随镜像走，否则 `geosite:` / `geoip:` 路由规则会被误拒），且不继承面板的其它环境变量——在这里能过的配置，到节点上必须是同一个意思。

### 部署所需环境变量

- `CHIRAL_SECRET_KEY`：私钥加密密钥。**不设则私钥明文入库**，启动时会告警。`openssl rand -base64 32` 生成。
- `CHIRAL_XRAY_BIN`：面板侧 Xray 二进制。不设则跳过下发前校验（Agent 侧仍会校验），且 ML-DSA-65 不可用。

## 待办

- [ ] ML-KEM-768 的 `Hash32` 分量：xray 会打印，但其派生方式未能复现，**暂不产出**（不发无法验证的值）。用到再补。
- [ ] 节点 config 骨架的 Monaco 编辑器（`{{变量}}` 高亮 + 校验提示）与 Profile 编辑界面。
- [ ] 密钥轮换流程（换 `CHIRAL_SECRET_KEY` 后批量重新封装）。
- [ ] 客户端模板渲染与订阅（M3）。
