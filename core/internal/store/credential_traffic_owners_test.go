package store

import (
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/migrations"
)

type trafficOwnerRow struct {
	Email        string
	CredentialID string
	UserID       sql.NullString
	NodeID       string
	TrafficRate  float64
	UpBytes      int64
	DownBytes    int64
	RetiredAt    sql.NullInt64
}

func readTrafficOwner(t *testing.T, s *Store, email string) trafficOwnerRow {
	t.Helper()
	var row trafficOwnerRow
	err := s.db.QueryRow(`SELECT email, credential_id, user_id, node_id, traffic_rate,
		up_bytes, down_bytes, retired_at
		FROM credential_traffic_owners WHERE email = ?`, email).Scan(
		&row.Email, &row.CredentialID, &row.UserID, &row.NodeID, &row.TrafficRate,
		&row.UpBytes, &row.DownBytes, &row.RetiredAt)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestCredentialTrafficOwnerTracksLiveRateAndFreezesOnRetirement(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "owner-rate")
	p, err := s.CreateProfile("owner-rate-profile")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateNode("owner-rate-node", "owner-rate-token")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email: "owner-rate@example.invalid", Secret: "credential-secret-never-in-ledger",
	})
	if err != nil {
		t.Fatal(err)
	}

	owner := readTrafficOwner(t, s, c.Email)
	if owner.CredentialID != c.ID || !owner.UserID.Valid || owner.UserID.String != u.ID ||
		owner.NodeID != n.ID || owner.TrafficRate != 1 || owner.RetiredAt.Valid {
		t.Fatalf("unexpected live accounting owner: %+v", owner)
	}
	if _, err := s.db.Exec(`UPDATE credentials SET up_bytes = 17, down_bytes = 23 WHERE id = ?`, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeTrafficRate(n.ID, 2.5); err != nil {
		t.Fatal(err)
	}
	if got := readTrafficOwner(t, s, c.Email).TrafficRate; got != 2.5 {
		t.Fatalf("active cached rate = %v, want 2.5", got)
	}

	if err := s.DeleteCredentialsForBinding(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	retired := readTrafficOwner(t, s, c.Email)
	if !retired.RetiredAt.Valid || retired.TrafficRate != 2.5 || retired.UpBytes != 17 || retired.DownBytes != 23 {
		t.Fatalf("retirement did not freeze rate and counters: %+v", retired)
	}
	if err := s.SetNodeTrafficRate(n.ID, 9); err != nil {
		t.Fatal(err)
	}
	if got := readTrafficOwner(t, s, c.Email).TrafficRate; got != 2.5 {
		t.Fatalf("retired rate changed to %v", got)
	}

	rows, err := s.db.Query(`PRAGMA table_info(credential_traffic_owners)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "secret" {
			t.Fatal("accounting owner table must never contain credential secrets")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialTrafficOwnerSurvivesParentCascadesWithCachedRate(t *testing.T) {
	t.Run("direct node", func(t *testing.T) {
		s := testStore(t, storeTestKey)
		u := seedUser(t, s, "cascade-node")
		p, _ := s.CreateProfile("cascade-node-profile")
		n, _ := s.CreateNode("cascade-node", "cascade-node-token")
		if err := s.SetNodeTrafficRate(n.ID, 3.25); err != nil {
			t.Fatal(err)
		}
		c, err := s.PutCredential(Credential{
			UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
			Email: "cascade-node@example.invalid", Secret: "node-secret",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteNode(n.ID); err != nil {
			t.Fatal(err)
		}
		owner := readTrafficOwner(t, s, c.Email)
		if !owner.RetiredAt.Valid || owner.TrafficRate != 3.25 || owner.NodeID != n.ID {
			t.Fatalf("node cascade lost accounting owner: %+v", owner)
		}
	})

	t.Run("external proxy", func(t *testing.T) {
		s := testStore(t, storeTestKey)
		u := seedUser(t, s, "cascade-proxy")
		p, _ := s.CreateProfile("cascade-proxy-profile")
		n, _ := s.CreateNode("cascade-proxy-node", "cascade-proxy-token")
		sub, err := s.CreateExternalSub("cascade-sub", "", "proxy-body")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceExternalProxies(sub.ID, []ExternalProxy{{
			Name: "cascade-exit", Type: "vless", Server: "proxy.example.invalid", Port: 443,
			Config: "name: cascade-exit",
		}}); err != nil {
			t.Fatal(err)
		}
		proxies, err := s.ExternalProxies(sub.ID)
		if err != nil || len(proxies) != 1 {
			t.Fatalf("external proxies: len=%d err=%v", len(proxies), err)
		}
		proxy := proxies[0]
		c, err := s.PutCredential(Credential{
			UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, ExitProxyID: proxy.ID,
			Email: "cascade-proxy@example.invalid", Secret: "proxy-secret",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetExternalProxyTrafficRate(proxy.ID, 2.25); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteExternalSub(sub.ID); err != nil {
			t.Fatal(err)
		}
		owner := readTrafficOwner(t, s, c.Email)
		if !owner.RetiredAt.Valid || owner.TrafficRate != 2.25 || owner.NodeID != n.ID {
			t.Fatalf("proxy cascade lost accounting owner: %+v", owner)
		}
	})

	t.Run("node relay", func(t *testing.T) {
		s := testStore(t, storeTestKey)
		u := seedUser(t, s, "cascade-relay")
		p, _ := s.CreateProfile("cascade-relay-profile")
		entry, _ := s.CreateNode("cascade-relay-entry", "cascade-relay-entry-token")
		exit, _ := s.CreateNode("cascade-relay-exit", "cascade-relay-exit-token")
		relay, err := s.CreateNodeRelay(NodeRelay{
			EntryNodeID: entry.ID, ExitNodeID: exit.ID, ProfileID: p.ID,
			Label: "cascade relay", Secret: "relay-link-secret", Enabled: true, TrafficRate: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		c, err := s.PutCredential(Credential{
			UserID: u.ID, ProfileID: p.ID, NodeID: entry.ID, ExitRelayID: relay.ID,
			Email: "cascade-relay@example.invalid", Secret: "relay-user-secret",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateNodeRelay(relay.ID, relay.Label, true, 1.75); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteNodeRelay(relay.ID); err != nil {
			t.Fatal(err)
		}
		owner := readTrafficOwner(t, s, c.Email)
		if !owner.RetiredAt.Valid || owner.TrafficRate != 1.75 || owner.NodeID != entry.ID {
			t.Fatalf("relay cascade lost accounting owner: %+v", owner)
		}
	})
}

func TestCredentialTrafficOwnerClearsDeletedUserWithoutRestoringAccess(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "deleted-owner")
	p, _ := s.CreateProfile("deleted-owner-profile")
	n, _ := s.CreateNode("deleted-owner-node", "deleted-owner-token")
	c, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email: "deleted-owner@example.invalid", Secret: "deleted-owner-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	owner := readTrafficOwner(t, s, c.Email)
	if owner.UserID.Valid || !owner.RetiredAt.Valid || owner.NodeID != n.ID {
		t.Fatalf("deleted user retained or lost the wrong accounting identity: %+v", owner)
	}
	if _, err := s.GetUser(u.ID); !IsNotFound(err) {
		t.Fatalf("deleted user was restored: %v", err)
	}
	if _, _, err := s.CredentialOwner(c.Email); !IsNotFound(err) {
		t.Fatalf("retired accounting row became an authorization owner: %v", err)
	}
}

func TestRetiredCredentialEmailGetsFreshStatsKeyAndLegacyTrafficStillIgnoresIt(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "rebind-owner")
	p, _ := s.CreateProfile("rebind-owner-profile")
	n, _ := s.CreateNode("rebind-owner-node", "rebind-owner-token")
	baseEmail := "deterministic-rebind@example.invalid"
	first, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email: baseEmail, Secret: "first-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCredentialsForBinding(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	before := readTrafficOwner(t, s, baseEmail)
	if err := s.AddCredentialTraffic(baseEmail, 10, 20); err != nil {
		t.Fatal(err)
	}
	after := readTrafficOwner(t, s, baseEmail)
	if before.UpBytes != after.UpBytes || before.DownBytes != after.DownBytes {
		t.Fatalf("legacy traffic path changed retired counters: before=%+v after=%+v", before, after)
	}
	gotUser, err := s.GetUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotUser.UsedBytes != 0 {
		t.Fatalf("legacy traffic path charged a retired owner: used_bytes=%d", gotUser.UsedBytes)
	}

	second, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email: baseEmail, Secret: "second-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantEmail := fmt.Sprintf("credential-%s@chiral.invalid", second.ID)
	if second.ID == first.ID || second.Email != wantEmail || second.Email == baseEmail {
		t.Fatalf("retired deterministic key was reused: first=%+v second=%+v", first, second)
	}
	idempotent, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email: baseEmail, Secret: "must-not-replace-second-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if idempotent.ID != second.ID || idempotent.Email != second.Email || idempotent.Secret != second.Secret {
		t.Fatalf("rebound credential is not idempotent: second=%+v again=%+v", second, idempotent)
	}
	if _, _, err := s.CredentialOwner(baseEmail); !IsNotFound(err) {
		t.Fatalf("retired email resolved through live ownership: %v", err)
	}
	ownerUserID, ownerNodeID, err := s.CredentialOwner(second.Email)
	if err != nil || ownerUserID != u.ID || ownerNodeID != n.ID {
		t.Fatalf("new stats key owner = (%q, %q, %v)", ownerUserID, ownerNodeID, err)
	}
	profileIDs, err := s.UserProfileIDs(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(profileIDs) != 0 {
		t.Fatalf("minting a replacement credential restored access grants: %v", profileIDs)
	}

	otherNode, _ := s.CreateNode("rebind-other-node", "rebind-other-token")
	if _, err := s.db.Exec(`INSERT INTO credentials
		(id, user_id, profile_id, node_id, email, secret, created_at)
		VALUES ('forced-reuse', ?, ?, ?, ?, 'forced-secret', 1)`,
		u.ID, p.ID, otherNode.ID, baseEmail); err == nil {
		t.Fatal("SQL accepted reuse of a retired accounting email")
	}
	var liveReuse int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE email = ?`, baseEmail).Scan(&liveReuse); err != nil {
		t.Fatal(err)
	}
	if liveReuse != 0 {
		t.Fatalf("failed historical-email insert left %d live credential rows", liveReuse)
	}
}

func TestCredentialTrafficOwnerRatePriorityMatchesBilling(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "rate-priority")
	p, _ := s.CreateProfile("rate-priority-profile")
	entry, _ := s.CreateNode("rate-priority-entry", "rate-priority-entry-token")
	exit, _ := s.CreateNode("rate-priority-exit", "rate-priority-exit-token")
	sub, err := s.CreateExternalSub("rate-priority-sub", "", "body")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceExternalProxies(sub.ID, []ExternalProxy{{
		Name: "priority-proxy", Type: "vless", Server: "priority.example.invalid", Port: 443,
		Config: "name: priority-proxy",
	}}); err != nil {
		t.Fatal(err)
	}
	proxies, err := s.ExternalProxies(sub.ID)
	if err != nil || len(proxies) != 1 {
		t.Fatalf("external proxies: len=%d err=%v", len(proxies), err)
	}
	proxy := proxies[0]
	if err := s.SetExternalProxyTrafficRate(proxy.ID, 2); err != nil {
		t.Fatal(err)
	}
	relay, err := s.CreateNodeRelay(NodeRelay{
		EntryNodeID: entry.ID, ExitNodeID: exit.ID, ProfileID: p.ID,
		Label: "priority relay", Secret: "priority-link-secret", Enabled: true, TrafficRate: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The normal API never creates this shape, but the legacy schema permits
	// both exits. Match the billing query's COALESCE(proxy, relay, node) order
	// even for such a stored row.
	c, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: entry.ID,
		ExitProxyID: proxy.ID, ExitRelayID: relay.ID,
		Email: "priority@example.invalid", Secret: "priority-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readTrafficOwner(t, s, c.Email).TrafficRate; got != 2 {
		t.Fatalf("initial rate = %v, want proxy rate 2", got)
	}
	if err := s.UpdateNodeRelay(relay.ID, relay.Label, true, 5); err != nil {
		t.Fatal(err)
	}
	if got := readTrafficOwner(t, s, c.Email).TrafficRate; got != 2 {
		t.Fatalf("relay update overrode higher-priority proxy rate: %v", got)
	}
	if err := s.SetExternalProxyTrafficRate(proxy.ID, 7); err != nil {
		t.Fatal(err)
	}
	if got := readTrafficOwner(t, s, c.Email).TrafficRate; got != 7 {
		t.Fatalf("proxy update did not refresh cached rate: %v", got)
	}
}

func TestMigration0031PreservesPopulated0030Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-0030.db")
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	applyMigrationsThrough(t, db, "0030_acknowledged_stats.sql")
	seedPopulated0030Database(t, db)
	before := snapshotDatabaseTables(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for table, want := range before {
		got := snapshotDatabaseTable(t, s.db, table)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("migration changed rows in old table %s\nwant: %#v\n got: %#v", table, want, got)
		}
	}

	var migrationNames []string
	rows, err := s.db.Query(`SELECT name FROM schema_migrations ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		migrationNames = append(migrationNames, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	wantMigrations, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(wantMigrations)
	if !reflect.DeepEqual(migrationNames, wantMigrations) {
		t.Fatalf("schema migration records = %v, want %v", migrationNames, wantMigrations)
	}

	owner := readTrafficOwner(t, s, "legacy-accounting@example.invalid")
	if owner.CredentialID != "legacy-credential" || !owner.UserID.Valid || owner.UserID.String != "legacy-user" ||
		owner.NodeID != "legacy-node" || owner.TrafficRate != 4.25 || owner.UpBytes != 17 ||
		owner.DownBytes != 23 || owner.RetiredAt.Valid {
		t.Fatalf("0031 backfill did not preserve the active accounting identity: %+v", owner)
	}
}

func applyMigrationsThrough(t *testing.T, db *sql.DB, last string) []string {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	var applied []string
	for _, name := range names {
		if name > last {
			break
		}
		script, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(string(script)); err != nil {
			tx.Rollback()
			t.Fatalf("apply legacy migration %s: %v", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, 1700000000)`, name); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		applied = append(applied, name)
	}
	return applied
}

func seedPopulated0030Database(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`INSERT INTO nodes
			(id, name, join_token_hash, credential_hash, hostname, public_ip, agent_version,
			 xray_version, created_at, registered_at, last_seen_at, config_skeleton,
			 xray_installed_version, display_name, platform, address, sort_order, traffic_rate)
		 VALUES
			('legacy-node', 'legacy node', 'raw-join-token-hash', 'raw-node-credential-hash',
			 'legacy-host', '192.0.2.10', 'legacy-agent', 'v-old', 1700000001, 1700000002,
			 1700000003, 'raw-node-config-secret', 'v-installed', 'Legacy Display',
			 'linux/amd64', 'legacy.example.invalid', 7, 4.25)`,
		`INSERT INTO node_configs
			(node_id, version, config, created_at, applied, error, probe_outbound)
		 VALUES
			('legacy-node', 9, 'raw-versioned-config-secret', 1700000004, 1, '',
			 'raw-probe-outbound-secret')`,
		`INSERT INTO profiles
			(id, name, inbound_template, client_entry, created_at, updated_at)
		 VALUES
			('legacy-profile', 'legacy profile', '{"tag":"legacy-inbound"}',
			 '{"id":"{{credential.secret}}"}', 1700000005, 1700000006)`,
		`INSERT INTO profile_client_templates (profile_id, client, template, serve)
		 VALUES ('legacy-profile', 'xray-json', '{"outbounds":["raw-xray-template"]}', 1)`,
		`INSERT INTO profile_client_templates (profile_id, client, template, serve)
		 VALUES ('legacy-profile', 'clash', 'proxies: [raw-clash-template]', 0)`,
		`INSERT INTO variables
			(id, name, scope, generator, created_at, updated_at)
		 VALUES ('legacy-variable', 'legacy-key', 'global', 'x25519', 1700000007, 1700000008)`,
		`INSERT INTO variable_components (variable_id, component, value, secret)
		 VALUES ('legacy-variable', 'private', 'raw-variable-private-key', 1)`,
		`INSERT INTO users
			(id, name, sub_token_hash, sub_token_enc, quota_bytes, used_bytes, expires_at,
			 renew_period, enabled, active, device_limit, created_at, updated_at, ruleset_none)
		 VALUES
			('legacy-user', 'legacy user', 'raw-sub-token-hash', 'raw-sealed-sub-token',
			 1000000, 1234, 1800000000, 86400, 1, 1, 3, 1700000009, 1700000010, 1)`,
		`INSERT INTO user_profiles (user_id, profile_id)
		 VALUES ('legacy-user', 'legacy-profile')`,
		`INSERT INTO credentials
			(id, user_id, profile_id, node_id, exit_proxy_id, exit_relay_id, email, secret,
			 up_bytes, down_bytes, created_at)
		 VALUES
			('legacy-credential', 'legacy-user', 'legacy-profile', 'legacy-node', NULL, NULL,
			 'legacy-accounting@example.invalid', 'raw-credential-secret', 17, 23, 1700000011)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("seed populated 0030 database: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func snapshotDatabaseTables(t *testing.T, db *sql.DB) map[string][][]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'
		ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	out := make(map[string][][]string, len(names))
	for _, name := range names {
		out[name] = snapshotDatabaseTable(t, db, name)
	}
	return out
}

func snapshotDatabaseTable(t *testing.T, db *sql.DB, table string) [][]string {
	t.Helper()
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := db.Query(`SELECT * FROM ` + quoted + ` ORDER BY rowid`)
	if err != nil {
		t.Fatalf("snapshot table %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]string, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		normalized := make([]string, len(values))
		for i, value := range values {
			normalized[i] = fmt.Sprintf("%T:%v", value, value)
		}
		out = append(out, normalized)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
