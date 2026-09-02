# 受限目的地：节点能到、但默认谁都不许去的网段

## 它是什么

动机是 DN42：某台节点接入了它（`172.20.0.0/14`、`fd00::/8`、`*.dn42`），「谁能去那儿」和「谁能用这台节点」是两个问题——节点照常给所有人用，那张私网却几乎不给任何人。

一条受限目的地 = **名字 + 若干 CIDR + 若干域名后缀 + 生效节点 + 获准用户**。装配时在生效节点的 config 里插入路由规则：目的地命中且 email 属于**未获准**用户 → `chiral-blocked`（blackhole）。入口天然知道目的地——代理目标是客户端明着发来的——所以不依赖 sniffing。

## Xray 的最后一跳也必须精确放行

当前 Xray 的 Freedom 出站有服务端兜底安全策略：来自 VLESS、VMess、Trojan、Shadowsocks、
Hysteria 或 WireGuard inbound 的流量，默认不能访问私有及保留地址；VLESS reverse 则默认阻止
所有目标。此策略在 `26.7.28` 上做过真机验证，规则与配置格式见 Xray 官方的
[`finalRules` 文档](https://xtls.github.io/config/outbounds/freedom.html#finalruleobject)。因此「节点的操作系统能到
DN42」还不够；出口节点的 Freedom `settings.finalRules` 也要只放行实际需要的网段，例如：

```json
{
  "protocol": "freedom",
  "tag": "direct",
  "settings": {
    "finalRules": [
      {
        "action": "allow",
        "network": "tcp,udp",
        "ip": ["172.20.0.0/14", "fd00::/8"]
      }
    ]
  }
}
```

这是两道不同的门：`finalRules` 决定这个出口是否允许碰该网段；Chiral 的受限目的地规则决定
哪些用户可以走到那里。当前 Chiral **不会自动注入这条 allow**；管理员必须把它手工合并进每个
实际承载该私网最后一跳的出口节点骨架。仅填写受限目的地的域名后缀也不够：`finalRules` 在最终
IP 上匹配，`ip` 必须覆盖该域名解析到的私网 CIDR。

Chiral 不会把骨架改成无条件 `{"action":"allow"}`，因为那会一并撤掉 loopback、云元数据地址
和其它私网的上游保护，也会让不经过 Chiral 凭证与路由规则的手写 inbound 失去这道 Xray
兜底。生产配置应精确到需要的 `network`、`ip`，能确定端口时再加 `port`。

## 为什么存白名单（全面板唯一的例外）

其它权限都存拒绝，方向由「漏掉一个的代价」决定：漏在拒绝表外的节点是「多一个人能用」，运维看得见、改得掉；漏在拒绝表外的**私网**是「向所有订阅者敞开」，没人会注意，直到它被逛遍。所以这张表存**获准者**，默认谁都不许——新建的目的地先描述网段，再各自决定生效节点和放行的人，三件事分开做。

## 两个容易做错的地方（都有测试钉着）

**空 user 列表匹配所有人，不是没有人。** 「全员获准」必须是**不发规则**，而不是发一条 user 为空的规则——否则「谁都不拦」和「谁都拦」在输出里只差一次疏忽。和中转规则同一个陷阱，方向相反。

**规则必须排在中转规则之前，且入口要继承出口的限制。** 中转规则按 email 匹配该用户的**一切**目的地；排在它后面的拦截等于没有——流量顺着线路到出口时只剩线路的机器凭证，人已经分不出来了。所以：(a) `chiral-blocked` 规则在装配时排在 relay 规则前面；(b) 一条启用的中转线路，其**入口**自动带上出口所属目的地的规则（`RestrictedNodeIDs` 算出这个并集，改动时一起重新下发）。入口是用户还是他自己的最后一个位置。变异测试验证过：把继承逻辑拿掉，真机双内核测试里被拦用户就能穿过线路摸到受限网段。

一个推论：被拦用户经这台入口**去往受限网段的流量一律被拦**，即使他走的线路出口并不在那张网里。保守而无害——那个地址段本来只在私网内有意义。

## domainStrategy：必须是 IPOnDemand，IPIfNonMatch 不够

`ip` 规则默认（AsIs）不解析域名。公网域名把 A 记录指进受限网段，就能绕过 IP 拦截。

**而 `IPIfNonMatch` 关不上这个洞**——这是真机测出来的，不是读文档读出来的：它只在第一遍**没有任何规则命中**时才解析域名重查，而恰恰在这个特性触及的节点上，第一遍总会有规则命中——中转规则不带目的地条件、匹配该用户的一切连接；「全部走 direct」是所有骨架的常见形状。实测（Xray 26.3.27）：IPIfNonMatch + 一条无目的地规则，A 记录指进拦截网段的域名直接走了 direct 且字节送达；同样的规则换 IPOnDemand，进了 `chiral-blocked`。所以 Advisories 只认 `IPOnDemand`。

对称的另一个洞：**只填域名后缀的目的地拦不住直接用 IP 访问的人**（`domain:` 匹配器见不到 IP 形式的目标）。Advisories 对这个形状也会明说；给目的地把 CIDR 补上才是解。域名后缀（`domain:dn42`）单独一条规则——Xray 同一条规则内的条件是 AND，`ip`+`domain` 写在一起等于什么都匹配不上。

另外**回滚会重推旧 config**，里面的拦截规则是当时的。所以 `Rollback` 会先剥掉旧的 `chiral-blocked` 规则、按当前策略重新装配再推——和探活凭证同一个待遇：策略是派生态，不跟着版本走。

## 验过什么

- `TestOnlyTheBarredAreBarredAndOnlyWhereItApplies`：未圈定不发规则；获准者不在 user 列表；全员获准时规则消失而非缩成空表；`xray -test` 通过。
- `TestABlockOutranksTheRelayRule`：拦截排在 relay 规则前；被拦用户的**中转凭证**也在拦截名单里。
- `TestRestrictedTrafficDiesAtTheEntryOfALine`：两个真内核。目的地只圈出口，被拦用户经线路访问受限网段死在入口，获准用户同线路同目的地正常到达。
