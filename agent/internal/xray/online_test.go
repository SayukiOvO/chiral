package xray

import "testing"

// The fixtures below are the verbatim output of Xray 26.3.27 on this machine,
// captured while a real proxied connection was open. Hand-written fixtures
// would only prove the parser matches what I imagined the format to be.

func TestParseOnlineRosterStripsTheStatKeyWrapper(t *testing.T) {
	// statsgetallonlineusers returns full stat keys, but statsonlineiplist
	// takes a bare email — passing the key straight through is the obvious
	// mistake, and it yields NotFound for every user.
	const out = `{
    "users": [
        "user>>>alice.u1@p1.n1>>>online"
    ]
}
`
	emails, err := parseOnlineRoster(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 || emails[0] != "alice.u1@p1.n1" {
		t.Fatalf("emails = %q, want [alice.u1@p1.n1]", emails)
	}
}

// Three causes produce this one output: the statsUserOnline policy is off,
// nobody is connected, or every source address was loopback. The parser cannot
// tell them apart and must not pretend to — it reports an empty roster and
// leaves the interpretation to Core.
func TestParseOnlineRosterAcceptsTheEmptyAnswer(t *testing.T) {
	emails, err := parseOnlineRoster("{}\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 0 {
		t.Fatalf("emails = %q, want none", emails)
	}
}

func TestParseOnlineIPs(t *testing.T) {
	const out = `{
    "ips": {
        "192.168.1.131": 1785068737
    },
    "name": "user>>>alice.u1@p1.n1>>>online"
}
`
	ips, err := parseOnlineIPs(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 {
		t.Fatalf("got %d addresses, want 1", len(ips))
	}
	if ips["192.168.1.131"] != 1785068737 {
		t.Fatalf("last-seen = %d, want 1785068737", ips["192.168.1.131"])
	}
}

// A user with connections but no reportable address (the loopback case) comes
// back with the name and no "ips" key at all, not with an empty object.
func TestParseOnlineIPsHandlesAMissingMap(t *testing.T) {
	const out = `{
    "name": "user>>>alice.u1@p1.n1>>>online"
}
`
	ips, err := parseOnlineIPs(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 0 {
		t.Fatalf("got %d addresses, want none", len(ips))
	}
}

// Log lines precede the JSON often enough that the other parsers in this file
// skip to the first brace; these must do the same.
func TestOnlineParsersSkipLeadingLogLines(t *testing.T) {
	const noise = "2026/07/26 20:19:12 [Info] something happened\n"
	if _, err := parseOnlineRoster(noise + `{"users":["user>>>a@b.c>>>online"]}`); err != nil {
		t.Errorf("roster: %v", err)
	}
	if _, err := parseOnlineIPs(noise + `{"ips":{"10.0.0.1":1}}`); err != nil {
		t.Errorf("iplist: %v", err)
	}
}

func TestOnlineParsersRejectNonJSON(t *testing.T) {
	if _, err := parseOnlineRoster("failed to get stats: rpc error\n"); err == nil {
		t.Error("roster: accepted an error message as output")
	}
	if _, err := parseOnlineIPs("failed to get stats: rpc error\n"); err == nil {
		t.Error("iplist: accepted an error message as output")
	}
}
