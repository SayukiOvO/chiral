package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The snippet an operator pastes onto a node machine, and the checked-in
// template it is supposed to mirror.
//
// Two copies exist because one is rendered with a live join token and the other
// is documentation, and neither can import the other. A test is the only thing
// keeping them from drifting — and the drift that matters here is silent: a
// state directory that does not match its volume means the node's identity, its
// config and every kernel it has been upgraded to live somewhere a container
// restart throws away.
func snippet(t *testing.T) string {
	t.Helper()
	s := &Server{grpcPublicAddr: "panel.example.com:8443", grpcTLS: true}
	return s.composeSnippet("join-token-under-test")
}

func TestComposeSnippetPersistsTheStateDirectory(t *testing.T) {
	out := snippet(t)
	const dir = "/var/lib/chiral-agent"
	if !strings.Contains(out, "CHIRAL_STATE_DIR: "+dir) {
		t.Fatalf("the snippet does not set the state directory:\n%s", out)
	}
	if !strings.Contains(out, "chiral-agent-data:"+dir) {
		t.Fatalf("the state directory is not on a volume, so a restart loses the "+
			"node's identity and every installed kernel:\n%s", out)
	}
}

func TestComposeSnippetCarriesThePanelAndToken(t *testing.T) {
	out := snippet(t)
	if !strings.Contains(out, `PANEL_URL: "panel.example.com:8443"`) {
		t.Errorf("no panel address:\n%s", out)
	}
	if !strings.Contains(out, `JOIN_TOKEN: "join-token-under-test"`) {
		t.Errorf("no join token:\n%s", out)
	}
}

// A plaintext panel must say so in the snippet, or the agent refuses TLS and
// the operator is left guessing.
func TestComposeSnippetFlagsAPlaintextPanel(t *testing.T) {
	plain := &Server{grpcPublicAddr: "10.0.0.1:8443", grpcTLS: false}
	if !strings.Contains(plain.composeSnippet("t"), "CHIRAL_INSECURE") {
		t.Error("a plaintext panel's snippet does not set CHIRAL_INSECURE")
	}
	if strings.Contains(snippet(t), "CHIRAL_INSECURE") {
		t.Error("a TLS panel's snippet sets CHIRAL_INSECURE anyway")
	}
}

// The checked-in template is what someone reads when they are not creating a
// node, so it has to agree with what the panel actually hands out.
func TestTheCheckedInTemplateAgreesWithTheSnippet(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "agent", "docker-compose.yml.tmpl"))
	if err != nil {
		t.Skipf("template not readable from here: %v", err)
	}
	tmpl := string(raw)
	out := snippet(t)

	for _, want := range []string{
		"CHIRAL_STATE_DIR: /var/lib/chiral-agent",
		"chiral-agent-data:/var/lib/chiral-agent",
		"network_mode: host",
		"image: ghcr.io/sayukiovo/chiral-agent:latest",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("the template is missing %q", want)
		}
		if !strings.Contains(out, want) {
			t.Errorf("the generated snippet is missing %q", want)
		}
	}
}

// Publishing under a different namespace than the built-in default is ordinary
// — a fork, a private registry, a pinned tag. The snippet is pasted straight
// into a shell on another machine, so a stale image reference fails minutes
// later and one host away from the mistake.
func TestComposeSnippetHonoursTheConfiguredImage(t *testing.T) {
	s := &Server{grpcPublicAddr: "panel.example.com:8443", grpcTLS: true}
	if !strings.Contains(s.composeSnippet("t"), DefaultAgentImage) {
		t.Fatalf("an unconfigured panel did not fall back to %s", DefaultAgentImage)
	}

	s.SetAgentImage("registry.example.com/team/chiral-agent:v1.2.3")
	out := s.composeSnippet("t")
	if !strings.Contains(out, "image: registry.example.com/team/chiral-agent:v1.2.3") {
		t.Fatalf("the configured image is not in the snippet:\n%s", out)
	}
	if strings.Contains(out, DefaultAgentImage) {
		t.Fatalf("the default image survived alongside the override:\n%s", out)
	}
}
