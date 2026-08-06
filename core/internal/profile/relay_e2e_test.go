package profile

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// Does a relayed connection actually come out at the far end?
//
// Everything else in this package checks the shape of a config. Shape is not
// the failure mode that matters here: an entry that quietly sends relayed
// traffic out of its own interface produces a config that assembles, validates
// and connects — the subscriber gets online, and only the exit address is
// wrong, which is the one thing they cannot see. So this test runs the two
// kernels for real and asks a destination who called it.
//
// The entry's default outbound is a blackhole, which is what makes the answer
// unambiguous in both directions: relayed traffic must arrive, and the same
// subscriber's DIRECT credential on the same inbound must not. Without the
// second half, "it worked" would also be the result of an entry that routes
// everything down the line regardless of who is asking.

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startXray writes a config and runs a kernel on it, failing the test with the
// kernel's own output if it will not come up.
func startXray(t *testing.T, name string, cfg []byte, port int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(xrayBin(), "run", "-c", path)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never listened on %d:\n%s\n%s", name, port, out.String(), cfg)
}

// plainRelay builds the arrangement in the clear: VLESS over TCP with no
// security layer. REALITY is covered by the assembly tests, and its server
// relays a handshake to a real host on the internet, which would make this
// test depend on something that has nothing to do with relaying.
func plainRelay(t *testing.T, svc *Service, st *store.Store, entryPort, exitPort int) (store.Profile, store.Node, store.Node, store.NodeRelay) {
	t.Helper()
	p, err := st.CreateProfile("plain")
	if err != nil {
		t.Fatal(err)
	}
	p.InboundTemplate = `{
	  "tag": "in",
	  "listen": "127.0.0.1",
	  "port": {{port}},
	  "protocol": "vless",
	  "settings": { "clients": [], "decryption": "none" },
	  "streamSettings": { "network": "tcp" }
	}`
	p.ClientEntry = `{"id":"{{user.uuid}}","email":"{{user.email}}"}`
	if err := st.UpdateProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := st.PutClientTemplate(p.ID, "xray-json", `{
	  "protocol": "vless",
	  "settings": { "vnext": [ { "address": "{{node.address}}", "port": {{port}},
	    "users": [ { "id": "{{user.uuid}}", "encryption": "none" } ] } ] },
	  "streamSettings": { "network": "tcp" }
	}`); err != nil {
		t.Fatal(err)
	}

	entry, err := st.CreateNode("entry", "join-hash-1")
	if err != nil {
		t.Fatal(err)
	}
	exit, err := st.CreateNode("exit", "join-hash-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []struct {
		id   string
		port int
	}{{entry.ID, entryPort}, {exit.ID, exitPort}} {
		if err := st.UpdateNode(n.id, "n"+n.id[:4], "", "127.0.0.1"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutVariable(store.Variable{
			Name: "port", Scope: store.ScopeNode,
			NodeID:     sql.NullString{String: n.id, Valid: true},
			Components: []store.Component{{Value: fmt.Sprint(n.port)}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.BindProfileNode(p.ID, n.id); err != nil {
			t.Fatal(err)
		}
	}
	// The entry cannot reach anything by itself. Whatever arrives at the
	// destination therefore came out of the exit, and nothing else can be
	// mistaken for that.
	if err := st.SetConfigSkeleton(entry.ID, `{
	  "log": { "loglevel": "warning" },
	  "inbounds": [],
	  "outbounds": [ { "protocol": "blackhole", "tag": "direct" } ]
	}`); err != nil {
		t.Fatal(err)
	}

	rl, err := st.CreateNodeRelay(store.NodeRelay{
		EntryNodeID: entry.ID, ExitNodeID: exit.ID, ProfileID: p.ID,
		Label: "线路 经由", Enabled: true,
		Secret: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, _ = st.GetNode(entry.ID)
	exit, _ = st.GetNode(exit.ID)
	return p, entry, exit, rl
}

// clientThrough runs a kernel whose only job is to offer an HTTP proxy on
// httpPort and send everything down the given outbound, and returns a client
// that speaks through it.
func clientThrough(t *testing.T, name string, outbound string, httpPort int) *http.Client {
	t.Helper()
	cfg := fmt.Sprintf(`{
	  "log": { "loglevel": "warning" },
	  "inbounds": [ { "tag": "http-in", "listen": "127.0.0.1", "port": %d, "protocol": "http" } ],
	  "outbounds": [ %s ]
	}`, httpPort, outbound)
	startXray(t, name, []byte(cfg), httpPort)
	proxy, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", httpPort))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Timeout:   8 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxy)},
	}
}

func TestRelayedTrafficReallyLeavesThroughTheExit(t *testing.T) {
	if xrayBin() == "" {
		t.Skip("no xray binary; this test needs real kernels")
	}
	svc, st, _ := newFixture(t)
	entryPort, exitPort := freePort(t), freePort(t)
	p, entry, exit, rl := plainRelay(t, svc, st, entryPort, exitPort)
	alice := entitle(t, st, "alice", p.ID, nil)
	if err := st.SetNodeRelayAccess(rl.ID, nil); err != nil {
		t.Fatal(err)
	}

	// Somewhere for the traffic to arrive. Bound to a loopback address so the
	// exit's freedom outbound can reach it without leaving the machine.
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "arrived")
	}))
	defer dest.Close()

	entryCfg, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	exitCfg, err := svc.AssembleNode(exit.ID)
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, "exit", exitCfg, exitPort)
	startXray(t, "entry", entryCfg, entryPort)

	// What the subscriber's client would dial, built the same way the
	// subscription builds it: their relayed credential rendered through the
	// entry's client template.
	relayCred, err := st.FindCredentialForExit(alice.ID, p.ID, entry.ID, "", rl.ID)
	if err != nil {
		t.Fatal(err)
	}
	directCred, err := st.FindCredential(alice.ID, p.ID, entry.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := st.ClientTemplates(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := svc.ClientContext(p.ID, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	render := func(c store.Credential) string {
		body, err := ctx.With(user.CredentialVars(c)).Render(tmpl["xray-json"])
		if err != nil {
			t.Fatal(err)
		}
		var ob map[string]any
		if err := json.Unmarshal([]byte(body), &ob); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(ob)
		return string(out)
	}

	via := clientThrough(t, "client-relay", render(relayCred), freePort(t))
	resp, err := via.Get(dest.URL)
	if err != nil {
		t.Fatalf("the relayed credential could not reach the destination: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "arrived" {
		t.Fatalf("relayed request returned %q", body)
	}

	// The control. Same person, same inbound, same entry — the only difference
	// is which credential they present, which is the only thing the routing
	// rule matches on. This one must hit the blackhole.
	// A blackhole closes the connection rather than refusing it, so the proxy
	// answers with a gateway error instead of failing the request outright:
	// the assertion is on what came back, not on whether anything did.
	direct := clientThrough(t, "client-direct", render(directCred), freePort(t))
	if resp, err := direct.Get(dest.URL); err == nil {
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(got) == "arrived" {
			t.Fatalf("the entry carried a non-relayed credential to the destination; "+
				"the rule matches more than the emails allowed on the line (status %d)", resp.StatusCode)
		}
	}
	_ = exit
}
