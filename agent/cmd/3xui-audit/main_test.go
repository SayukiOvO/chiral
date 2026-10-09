package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAuditReturnsExplicitNonApproval(t *testing.T) {
	for _, input := range []string{`{}`, `{"inbounds":[{"protocol":"vless","settings":{"clients":[{"level":3,"id":"private-secret"}]}}]}`} {
		var out, diagnostic bytes.Buffer
		code := run(nil, strings.NewReader(input), &out, &diagnostic)
		if code != 2 || diagnostic.Len() != 0 || !strings.Contains(out.String(), `"takeover_permitted":false`) || !strings.Contains(out.String(), `"runtime_validation_required":true`) || strings.Contains(out.String(), "private-secret") {
			t.Fatalf("unexpected audit: code=%d output=%s diagnostic=%s", code, &out, &diagnostic)
		}
	}
}

func TestAuditRejectsInvalidInputsWithoutEcho(t *testing.T) {
	for _, test := range []struct {
		args  []string
		input string
	}{
		{[]string{"private-secret"}, `{}`},
		{nil, `{"private-secret":`},
		{nil, strings.Repeat(" ", maxInputBytes+1)},
		{nil, `{"inbounds":"private-secret"}`},
	} {
		var out, diagnostic bytes.Buffer
		if code := run(test.args, strings.NewReader(test.input), &out, &diagnostic); code != 1 || out.Len() != 0 || diagnostic.Len() == 0 || strings.Contains(diagnostic.String(), "private-secret") {
			t.Fatalf("invalid rejection: code=%d output=%s diagnostic=%s", code, &out, &diagnostic)
		}
	}
}

type failedIO struct{}

func (failedIO) Read([]byte) (int, error)  { return 0, errors.New("private-secret") }
func (failedIO) Write([]byte) (int, error) { return 0, errors.New("private-secret") }

func TestAuditIOFailuresHaveFixedDiagnostics(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if code := run(nil, failedIO{}, &out, &diagnostic); code != 1 || out.Len() != 0 || strings.Contains(diagnostic.String(), "private-secret") {
		t.Fatal("read failure was not safely reported")
	}
	diagnostic.Reset()
	if code := run(nil, strings.NewReader(`{}`), failedIO{}, &diagnostic); code != 1 || diagnostic.Len() == 0 || strings.Contains(diagnostic.String(), "private-secret") {
		t.Fatal("write failure was not safely reported")
	}
}
