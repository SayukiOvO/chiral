package equivalence

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/subscription"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

const (
	exportTestKey      = "equivalence-export-test-key-0123456789"
	exportPublicURL    = "https://panel.example.test"
	exportRevision     = "renderer-test-revision"
	exportStoredConfig = `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[]}`
)

var exportAt = time.Date(2032, time.March, 4, 5, 6, 7, 0, time.UTC)

type exportFixtureOptions struct {
	ruleBaseURL       string
	noRuleset         bool
	missingToken      string
	missingCredential string
	omitTemplate      string
	missingRuleCache  bool
}

type exportFixture struct {
	path        string
	box         *secret.Box
	node        store.Node
	profile     store.Profile
	users       map[string]store.User
	credentials map[string]store.Credential
}

func buildExportFixture(t *testing.T, opts exportFixtureOptions) exportFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "equivalence.db")
	box, err := secret.NewBox(exportTestKey)
	if err != nil {
		t.Fatalf("create fixture secret box: %v", err)
	}
	st, err := store.Open(path, box)
	if err != nil {
		t.Fatalf("open fixture store: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = st.Close()
		}
	})

	node, err := st.CreateNode("internal-tokyo-01", auth.HashSecret("join-token"))
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.UpdateNode(node.ID, node.Name, "Tokyo 01", "203.0.113.10"); err != nil {
		t.Fatalf("set node subscription metadata: %v", err)
	}
	node, err = st.GetNode(node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}

	profile, err := st.CreateProfile("vless-main")
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profile.InboundTemplate = `{
  "tag": "vless-main",
  "listen": "0.0.0.0",
  "port": 443,
  "protocol": "vless",
  "settings": {"clients": [], "decryption": "none"}
}`
	profile.ClientEntry = `{"id":"{{user.uuid}}","email":"{{user.email}}"}`
	if err := st.UpdateProfile(profile); err != nil {
		t.Fatalf("update profile templates: %v", err)
	}
	if err := st.BindProfileNode(profile.ID, node.ID); err != nil {
		t.Fatalf("bind profile to node: %v", err)
	}
	templates := map[string]string{
		subscription.ClientXrayJSON: `{"tag":"{{node.display_name}}","protocol":"vless","settings":{"vnext":[{"address":"{{node.address}}","port":443,"users":[{"id":"{{user.uuid}}","encryption":"none"}]}]}}`,
		subscription.ClientClash:    "name: \"{{node.display_name}}\"\ntype: vless\nserver: \"{{node.address}}\"\nport: 443\nuuid: \"{{user.uuid}}\"\ntls: true",
		subscription.ClientVlessURI: `vless://{{user.uuid}}@{{node.address}}:443?encryption=none#{{node.display_name}}`,
		subscription.ClientStash:    "name: \"{{node.display_name}}\"\ntype: vless\nserver: \"{{node.address}}\"\nport: 443\nuuid: \"{{user.uuid}}\"\ntls: true",
	}
	for _, kind := range subscription.Kinds() {
		if kind == opts.omitTemplate {
			continue
		}
		if err := st.PutClientTemplate(profile.ID, kind, templates[kind]); err != nil {
			t.Fatalf("store %s client template: %v", kind, err)
		}
	}

	ruleBase := opts.ruleBaseURL
	if ruleBase == "" {
		ruleBase = "https://rules.example.test"
	}
	listURL := ruleBase + "/chiral.list"
	rules, err := st.CreateRuleset("cached-rules", "", ruleBase+"/preset.ini")
	if err != nil {
		t.Fatalf("create ruleset: %v", err)
	}
	ini := "custom_proxy_group=Proxy`select`.*\n" +
		"ruleset=Proxy," + listURL + "\n" +
		"ruleset=Proxy,[]FINAL\n"
	if err := st.SaveRulesetFetch(rules.ID, ini, ""); err != nil {
		t.Fatalf("store ruleset source: %v", err)
	}
	if !opts.missingRuleCache {
		if err := st.PutRuleList(store.RuleList{
			URL:  listURL,
			Body: "DOMAIN-SUFFIX,example.com\nIP-CIDR,192.0.2.0/24,no-resolve\n",
			ETag: `"fixture-v1"`,
		}); err != nil {
			t.Fatalf("store ruleset list: %v", err)
		}
	}

	specs := []struct {
		key    string
		name   string
		user   store.User
		token  string
		secret string
	}{
		{
			key: "healthy", name: "01 healthy", token: "subscription-token-healthy",
			secret: "00000000-0000-4000-8000-000000000001",
			user:   store.User{Enabled: true},
		},
		{
			key: "disabled", name: "02 disabled", token: "subscription-token-disabled",
			secret: "00000000-0000-4000-8000-000000000002",
			user:   store.User{Enabled: false},
		},
		{
			key: "expired", name: "03 expired", token: "subscription-token-expired",
			secret: "00000000-0000-4000-8000-000000000003",
			user:   store.User{Enabled: true, ExpiresAt: exportAt.Add(-time.Second).Unix()},
		},
		{
			key: "overquota", name: "04 over quota", token: "subscription-token-overquota",
			secret: "00000000-0000-4000-8000-000000000004",
			user:   store.User{Enabled: true, QuotaBytes: 1},
		},
	}
	fixture := exportFixture{
		path: path, box: box, node: node, profile: profile,
		users: make(map[string]store.User), credentials: make(map[string]store.Credential),
	}
	for i, spec := range specs {
		spec.user.Name = spec.name
		var created store.User
		if spec.key == opts.missingToken {
			created, err = st.CreateUser(spec.user, auth.HashSecret(spec.token))
		} else {
			created, err = st.CreateUserWithToken(spec.user, spec.token, auth.HashSecret(spec.token))
		}
		if err != nil {
			t.Fatalf("create %s user: %v", spec.key, err)
		}
		if err := st.BindUserProfile(created.ID, profile.ID); err != nil {
			t.Fatalf("bind %s user to profile: %v", spec.key, err)
		}
		if err := st.SetUserNodeAccess(created.ID, nil, nil, nil); err != nil {
			t.Fatalf("allow node for %s user: %v", spec.key, err)
		}
		if !opts.noRuleset {
			if err := st.SetUserRuleset(created.ID, rules.ID); err != nil {
				t.Fatalf("assign ruleset to %s user: %v", spec.key, err)
			}
		}
		if spec.key != opts.missingCredential {
			credential, putErr := st.PutCredential(store.Credential{
				UserID: created.ID, ProfileID: profile.ID, NodeID: node.ID,
				Email: fmt.Sprintf("fixture-%d@equivalence.test", i+1), Secret: spec.secret,
			})
			if putErr != nil {
				t.Fatalf("store %s credential: %v", spec.key, putErr)
			}
			fixture.credentials[spec.key] = credential
			if spec.key == "overquota" {
				if err := st.AddCredentialTraffic(credential.Email, 2, 0); err != nil {
					t.Fatalf("exhaust quota: %v", err)
				}
			}
		}
		created, err = st.GetUser(created.ID)
		if err != nil {
			t.Fatalf("reload %s user: %v", spec.key, err)
		}
		fixture.users[spec.key] = created
	}
	if _, err := st.InsertConfig(node.ID, exportStoredConfig, ""); err != nil {
		t.Fatalf("store node config: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close fixture store: %v", err)
	}
	closed = true
	assertNoSQLiteSidecars(t, path)
	return fixture
}

func openExportSnapshot(t *testing.T, fixture exportFixture) *store.Store {
	t.Helper()
	st, err := store.OpenSnapshot(fixture.path, fixture.box)
	if err != nil {
		t.Fatalf("open read-only snapshot: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close read-only snapshot: %v", err)
		}
	})
	return st
}

func exportOptions(at time.Time) Options {
	return Options{At: at, PublicURL: exportPublicURL, Revision: exportRevision}
}

func findExportRecord(t *testing.T, bundle Bundle, kind, id, client string) Record {
	t.Helper()
	for _, record := range bundle.Records {
		if record.Kind == kind && record.ID == id && record.Client == client {
			return record
		}
	}
	t.Fatalf("record %q/%q/%q was not exported", kind, id, client)
	return Record{}
}

func recordHasIssue(record Record, issue string) bool {
	for _, got := range record.Issues {
		if got == issue {
			return true
		}
	}
	return false
}

func assertNoSQLiteSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("snapshot has SQLite sidecar %s: %v", suffix, err)
		}
	}
}

type rejectingRoundTripper struct{ requests *atomic.Int64 }

func (r rejectingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.requests.Add(1)
	return nil, fmt.Errorf("network access is forbidden during offline export")
}

func TestExportProducesCompleteDeterministicOfflineEvidence(t *testing.T) {
	var requests atomic.Int64
	previousTransport := http.DefaultTransport
	http.DefaultTransport = rejectingRoundTripper{requests: &requests}
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	fixture := buildExportFixture(t, exportFixtureOptions{ruleBaseURL: "https://network-must-not-be-used.invalid"})
	before, err := os.ReadFile(fixture.path)
	if err != nil {
		t.Fatalf("read source before export: %v", err)
	}
	beforeInfo, err := os.Stat(fixture.path)
	if err != nil {
		t.Fatalf("stat source before export: %v", err)
	}
	snapshot := openExportSnapshot(t, fixture)

	bundle, err := Export(snapshot, exportOptions(exportAt))
	if err != nil {
		t.Fatalf("export complete snapshot: %v", err)
	}
	again, err := Export(snapshot, exportOptions(exportAt))
	if err != nil {
		t.Fatalf("repeat complete export: %v", err)
	}
	if !bundle.Complete() {
		t.Fatalf("complete fixture produced incomplete evidence: %+v", bundle)
	}
	comparison := Compare(bundle, again)
	if !comparison.Equal || !comparison.Complete || !comparison.Passed || comparison.Differences != 0 {
		t.Fatalf("identical complete exports did not pass: %+v", comparison)
	}
	if bundle.Version != 1 || bundle.At != exportAt.Unix() || bundle.PublicURL != exportPublicURL || bundle.Revision != exportRevision {
		t.Fatalf("unexpected bundle provenance: %+v", bundle)
	}
	if bundle.Users != len(fixture.users) || bundle.Nodes != 1 {
		t.Fatalf("unexpected inventory: users=%d nodes=%d", bundle.Users, bundle.Nodes)
	}
	sum := sha256.Sum256(before)
	if bundle.DatabaseSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("database digest = %q, want digest of exact snapshot", bundle.DatabaseSHA256)
	}

	status := map[string]string{
		"healthy":   "",
		"disabled":  user.ReasonSuspended,
		"expired":   user.ReasonExpired,
		"overquota": user.ReasonQuotaExhausted,
	}
	subscriptions := 0
	for state, subscriber := range fixture.users {
		for _, kind := range subscription.Kinds() {
			record := findExportRecord(t, bundle, "subscription", subscriber.ID, kind)
			subscriptions++
			if len(record.Issues) != 0 || record.Fragments != 1 || len(record.Body) == 0 {
				t.Errorf("%s/%s subscription is not complete: %+v", state, kind, record)
			}
			if len(record.TokenSHA256) != 64 {
				t.Errorf("%s/%s token identity length = %d, want 64", state, kind, len(record.TokenSHA256))
			}
			credential := fixture.credentials[state]
			if !bytes.Contains(record.Body, []byte(credential.Secret)) {
				t.Errorf("%s/%s subscription omitted its preserved credential", state, kind)
			}
			headers := http.Header(record.Headers)
			if got := headers.Get("Subscription-Status"); got != status[state] {
				t.Errorf("%s/%s status = %q, want %q at fixed time", state, kind, got, status[state])
			}
			if headers.Get("Content-Type") == "" || headers.Get("Content-Disposition") == "" || headers.Get("Subscription-Userinfo") == "" {
				t.Errorf("%s/%s is missing successful response headers: %v", state, kind, record.Headers)
			}
		}
	}
	if subscriptions != len(fixture.users)*len(subscription.Kinds()) {
		t.Fatalf("subscription records = %d, want every user x client", subscriptions)
	}

	// Rule record clients are content-derived hashes, so inspect them by kind
	// and user rather than duplicating that implementation in this test.
	rulesByUser := make(map[string]int)
	for _, record := range bundle.Records {
		if record.Kind != "rule_payload" {
			continue
		}
		rulesByUser[record.ID]++
		if len(record.Issues) != 0 || !bytes.Contains(record.Body, []byte("DOMAIN-SUFFIX,example.com")) {
			t.Errorf("invalid cached rule payload: %+v", record)
		}
	}
	for _, subscriber := range fixture.users {
		if rulesByUser[subscriber.ID] != 1 {
			t.Errorf("rule payload records for %s = %d, want 1", subscriber.Name, rulesByUser[subscriber.ID])
		}
	}

	rendered := findExportRecord(t, bundle, "node_rendered", fixture.node.ID, "")
	if len(rendered.Issues) != 0 || len(rendered.Body) == 0 {
		t.Fatalf("rendered node evidence is incomplete: %+v", rendered)
	}
	if !bytes.Contains(rendered.Body, []byte(fixture.credentials["healthy"].Email)) {
		t.Error("rendered node omitted the user allowed at the fixed observation time")
	}
	for _, state := range []string{"disabled", "expired", "overquota"} {
		if bytes.Contains(rendered.Body, []byte(fixture.credentials[state].Email)) {
			t.Errorf("rendered node included %s user at the fixed observation time", state)
		}
	}
	stored := findExportRecord(t, bundle, "node_stored", fixture.node.ID, "")
	if stored.Version != 1 || string(stored.Body) != exportStoredConfig || len(stored.Issues) != 0 {
		t.Fatalf("unexpected stored node evidence: %+v", stored)
	}

	// The same sealed database rendered at a time before the expiry boundary
	// must include that user in the node config and omit the expiry status.
	earlierAt := exportAt.Add(-time.Minute)
	earlier, err := Export(snapshot, exportOptions(earlierAt))
	if err != nil {
		t.Fatalf("export at earlier fixed time: %v", err)
	}
	earlierNode := findExportRecord(t, earlier, "node_rendered", fixture.node.ID, "")
	if !bytes.Contains(earlierNode.Body, []byte(fixture.credentials["expired"].Email)) {
		t.Error("earlier fixed time did not restore the not-yet-expired user")
	}
	earlierSub := findExportRecord(t, earlier, "subscription", fixture.users["expired"].ID, subscription.ClientXrayJSON)
	if got := http.Header(earlierSub.Headers).Get("Subscription-Status"); got != "" {
		t.Errorf("not-yet-expired user has status %q at earlier fixed time", got)
	}
	if changed := Compare(bundle, earlier); changed.Passed || changed.Differences == 0 {
		t.Fatalf("different observation times compared as equivalent: %+v", changed)
	}

	after, err := os.ReadFile(fixture.path)
	if err != nil {
		t.Fatalf("read source after export: %v", err)
	}
	afterInfo, err := os.Stat(fixture.path)
	if err != nil {
		t.Fatalf("stat source after export: %v", err)
	}
	if !bytes.Equal(before, after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("offline export changed its source database")
	}
	assertNoSQLiteSidecars(t, fixture.path)
	if got := requests.Load(); got != 0 {
		t.Fatalf("offline export made %d network requests", got)
	}
}

func TestCompareDetectsSubscriptionTokenRotationWithoutRulesetURLs(t *testing.T) {
	fixture := buildExportFixture(t, exportFixtureOptions{noRuleset: true})
	rotatedPath := filepath.Join(t.TempDir(), "rotated.db")
	body, err := os.ReadFile(fixture.path)
	if err != nil {
		t.Fatalf("read source snapshot: %v", err)
	}
	if err := os.WriteFile(rotatedPath, body, 0600); err != nil {
		t.Fatalf("copy source snapshot: %v", err)
	}
	mutable, err := store.Open(rotatedPath, fixture.box)
	if err != nil {
		t.Fatalf("open copied snapshot for fixture mutation: %v", err)
	}
	newToken := "rotated-subscription-token"
	if err := mutable.ResetSubToken(fixture.users["healthy"].ID, newToken, auth.HashSecret(newToken)); err != nil {
		_ = mutable.Close()
		t.Fatalf("rotate subscription token in copied fixture: %v", err)
	}
	if err := mutable.Close(); err != nil {
		t.Fatalf("close rotated fixture: %v", err)
	}
	assertNoSQLiteSidecars(t, rotatedPath)

	before := openExportSnapshot(t, fixture)
	after, err := store.OpenSnapshot(rotatedPath, fixture.box)
	if err != nil {
		t.Fatalf("open rotated snapshot: %v", err)
	}
	t.Cleanup(func() { _ = after.Close() })
	left, err := Export(before, exportOptions(exportAt))
	if err != nil {
		t.Fatalf("export original token snapshot: %v", err)
	}
	right, err := Export(after, exportOptions(exportAt))
	if err != nil {
		t.Fatalf("export rotated token snapshot: %v", err)
	}
	if !left.Complete() || !right.Complete() {
		t.Fatal("token comparison fixtures were not otherwise complete")
	}
	leftRecord := findExportRecord(t, left, "subscription", fixture.users["healthy"].ID, subscription.ClientXrayJSON)
	rightRecord := findExportRecord(t, right, "subscription", fixture.users["healthy"].ID, subscription.ClientXrayJSON)
	if !bytes.Equal(leftRecord.Body, rightRecord.Body) {
		t.Fatal("no-ruleset subscription body unexpectedly exposed the token rotation")
	}
	if leftRecord.TokenSHA256 == rightRecord.TokenSHA256 || len(leftRecord.TokenSHA256) != 64 || len(rightRecord.TokenSHA256) != 64 {
		t.Fatalf("subscription token identities were not exported distinctly: %q %q", leftRecord.TokenSHA256, rightRecord.TokenSHA256)
	}
	comparison := Compare(left, right)
	if comparison.Equal || !comparison.Complete || comparison.Passed || comparison.Differences == 0 {
		t.Fatalf("token rotation compared as equivalent: %+v", comparison)
	}
}

func TestExportRejectsSnapshotWithUnreadableSettings(t *testing.T) {
	fixture := buildExportFixture(t, exportFixtureOptions{})
	db, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatalf("open fixture with raw SQLite connection: %v", err)
	}
	if _, err := db.Exec("DROP TABLE settings"); err != nil {
		_ = db.Close()
		t.Fatalf("remove settings table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw SQLite connection: %v", err)
	}
	assertNoSQLiteSidecars(t, fixture.path)
	snapshot := openExportSnapshot(t, fixture)
	if bundle, err := Export(snapshot, exportOptions(exportAt)); err == nil {
		t.Fatalf("snapshot with unreadable settings was exported: %+v", bundle)
	} else if err.Error() != "cannot read snapshot settings" {
		t.Fatalf("unexpected settings preflight error: %q", err)
	}
}

func TestExportRejectsMutableStore(t *testing.T) {
	box, err := secret.NewBox(exportTestKey)
	if err != nil {
		t.Fatalf("create secret box: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "mutable.db"), box)
	if err != nil {
		t.Fatalf("open mutable store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if bundle, err := Export(st, exportOptions(exportAt)); err == nil {
		t.Fatalf("mutable store was exported: %+v", bundle)
	} else if err.Error() != "a read-only snapshot is required" {
		t.Fatalf("unexpected mutable-store rejection: %q", err)
	}
}

func TestExportMarksIncompleteEvidenceAndCompareFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		opts       exportFixtureOptions
		recordKind string
		userState  string
		client     string
		issue      string
	}{
		{
			name: "missing subscription token", opts: exportFixtureOptions{missingToken: "healthy"},
			recordKind: "subscription", userState: "healthy", client: subscription.ClientXrayJSON,
			issue: "subscription_token_unavailable",
		},
		{
			name: "missing credential", opts: exportFixtureOptions{missingCredential: "healthy"},
			recordKind: "subscription", userState: "healthy", client: subscription.ClientXrayJSON,
			issue: "credential_inventory_incomplete",
		},
		{
			name: "empty offered client", opts: exportFixtureOptions{omitTemplate: subscription.ClientStash},
			recordKind: "subscription", userState: "healthy", client: subscription.ClientStash,
			issue: "empty_subscription",
		},
		{
			name: "missing cached rule list", opts: exportFixtureOptions{missingRuleCache: true},
			recordKind: "rule_payload", userState: "healthy", issue: "rule_cache_incomplete",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := buildExportFixture(t, tc.opts)
			snapshot := openExportSnapshot(t, fixture)
			bundle, err := Export(snapshot, exportOptions(exportAt))
			if err != nil {
				t.Fatalf("export incomplete fixture: %v", err)
			}
			if bundle.Complete() {
				t.Fatalf("%s produced complete evidence", tc.name)
			}
			var record Record
			if tc.recordKind == "rule_payload" {
				for _, candidate := range bundle.Records {
					if candidate.Kind == tc.recordKind && candidate.ID == fixture.users[tc.userState].ID {
						record = candidate
						break
					}
				}
				if record.Kind == "" {
					t.Fatalf("missing %s record for %s", tc.recordKind, tc.userState)
				}
			} else {
				record = findExportRecord(t, bundle, tc.recordKind, fixture.users[tc.userState].ID, tc.client)
			}
			if !recordHasIssue(record, tc.issue) {
				t.Fatalf("record issues = %v, want %q", record.Issues, tc.issue)
			}

			again, err := Export(snapshot, exportOptions(exportAt))
			if err != nil {
				t.Fatalf("repeat incomplete export: %v", err)
			}
			comparison := Compare(bundle, again)
			if !comparison.Equal || comparison.Complete || comparison.Passed || comparison.Differences != 0 {
				t.Fatalf("identical incomplete exports did not fail closed: %+v", comparison)
			}
			assertNoSQLiteSidecars(t, fixture.path)
		})
	}
}
