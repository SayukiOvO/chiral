package passkey

import (
	"encoding/json"
	"strings"
	"testing"
)

// The relying-party id is what stops a phishing site from replaying a
// credential, so deriving it from the panel's URL has to be exact — and a URL
// that browsers will refuse must fail here, loudly, rather than at the moment
// someone tries to use their key.
func TestRelyingPartyDerivation(t *testing.T) {
	cases := []struct {
		url       string
		wantRPID  string
		wantOrig  string
		wantError string
	}{
		{url: "https://panel.example.com", wantRPID: "panel.example.com", wantOrig: "https://panel.example.com"},
		{url: "https://panel.example.com:8443", wantRPID: "panel.example.com", wantOrig: "https://panel.example.com:8443"},
		// Localhost is a secure context, so plain http is allowed there.
		{url: "http://localhost:5173", wantRPID: "localhost", wantOrig: "http://localhost:5173"},
		{url: "http://127.0.0.1:8080", wantRPID: "127.0.0.1", wantOrig: "http://127.0.0.1:8080"},
		// Anything else over http is not, and browsers would refuse.
		{url: "http://panel.example.com", wantError: "https"},
		{url: "", wantError: "not set"},
		{url: "ftp://example.com", wantError: "http"},
		{url: "https://", wantError: "no host"},
	}
	for _, tc := range cases {
		rpID, origin, err := parseRP(tc.url)
		if tc.wantError != "" {
			if err == nil {
				t.Errorf("%q: expected an error mentioning %q", tc.url, tc.wantError)
			} else if !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("%q: error %q does not mention %q", tc.url, err, tc.wantError)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.url, err)
			continue
		}
		if rpID != tc.wantRPID {
			t.Errorf("%q: rpID = %q, want %q", tc.url, rpID, tc.wantRPID)
		}
		if origin != tc.wantOrig {
			t.Errorf("%q: origin = %q, want %q", tc.url, origin, tc.wantOrig)
		}
	}
}

// The RP id must be the bare host: including the port would make a credential
// registered on :8443 unusable from :443 and vice versa.
func TestRPIDExcludesThePort(t *testing.T) {
	rpID, _, err := parseRP("https://panel.example.com:8443")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rpID, ":") {
		t.Errorf("rpID carries a port: %q", rpID)
	}
}

func TestNewRejectsAnUnusableURL(t *testing.T) {
	if _, err := New("http://panel.example.com", "Chiral"); err == nil {
		t.Error("a URL browsers would refuse was accepted")
	}
	svc, err := New("https://panel.example.com", "Chiral")
	if err != nil {
		t.Fatal(err)
	}
	if svc.RPID() != "panel.example.com" {
		t.Errorf("RPID() = %q", svc.RPID())
	}
}

// Registration must produce options the browser can actually use: a challenge,
// the relying party, and the user handle.
func TestBeginRegistrationProducesUsableOptions(t *testing.T) {
	svc, err := New("https://panel.example.com", "Chiral")
	if err != nil {
		t.Fatal(err)
	}
	options, session, err := svc.BeginRegistration(User{
		ID: "admin-1", Name: "mai", DisplayName: "Mai",
	})
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Response struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"rp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &parsed); err != nil {
		t.Fatalf("options are not the expected shape: %v\n%s", err, options)
	}
	if parsed.Response.Challenge == "" {
		t.Error("no challenge in the creation options")
	}
	if parsed.Response.RP.ID != "panel.example.com" {
		t.Errorf("rp.id = %q", parsed.Response.RP.ID)
	}
	if parsed.Response.User.ID == "" {
		t.Error("no user handle in the creation options")
	}

	// The session carries the challenge; without persisting it there is
	// nothing to verify the response against.
	if len(session) == 0 || !strings.Contains(string(session), "challenge") {
		t.Errorf("session data does not carry the challenge: %s", session)
	}
}

// Two ceremonies must not share a challenge, or one could be replayed for the
// other.
func TestChallengesAreUnique(t *testing.T) {
	svc, _ := New("https://panel.example.com", "Chiral")
	u := User{ID: "admin-1", Name: "mai", DisplayName: "Mai"}
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		_, session, err := svc.BeginRegistration(u)
		if err != nil {
			t.Fatal(err)
		}
		var sd struct {
			Challenge string `json:"challenge"`
		}
		json.Unmarshal(session, &sd)
		if sd.Challenge == "" {
			t.Fatal("empty challenge")
		}
		if seen[sd.Challenge] {
			t.Fatal("a challenge repeated")
		}
		seen[sd.Challenge] = true
	}
}

// A garbage or replayed response must be refused rather than accepted with a
// shrug — this is the whole point of delegating the protocol.
func TestFinishRegistrationRejectsNonsense(t *testing.T) {
	svc, _ := New("https://panel.example.com", "Chiral")
	u := User{ID: "admin-1", Name: "mai", DisplayName: "Mai"}
	_, session, err := svc.BeginRegistration(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{}`,
		`{"id":"x","rawId":"eA","type":"public-key","response":{}}`,
		`not json at all`,
	} {
		if _, err := svc.FinishRegistration(u, session, json.RawMessage(body)); err == nil {
			t.Errorf("accepted a bogus attestation: %s", body)
		}
	}
}

func TestFinishRejectsACorruptStoredChallenge(t *testing.T) {
	svc, _ := New("https://panel.example.com", "Chiral")
	u := User{ID: "admin-1", Name: "mai", DisplayName: "Mai"}
	_, err := svc.FinishRegistration(u, []byte("not json"), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("expected a clear error about the stored challenge, got %v", err)
	}
}

// Assertion needs at least one enrolled credential; asking for one with none
// is a programming error worth surfacing rather than an empty ceremony.
func TestBeginLoginNeedsACredential(t *testing.T) {
	svc, _ := New("https://panel.example.com", "Chiral")
	if _, _, err := svc.BeginLogin(User{ID: "admin-1", Name: "mai"}); err == nil {
		t.Error("began an assertion for an account with no passkeys")
	}
}
