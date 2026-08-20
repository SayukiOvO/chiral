# 出站分流：这台节点上，什么流量从哪出去

## 它是什么

一条规则 = **匹配（域名 / geosite，或 IP / geoip）+ 落点 + 在哪台节点上**。命中的流量从落点出去，其余照常从这台节点直出。

落点三选一，三种都是复用已有机件：

- **直连**：节点自己的 `direct` outbound
- **外部节点**：`external.XrayOutbound`，和「经由」用的是同一个转换
- **本机队的另一台节点**：和中转线路同款——用**目标节点那个接入配置的 `xray-json` 客户端模板**渲染出 outbound

第三种不要求先建中转线路。中转线路是**给订阅者看的一条线**（有名字、有权限、进订阅）；出站分流是**机器自己的路由**，订阅者看不见也选不了。两者机制相同、意图不同，所以是两张表。

## 那份拨号凭证

落点是自有节点时，需要一份凭证去拨。和中转线路一样：**一条规则一份**，不属于任何订阅者，存 `node_egress_rules.secret`（AES-GCM 密封），统计键 `egress.<规则 id>@<接入配置>.<目标节点>`。

**不计费**——字节已经在订阅者认证的那台节点上计过一次了。`AddCredentialTraffic` 认不出这个 email 会直接跳过，正是对的。

目标节点必须**接受**这份凭证：装配目标节点时会把它作为一条普通 client entry 渲染进对应 inbound（`egressClientEntries`）。所以创建 / 修改 / 删除规则会**同时重新下发两台节点**。

## 顺序就是优先级

Xray 路由是首个匹配优先，所以装配时的次序是硬性的：

```
受限目的地拦截  >  出站分流  >  中转规则  >  运维骨架自己的规则
```

- **拦截必须在最前**：否则被拦用户可以借道某个落点摸进受限网段。
- **出站分流必须在中转规则之前**：中转规则不带目的地条件、匹配该用户的一切连接，排在它后面的分流永远不会命中。这条有变异测试守着（把顺序调到 exits 之后，`TestEgressSitsBetweenBlocksAndRelayRules` 立刻变红）。

同一节点内多条规则的先后由运维在面板上排（上移 / 下移），位置即优先级。

## 两个容易做错的地方

**geosite 和 geoip 必须拆成两条规则。** Xray 同一条规则内的条件是 AND，`domain` + `ip` 写在一起等于「既是这个域名又是这个 IP」——什么都匹配不上。所以一条规则渲染出最多两条 Xray 规则，共用同一个 `outboundTag`。

**匹配不能为空。** 一条没有任何匹配的规则会渲染成没有目的地条件的 Xray 规则，那匹配的是**全部**——整台节点的流量会被它吞掉。数据库 CHECK 和 API 都拒绝这种规则，和「空 user 列表匹配所有人」同一类陷阱。

## outbound 共用

同一个外部节点既被「经由」中继、又被出站分流当落点时，**共用同一条 outbound**（tag 相同，只生成一次）。两条 outbound 指向同一家提供商会翻倍连接数，也会把「这条线到底跑了多少」拆成两半。

## 验过什么

- `TestGeoMatchesSplitIntoTwoRulesOnOneOutbound`：geosite/geoip 分成两条、共用一个 tag，且没有任何一条规则同时带 domain 和 ip。
- `TestARealKernelAcceptsGeoEgressRules`：真内核 + 真 geodata 校验 `geosite:netflix` / `geoip:jp`——类别名打错时 `xray -test` 是唯一能拦住的地方（需要 `geosite.dat`，面板镜像自带，本地跑要设 `XRAY_LOCATION_ASSET`）。
- `TestEgressSitsBetweenBlocksAndRelayRules`：三者次序，变异验证过。
- `TestAnEgressLandingSharesAnExistingExitOutbound`：共用不重复拨号。
- `TestEgressSendsOnlyMatchedTrafficThroughTheOtherNode`：**双真内核**。源节点默认 outbound 是 blackhole，所以到得了目的地就只可能是从目标节点出去的；同一台服务器用域名访问（命中）能到、用 IP 访问（不命中）到不了——反向对照排除了「所有流量都被送进落点」这种假通过。
