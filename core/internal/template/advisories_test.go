package template

import (
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
	got := Advisories(realityConfig(""), []string{"clash", "xray-json"})
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
	if got := Advisories(realityConfig(""), []string{"xray-json", "vless-uri"}); got != nil {
		t.Fatalf("warned with no clash template: %v", got)
	}
}

func TestQuietWhenMinClientVerAdmitsThem(t *testing.T) {
	for _, v := range []string{"1.8.0", "0.0.0", "1.0"} {
		if got := Advisories(realityConfig(v), []string{"clash"}); got != nil {
			t.Errorf("minClientVer %q warned: %v", v, got)
		}
	}
}

func TestWarnsForStashToo(t *testing.T) {
	if got := Advisories(realityConfig(""), []string{"stash"}); len(got) != 1 {
		t.Fatalf("stash is clash-family: %v", got)
	}
}

// A non-REALITY inbound has no such rule, and a malformed version is left
// alone rather than guessed at.
func TestNoAdvisoryWhereThereIsNothingToSay(t *testing.T) {
	plain := []byte(`{"inbounds":[{"tag":"t","streamSettings":{"security":"tls"}}]}`)
	if got := Advisories(plain, []string{"clash"}); got != nil {
		t.Errorf("warned about a TLS inbound: %v", got)
	}
	if got := Advisories(realityConfig("nonsense"), []string{"clash"}); got != nil {
		t.Errorf("guessed at a malformed version: %v", got)
	}
	if got := Advisories([]byte("not json"), []string{"clash"}); got != nil {
		t.Errorf("warned about unparseable config: %v", got)
	}
}
