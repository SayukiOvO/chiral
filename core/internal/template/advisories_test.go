package template

import (
	"fmt"
	"strings"
	"testing"
)

func realityConfig(minVer string) []byte {
	min := ""
	if minVer != "" {
		min = `, "minClientVer": "` + minVer + `"`
	}
	return []byte(`{"inbounds":[{"tag":"vision","streamSettings":{"security":"reality",
		"realitySettings":{"serverNames":["x"]` + min + `}}}]}`)
}

// The configuration passes xray -test, starts cleanly, and refuses every
// clash-family client. The handshake falls through to the fallback, so the
// client reports a TLS failure and nothing anywhere names the cause.
func TestWarnsWhenRealityWouldRefuseClashClients(t *testing.T) {
	got := Advisories(realityConfig(""), []string{"clash", "xray-json"}, nil)
	if len(got) != 1 {
		t.Fatalf("advisories = %v, want one", got)
	}
	for _, want := range []string{"vision", "minClientVer", "26.3.27", "1.8.0"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("advisory does not mention %q: %s", want, got[0])
		}
	}
}

func TestQuietWhenNobodyIsServedAClashConfig(t *testing.T) {
	if got := Advisories(realityConfig(""), []string{"xray-json", "vless-uri"}, nil); got != nil {
		t.Fatalf("warned with no clash template: %v", got)
	}
}

func TestQuietWhenMinClientVerAdmitsThem(t *testing.T) {
	for _, v := range []string{"1.8.0", "0.0.0", "1.0"} {
		if got := Advisories(realityConfig(v), []string{"clash"}, nil); got != nil {
			t.Errorf("minClientVer %q warned: %v", v, got)
		}
	}
}

func TestWarnsForStashToo(t *testing.T) {
	if got := Advisories(realityConfig(""), []string{"stash"}, nil); len(got) != 1 {
		t.Fatalf("stash is clash-family: %v", got)
	}
}

// A non-REALITY inbound has no such rule, and a malformed version is left
// alone rather than guessed at.
func TestNoAdvisoryWhereThereIsNothingToSay(t *testing.T) {
	plain := []byte(`{"inbounds":[{"tag":"t","streamSettings":{"security":"tls"}}]}`)
	if got := Advisories(plain, []string{"clash"}, nil); got != nil {
		t.Errorf("warned about a TLS inbound: %v", got)
	}
	if got := Advisories(realityConfig("nonsense"), []string{"clash"}, nil); got != nil {
		t.Errorf("guessed at a malformed version: %v", got)
	}
	if got := Advisories([]byte("not json"), []string{"clash"}, nil); got != nil {
		t.Errorf("warned about unparseable config: %v", got)
	}
}

// The setting that made every chained external node fail while the node itself
// worked perfectly, `xray -test` passed, and the error named an address nobody
// had configured.
func TestSniffingWithoutRouteOnlyIsReported(t *testing.T) {
	cfg := []byte(`{"inbounds":[{"tag":"in","sniffing":{"enabled":true,"destOverride":["http","tls"]}}]}`)
	got := Advisories(cfg, []string{"xray-json"}, nil)
	if len(got) != 1 || !strings.Contains(got[0], "routeOnly") {
		t.Fatalf("advisories = %v", got)
	}

	// With routeOnly, nothing to say.
	cfg = []byte(`{"inbounds":[{"tag":"in","sniffing":{"enabled":true,"destOverride":["http","tls"],"routeOnly":true}}]}`)
	if got := Advisories(cfg, []string{"xray-json"}, nil); len(got) != 0 {
		t.Fatalf("routeOnly still warned: %v", got)
	}
	// And sniffing off is not a relay hazard either.
	cfg = []byte(`{"inbounds":[{"tag":"in","sniffing":{"enabled":false,"destOverride":["tls"]}}]}`)
	if got := Advisories(cfg, []string{"xray-json"}, nil); len(got) != 0 {
		t.Fatalf("disabled sniffing warned: %v", got)
	}
}

// The two advisories are independent: a node can serve clash clients badly and
// relay badly at the same time, and hiding one behind the other is how the
// second one gets found by a subscriber instead of by the operator.
func TestBothAdvisoriesCanFireTogether(t *testing.T) {
	cfg := []byte(`{"inbounds":[{"tag":"in",
		"streamSettings":{"security":"reality","realitySettings":{"minClientVer":"26.3.27"}},
		"sniffing":{"enabled":true,"destOverride":["tls"]}}]}`)
	got := Advisories(cfg, []string{"clash"}, nil)
	if len(got) != 2 {
		t.Fatalf("expected both, got %v", got)
	}
}

// IPIfNonMatch is NOT sufficient for the block rules, and this is measured,
// not read: it resolves a domain only when no rule matched the first pass,
// and a relay rule (no destination condition) always matches first. Only
// IPOnDemand resolves before matching.
func TestBlockAdvisoryAcceptsOnlyIPOnDemand(t *testing.T) {
	build := func(strategy string) []byte {
		cfg := `{"inbounds":[],"routing":{` +
			`"domainStrategy":` + fmt.Sprintf("%q", strategy) + `,` +
			`"rules":[{"type":"field","ip":["172.20.0.0/14"],"user":["bob@x"],"outboundTag":"chiral-blocked"}]}}`
		return []byte(cfg)
	}
	for _, insufficient := range []string{"", "AsIs", "IPIfNonMatch"} {
		if got := Advisories(build(insufficient), nil, nil); len(got) == 0 {
			t.Errorf("domainStrategy %q passed without an advisory", insufficient)
		}
	}
	if got := Advisories(build("IPOnDemand"), nil, nil); len(got) != 0 {
		t.Errorf("IPOnDemand still drew an advisory: %v", got)
	}
}

// A destination described only by domain suffixes bars nobody who connects by
// literal IP; the operator has to hear that from somewhere.
func TestDomainOnlyBlockDrawsAnAdvisory(t *testing.T) {
	cfg := []byte(`{"inbounds":[],"routing":{"domainStrategy":"IPOnDemand",` +
		`"rules":[{"type":"field","domain":["domain:dn42"],"user":["bob@x"],"outboundTag":"chiral-blocked"}]}}`)
	got := Advisories(cfg, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want exactly the domain-only advisory, got %v", got)
	}
}

// The egress DNS-timing advisory must fire whatever the landing is — the
// first version keyed on the tag's spelling and so caught only fleet
// landings, missing "geoip:netflix goes out through the Japanese provider",
// which is the case operators actually write.
//
// And it must NOT fire for the operator's own skeleton rules: pairing
// geoip:cn with geosite:cn is the correct idiom, written deliberately.
func TestEgressIPAdvisoryCoversEveryLandingAndOnlyOurs(t *testing.T) {
	build := func(tag string) []byte {
		return []byte(`{"inbounds":[],"routing":{"domainStrategy":"AsIs","rules":[` +
			`{"type":"field","ip":["geoip:netflix"],"outboundTag":` + fmt.Sprintf("%q", tag) + `}]}}`)
	}
	for _, tag := range []string{"direct", "exit-abc123", "egress-abc123"} {
		got := Advisories(build(tag), nil, []string{tag})
		if len(got) == 0 {
			t.Errorf("landing %q drew no advisory", tag)
		}
	}
	// The same rule, not contributed by us: silence.
	if got := Advisories(build("direct"), nil, nil); len(got) != 0 {
		t.Errorf("the operator's own ip rule drew an advisory: %v", got)
	}
	// And IPOnDemand resolves before matching, so there is nothing to say.
	cfg := []byte(`{"inbounds":[],"routing":{"domainStrategy":"IPOnDemand","rules":[` +
		`{"type":"field","ip":["geoip:netflix"],"outboundTag":"direct"}]}}`)
	if got := Advisories(cfg, nil, []string{"direct"}); len(got) != 0 {
		t.Errorf("IPOnDemand still drew an advisory: %v", got)
	}
}
