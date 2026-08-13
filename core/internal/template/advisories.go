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
func blockAdvisories(configJSON []byte) []string {
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
	hasIPBlock, hasDomainBlock := false, false
	for _, r := range cfg.Routing.Rules {
		if r.OutboundTag != BlackholeTag {
			continue
		}
		if len(r.IP) > 0 {
			hasIPBlock = true
		}
		if len(r.Domain) > 0 {
			hasDomainBlock = true
		}
	}
	var out []string
	if hasIPBlock && cfg.Routing.DomainStrategy != "IPOnDemand" {
		out = append(out,
			"受限目的地按 IP 段拦截，但 routing.domainStrategy 不是 IPOnDemand："+
				"经由域名访问这些网段（A 记录指进去）不会被拦。IPIfNonMatch 也不够——"+
				"它只在第一遍没有任何规则命中时才解析，而中转规则和「全部走 direct」都会先命中。"+
				"请在节点骨架的 routing 里设 domainStrategy: IPOnDemand。")
	}
	if hasDomainBlock && !hasIPBlock {
		out = append(out,
			"这个节点的受限目的地只按域名后缀拦截：用户直接用 IP 访问这些主机不会被拦。"+
				"请给目的地补上对应的 CIDR。")
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
func Advisories(configJSON []byte, clientKinds []string) []string {
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
	out = append(out, blockAdvisories(configJSON)...)
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
				"（它们自报的版本号是 1.x）。握手会回落到 fallback，客户端只看到 TLS 失败。"+
				"要服务 Clash 客户端，请在 realitySettings 里设 \"minClientVer\": \"1.8.0\"。",
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
			"%s：sniffing 开了 destOverride 但没有 routeOnly。"+
				"这个节点给别的代理做前置（链式出站）时，被中转的那条连接会被嗅探到的 SNI "+
				"改写目的地址，连到别处去，报错里不会提到这里。"+
				"若要用它做前置，请在 sniffing 里加 \"routeOnly\": true。", tag))
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
