package template

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

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
	if !servesClashFamily(clientKinds) {
		return nil
	}
	var cfg struct {
		Inbounds []struct {
			Tag            string `json:"tag"`
			StreamSettings struct {
				Security string `json:"security"`
				Reality  struct {
					MinClientVer string `json:"minClientVer"`
				} `json:"realitySettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil
	}
	var out []string
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
