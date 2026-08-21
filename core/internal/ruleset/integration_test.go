package ruleset

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// A ruleset from fetch to rendered subscription, against a server standing in
// for GitHub. The parser and renderer are unit-tested; what this pins down is
// the part between them — that a refresh stores what a render reads, and that
// a provider name in the config resolves to the list it was made from.
func fixture(t *testing.T) (*store.Store, *Service, *httptest.Server, store.User) {
	t.Helper()
	box, err := secret.NewBox("ruleset-test-key-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	mux := http.NewServeMux()
	mux.HandleFunc("/preset.ini", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Write([]byte(
			"custom_proxy_group=🚀 节点选择`select`[]♻️ 自动选择`[]DIRECT`.*\n" +
				"custom_proxy_group=♻️ 自动选择`url-test`.*`http://x/`300,,50\n" +
				"custom_proxy_group=🎯 全球直连`select`[]DIRECT\n" +
				"ruleset=🎯 全球直连," + base + "/china.list\n" +
				"ruleset=🎯 全球直连,[]GEOIP,CN\n" +
				"ruleset=🚀 节点选择,[]FINAL\n"))
	})
	mux.HandleFunc("/china.list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write([]byte("# 直连\nDOMAIN-SUFFIX,taobao.com\nIP-CIDR,10.0.0.0/8,no-resolve\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u, err := st.CreateUser(store.User{Name: "sub", Enabled: true}, "sub-hash")
	if err != nil {
		t.Fatal(err)
	}
	return st, NewService(st, "", slog.New(slog.NewTextHandler(io.Discard, nil))), srv, u
}

func TestRefreshThenRender(t *testing.T) {
	st, svc, srv, u := fixture(t)
	rs, err := st.CreateRuleset("test", "", srv.URL+"/preset.ini")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Refresh(context.Background(), rs.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := st.SetUserRuleset(u.ID, rs.ID); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(u.ID)

	groups, providers, rules, err := svc.For(u, []string{"tokyo 01"}, "https://p.example/sub/T/rules")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if !strings.Contains(groups, "🚀 节点选择") || !strings.Contains(groups, "- tokyo 01") {
		t.Errorf("groups:\n%s", groups)
	}
	if !strings.Contains(providers, "https://p.example/sub/T/rules/china.yaml") {
		t.Errorf("providers:\n%s", providers)
	}
	if !strings.Contains(rules, "RULE-SET,china,🎯 全球直连") ||
		!strings.Contains(rules, "GEOIP,CN,🎯 全球直连") ||
		!strings.Contains(rules, "MATCH,🚀 节点选择") {
		t.Errorf("rules:\n%s", rules)
	}

	// The provider the config names must resolve to the list it was built
	// from — for this subscriber, whose ruleset decides the mapping.
	body, ok, err := svc.ListFor(u, "china")
	if err != nil || !ok {
		t.Fatalf("ListFor: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(body, "- 'DOMAIN-SUFFIX,taobao.com'") ||
		!strings.Contains(body, "no-resolve") || strings.Contains(body, "# 直连") {
		t.Errorf("provider body:\n%s", body)
	}
	if _, ok, _ := svc.ListFor(u, "nope"); ok {
		t.Error("resolved a provider name the ruleset does not define")
	}
}

// The stored copy is what renders. A panel that cannot reach upstream must
// keep serving the rules it already has rather than serving none.
func TestRenderSurvivesUpstreamGoingAway(t *testing.T) {
	st, svc, srv, u := fixture(t)
	rs, _ := st.CreateRuleset("test", "", srv.URL+"/preset.ini")
	if err := svc.Refresh(context.Background(), rs.ID); err != nil {
		t.Fatal(err)
	}
	st.SetUserRuleset(u.ID, rs.ID)
	u, _ = st.GetUser(u.ID)
	srv.Close()

	groups, _, rules, err := svc.For(u, []string{"tokyo 01"}, "https://p.example/sub/T/rules")
	if err != nil {
		t.Fatalf("For after upstream went away: %v", err)
	}
	if groups == "" || !strings.Contains(rules, "MATCH,") {
		t.Fatal("rendered nothing from the cached copy")
	}
	// And a refresh that fails leaves the previous copy alone.
	_ = svc.Refresh(context.Background(), rs.ID)
	after, _ := st.GetRuleset(rs.ID)
	if after.INI == "" {
		t.Fatal("a failed refresh cleared the stored config")
	}
	if after.LastError == "" {
		t.Fatal("a failed refresh was not reported")
	}
}

func TestNoRulesetRendersNothing(t *testing.T) {
	_, svc, _, u := fixture(t)
	groups, _, _, err := svc.For(u, []string{"a"}, "https://p/x")
	if err != nil || groups != "" {
		t.Fatalf("groups=%q err=%v, want empty and no error", groups, err)
	}
}

// A member of a group with a rule set renders that group's rules.
//
// The subscription used to read users.ruleset_id directly, so a member who had
// chosen nothing for themselves — which after joining a group is every member —
// got a subscription with no routing rules at all while the console reported
// the group's. Rendering resolves the same way everything else does: their own
// choice, else none if they refused it, else their group's.
func TestAGroupsRuleSetReachesTheRender(t *testing.T) {
	st, svc, srv, u := fixture(t)
	rs, err := st.CreateRuleset("test", "", srv.URL+"/preset.ini")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Refresh(context.Background(), rs.ID); err != nil {
		t.Fatal(err)
	}
	g, err := st.CreateSubscriberGroup("staff", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupRuleset(g.ID, rs.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserGroup(u.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(u.ID)
	if u.RulesetID != "" {
		t.Fatalf("fixture: the member should have chosen nothing of their own, got %q", u.RulesetID)
	}

	groups, _, rules, err := svc.For(u, []string{"tokyo 01"}, "https://p.example/sub/T/rules")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if !strings.Contains(groups, "🚀 节点选择") || !strings.Contains(rules, "MATCH,🚀 节点选择") {
		t.Errorf("the group's rules did not reach the render:\ngroups:\n%s\nrules:\n%s", groups, rules)
	}
	if _, ok, err := svc.ListFor(u, "china"); err != nil || !ok {
		t.Errorf("the group's rule providers do not resolve: ok=%v err=%v", ok, err)
	}

	// And a member excused from their group's rules renders none, which an
	// empty ruleset_id alone could not express.
	if err := st.SetUserRulesetNone(u.ID, true); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(u.ID)
	groups, _, rules, err = svc.For(u, []string{"tokyo 01"}, "https://p.example/sub/T/rules")
	if err != nil {
		t.Fatal(err)
	}
	if groups != "" || rules != "" {
		t.Errorf("an excused member still received rules:\ngroups:\n%s\nrules:\n%s", groups, rules)
	}
}
