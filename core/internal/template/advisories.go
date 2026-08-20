package template

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// blockAdvisories warns when restricted-destination rules exist in shapes
// that do not enforce what they look like they enforce.
//
// Two bypasses, mirror images of each other. An ip rule never sees an address
// for a domain-form destination — a public A record pointing into the range
// walks past it — unless routing resolves domains BEFORE matching. Only
// IPOnDemand does that. IPIfNonMatch is not enough, and this is measured, not
// read: it resolves only when NO rule matched the first pass, and on exactly
// the nodes this feature touches, one always has — a relay rule carries no
// destination condition and matches its user's every connection, and
// "everything to direct" is the shape of every skeleton in the wild. Verified
// against a real kernel: IPIfNonMatch + a destination-less rule routed a
// domain whose A record sat inside the blocked range straight past the block;
// IPOnDemand with identical rules blackholed it.
//
// And a domain rule never sees a name when the user connects by literal IP —
// so a destination described only by suffixes bars nobody who knows the
// address.
func blockAdvisories(configJSON []byte, egressTags []string) []string {
	var cfg struct {
		Routing struct {
			DomainStrategy string `json:"domainStrategy"`
			Rules          []struct {
				IP          []string `json:"ip"`
				Domain      []string `json:"domain"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil
	}
	// Which tags this panel's own egress rules point at. Identified by the
	// caller rather than by the tag's spelling: a landing may be "direct" or
	// an exit's tag, and guessing from the name caught only fleet landings —
	// the operator's own skeleton rules must NOT draw this warning, since
	// pairing geoip:cn with geosite:cn is the correct idiom and they wrote it
	// deliberately.
	fromEgress := make(map[string]bool, len(egressTags))
	for _, t := range egressTags {
		fromEgress[t] = true
	}
	hasIPBlock, hasDomainBlock := false, false
	hasIPEgress := false
	for _, r := range cfg.Routing.Rules {
		if r.OutboundTag == BlackholeTag {
			if len(r.IP) > 0 {
				hasIPBlock = true
			}
			if len(r.Domain) > 0 {
				hasDomainBlock = true
			}
			continue
		}
		// An egress rule matching on IPs has the same DNS-timing problem as a
		// block: with the wrong domainStrategy the rule never sees an address
		// for a domain-form destination, so "geoip:netflix goes out through
		// Japan" quietly does not apply to anything asked for by name — which
		// is nearly everything.
		if len(r.IP) > 0 && fromEgress[r.OutboundTag] {
			hasIPEgress = true
		}
	}
	var out []string
	if hasIPEgress && !hasIPBlock && cfg.Routing.DomainStrategy != "IPOnDemand" {
		out = append(out,
			"出站分流规则按 IP / geoip 匹配，但 routing.domainStrategy 不是 IPOnDemand："+
				"以域名形式访问这些目标时规则不会命中（首轮匹配没有地址可供比对），流量将直接出站。"+
				"请在节点骨架的 routing 中设置 domainStrategy: IPOnDemand。")
	}
	if hasIPBlock && cfg.Routing.DomainStrategy != "IPOnDemand" {
		out = append(out,
			"受限目的地按 IP 段拦截，但 routing.domainStrategy 不是 IPOnDemand："+
				"以域名形式访问这些网段（A 记录指向其中）将不会被拦截。IPIfNonMatch 亦不足以覆盖此情形："+
				"该策略仅在首轮匹配无任何规则命中时才解析域名，而中转规则与「全部直连」的骨架规则均会先行命中。"+
				"请在节点骨架的 routing 中设置 domainStrategy: IPOnDemand。")
	}
	if hasDomainBlock && !hasIPBlock {
		out = append(out,
			"本节点的受限目的地仅按域名后缀拦截：订阅者直接以 IP 访问这些主机时不会被拦截。"+
				"请为该目的地补充对应的 CIDR。")
	}
	return out
}

// realityMinClientDefault is the minimum client version Xray 26.x applies to a
// REALITY inbound that does not set one. It refuses anything older, and every
// clash-family client advertises its own version number — mihomo says 1.8.2 —
// so the default excludes all of them.
const realityMinClientDefault = "26.3.27"

// Advisories reports configurations that assemble, validate and then do not
// work.
//
// `xray -test` answers "is this well formed", which is a different question
// from "can the people this is for connect". A REALITY inbound with the
// default minimum client version passes validation, starts cleanly, and then
// refuses every clash-family client: the handshake falls through to the
// fallback target, so the client reports a TLS failure with nothing naming the
// cause, and the operator sees a healthy node with subscribers who time out.
//
// Reported rather than refused. The default is a reasonable choice for a fleet
// serving only Xray clients, and the panel does not get to decide that; it
// only has to make the consequence visible before somebody debugs it from the
// wrong end.
func Advisories(configJSON []byte, clientKinds []string, egressTags []string) []string {
	var cfg struct {
		Inbounds []struct {
			Tag            string `json:"tag"`
			StreamSettings struct {
				Security string `json:"security"`
				Reality  struct {
					MinClientVer string `json:"minClientVer"`
				} `json:"realitySettings"`
			} `json:"streamSettings"`
			Sniffing struct {
				Enabled      bool     `json:"enabled"`
				DestOverride []string `json:"destOverride"`
				RouteOnly    bool     `json:"routeOnly"`
			} `json:"sniffing"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil
	}
	var out []string
	out = append(out, sniffingAdvisories(cfg.Inbounds)...)
	out = append(out, blockAdvisories(configJSON, egressTags)...)
	if !servesClashFamily(clientKinds) {
		return out
	}
	for _, ib := range cfg.Inbounds {
		if !strings.EqualFold(ib.StreamSettings.Security, "reality") {
			continue
		}
		min := ib.StreamSettings.Reality.MinClientVer
		if min == "" {
			min = realityMinClientDefault
		}
		if !refusesClashClients(min) {
			continue
		}
		tag := ib.Tag
		if tag == "" {
			tag = "(未命名 inbound)"
		}
		out = append(out, fmt.Sprintf(
			"%s：REALITY 的 minClientVer 为 %s，会拒绝全部 Clash 类客户端"+
				"（此类客户端自报的版本号为 1.x）。握手将回落至 fallback，客户端仅能观察到 TLS 失败。"+
				"如需服务 Clash 类客户端，请在 realitySettings 中设置 \"minClientVer\": \"1.8.0\"。",
			tag, min))
	}
	return out
}

// sniffingAdvisories reports the setting that breaks this node as a relay.
//
// `destOverride` replaces a connection's destination address with the name
// sniffed out of it. For a subscriber browsing, that is the point — it is what
// lets domain rules match. For a connection this node is relaying on behalf of
// another proxy, it is fatal: that connection is itself a TLS handshake
// carrying the other provider's camouflage SNI, so the destination gets
// rewritten from the address the client asked for to whatever that name
// resolves to, and the dial fails with nothing in it that names the cause.
//
// `routeOnly` keeps the sniffed name for routing decisions and stops it being
// written back over the address. Reported rather than corrected: a fleet that
// never relays for anything is fine as it is, and the panel does not get to
// decide that.
func sniffingAdvisories(inbounds []struct {
	Tag            string `json:"tag"`
	StreamSettings struct {
		Security string `json:"security"`
		Reality  struct {
			MinClientVer string `json:"minClientVer"`
		} `json:"realitySettings"`
	} `json:"streamSettings"`
	Sniffing struct {
		Enabled      bool     `json:"enabled"`
		DestOverride []string `json:"destOverride"`
		RouteOnly    bool     `json:"routeOnly"`
	} `json:"sniffing"`
}) []string {
	var out []string
	for _, ib := range inbounds {
		sn := ib.Sniffing
		if !sn.Enabled || len(sn.DestOverride) == 0 || sn.RouteOnly {
			continue
		}
		tag := ib.Tag
		if tag == "" {
			tag = "(未命名 inbound)"
		}
		out = append(out, fmt.Sprintf(
			"%s：sniffing 启用了 destOverride，但未设置 routeOnly。"+
				"本节点作为其他代理的前置（链式出站）时，被中转的连接会被嗅探到的 SNI "+
				"改写目的地址，从而连接至错误的目标，且错误信息不会指向此处。"+
				"如需将其用作前置，请在 sniffing 中加入 \"routeOnly\": true。", tag))
	}
	return out
}

// servesClashFamily reports whether any of these client templates is one whose
// clients advertise a 1.x version.
func servesClashFamily(kinds []string) bool {
	for _, k := range kinds {
		switch strings.ToLower(k) {
		case "clash", "stash", "mihomo":
			return true
		}
	}
	return false
}

// refusesClashClients reports whether a minimum version excludes a 1.x client.
//
// Compared on the major component alone: the clash family is on 1.x and Xray
// on 26.x, so anything that demands 2 or more excludes them, and a malformed
// value is left alone rather than guessed at.
func refusesClashClients(min string) bool {
	major, _, _ := strings.Cut(min, ".")
	n, err := strconv.Atoi(strings.TrimSpace(major))
	if err != nil {
		return false
	}
	return n >= 2
}
