package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

const (
	liveTailSettingsReadEndpoint   = "panel/api/setting/all"
	liveTailSettingsUpdateEndpoint = "panel/api/setting/update"
	liveTailSettingsRestoreTimeout = 45 * time.Second
)

var liveTailSecretPresenceFields = []string{
	"hasTgBotToken",
	"hasTwoFactorToken",
	"hasLdapPassword",
	"hasWarpSecret",
	"hasNordSecret",
	"hasSmtpPassword",
	"hasDiscordBotToken",
}

var liveTailRedactedSecretFields = []string{
	"tgBotToken",
	"twoFactorToken",
	"ldapPassword",
	"smtpPassword",
	"discordBotToken",
}

type liveTailSettingsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f liveTailSettingsRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// liveTailDisableRestart changes the official panel-wide setting through its
// only HTTP write path. The upstream route rewrites the complete AllSetting
// object, so this helper round-trips every returned member and accepts the
// change only after a complete semantic readback. It is test-only and requires
// the caller's already-verified fresh, isolated instance.
//
// The returned cleanup uses an independent context. It refuses to overwrite a
// setting snapshot that no longer exactly matches this helper's candidate: the
// endpoint has no revision or conditional-write primitive, so a concurrent
// change cannot be restored safely.
func liveTailDisableRestart(t *testing.T, ctx context.Context, client *Client) func() {
	t.Helper()
	original, err := liveTailReadAllSettings(ctx, client)
	if err != nil {
		t.Fatal("cannot read the isolated panel settings")
	}
	candidate, err := liveTailDisableRestartCandidate(original)
	if err != nil {
		t.Fatal("isolated panel settings are not safe to round-trip")
	}
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			t.Helper()
			restoreCtx, cancel := context.WithTimeout(context.Background(), liveTailSettingsRestoreTimeout)
			defer cancel()

			current, readErr := liveTailReadAllSettings(restoreCtx, client)
			if readErr != nil {
				t.Error("cannot verify the isolated panel settings before restoration")
				return
			}
			unchanged, compareErr := liveTailSettingsEqual(candidate, current)
			if compareErr != nil {
				t.Error("cannot compare the isolated panel settings before restoration")
				return
			}
			if !unchanged {
				// A failed request can leave either the candidate or the original
				// state. The latter needs no write; every other state may include a
				// concurrent change and is left untouched.
				alreadyOriginal, originalErr := liveTailSettingsEqual(original, current)
				if originalErr == nil && alreadyOriginal {
					t.Log("tail_settings_restored_verified=true")
					return
				}
				t.Error("isolated panel settings changed after the candidate; refusing unsafe restoration")
				return
			}
			if writeErr := liveTailWriteAllSettings(restoreCtx, client, original); writeErr != nil {
				// As above, an unknown write result is not retried.
				t.Error("settings restoration result is unknown or failed; no retry is permitted")
				return
			}
			restored, readErr := liveTailReadAllSettings(restoreCtx, client)
			if readErr != nil {
				t.Error("cannot read back the restored isolated panel settings")
				return
			}
			equal, compareErr := liveTailSettingsEqual(original, restored)
			if compareErr != nil || !equal {
				t.Error("restored panel settings did not equal the complete original snapshot")
				return
			}
			t.Log("tail_settings_restored_verified=true")
		})
	}
	// The caller also defers the returned function to control ordering relative
	// to fixture shutdown. This fallback covers a fatal result after the write
	// but before the caller receives that function.
	t.Cleanup(restore)
	if err := liveTailWriteAllSettings(ctx, client, candidate); err != nil {
		// A missing or malformed success envelope is an unknown result. Never
		// retry this full-settings write blindly.
		t.Fatal("disable-restart settings result is unknown or failed; no retry is permitted")
	}
	confirmed, err := liveTailReadAllSettings(ctx, client)
	if err != nil {
		t.Fatal("cannot read back the disable-restart settings candidate")
	}
	equal, err := liveTailSettingsEqual(candidate, confirmed)
	if err != nil || !equal {
		t.Fatal("disable-restart settings readback did not equal the complete candidate")
	}
	t.Log("tail_restart_disabled_verified=true")

	return restore
}

func liveTailDisableRestartCandidate(original map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if err := liveTailValidateSettingsSnapshot(original); err != nil {
		return nil, err
	}
	candidate := liveTailCloneSettings(original)
	candidate["restartXrayOnClientDisable"] = json.RawMessage("false")
	return candidate, nil
}

func liveTailValidateSettingsSnapshot(settings map[string]json.RawMessage) error {
	if len(settings) == 0 {
		return fmt.Errorf("empty settings snapshot")
	}
	if raw, ok := settings["restartXrayOnClientDisable"]; !ok || !liveTailStrictJSONBool(raw) {
		return fmt.Errorf("missing restart setting")
	}
	for _, key := range liveTailSecretPresenceFields {
		raw, ok := settings[key]
		if !ok || !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
			return fmt.Errorf("invalid secret-presence setting")
		}
	}
	for _, key := range liveTailRedactedSecretFields {
		raw, ok := settings[key]
		if !ok || !bytes.Equal(bytes.TrimSpace(raw), []byte(`""`)) {
			return fmt.Errorf("fresh instance returned a non-redacted secret field")
		}
	}
	return nil
}

func liveTailStrictJSONBool(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false"))
}

func liveTailReadAllSettings(ctx context.Context, client *Client) (map[string]json.RawMessage, error) {
	u, err := client.endpointURL(liveTailSettingsReadEndpoint)
	if err != nil {
		return nil, err
	}
	var settings map[string]json.RawMessage
	if err := client.sensitiveEnvelope(ctx, http.MethodPost, u, nil, "", &settings); err != nil {
		return nil, err
	}
	if len(settings) == 0 {
		return nil, &ContractError{Message: "3x-ui returned an empty settings object"}
	}
	return settings, nil
}

func liveTailWriteAllSettings(ctx context.Context, client *Client, settings map[string]json.RawMessage) error {
	body, err := marshalSensitive(settings)
	if err != nil {
		return err
	}
	u, err := client.endpointURL(liveTailSettingsUpdateEndpoint)
	if err != nil {
		return err
	}
	return client.sensitiveEnvelope(ctx, http.MethodPost, u, body, "application/json", nil)
}

func liveTailCloneSettings(settings map[string]json.RawMessage) map[string]json.RawMessage {
	clone := make(map[string]json.RawMessage, len(settings))
	for key, raw := range settings {
		clone[key] = append(json.RawMessage(nil), raw...)
	}
	return clone
}

func liveTailSettingsEqual(left, right map[string]json.RawMessage) (bool, error) {
	if len(left) != len(right) {
		return false, nil
	}
	for key, leftRaw := range left {
		rightRaw, ok := right[key]
		if !ok {
			return false, nil
		}
		leftCanonical, err := liveTailCanonicalJSON(leftRaw)
		if err != nil {
			return false, err
		}
		rightCanonical, err := liveTailCanonicalJSON(rightRaw)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(leftCanonical, rightCanonical) {
			return false, nil
		}
	}
	return true, nil
}

func liveTailCanonicalJSON(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid settings JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("invalid settings JSON")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid settings JSON")
	}
	return canonical, nil
}

func TestLiveTailDisableRestartRoundTripsEverySetting(t *testing.T) {
	original := map[string]json.RawMessage{
		"restartXrayOnClientDisable": json.RawMessage("true"),
		"hasTgBotToken":              json.RawMessage("false"),
		"hasTwoFactorToken":          json.RawMessage("false"),
		"hasLdapPassword":            json.RawMessage("false"),
		"hasWarpSecret":              json.RawMessage("false"),
		"hasNordSecret":              json.RawMessage("false"),
		"hasSmtpPassword":            json.RawMessage("false"),
		"hasDiscordBotToken":         json.RawMessage("false"),
		"hasApiToken":                json.RawMessage("true"),
		"tgBotToken":                 json.RawMessage(`""`),
		"twoFactorToken":             json.RawMessage(`""`),
		"ldapPassword":               json.RawMessage(`""`),
		"smtpPassword":               json.RawMessage(`""`),
		"discordBotToken":            json.RawMessage(`""`),
		"futureScalar":               json.RawMessage(`9007199254740993`),
		"futureObject":               json.RawMessage(`{"nested":[true,{"kept":"yes"}]}`),
	}
	current := liveTailCloneSettings(original)
	var mu sync.Mutex
	var paths []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/panel/api/setting/all":
			if r.Method != http.MethodPost {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			payload, _ := json.Marshal(current)
			_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":` + string(payload) + `}`))
		case "/panel/api/setting/update":
			if r.Method != http.MethodPost {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			var next map[string]json.RawMessage
			decoder := json.NewDecoder(io.LimitReader(r.Body, maxMutationRequestBytes+1))
			if decoder.Decode(&next) != nil {
				http.Error(w, "body", http.StatusBadRequest)
				return
			}
			current = liveTailCloneSettings(next)
			_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":null}`))
		default:
			http.Error(w, "route", http.StatusNotFound)
		}
	})
	transport := liveTailSettingsRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})

	client, err := New(Config{
		BaseURL:    "http://127.0.0.1",
		Token:      "test-token",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal("construct client:", err)
	}
	restore := liveTailDisableRestart(t, context.Background(), client)
	mu.Lock()
	disabled := liveTailCloneSettings(current)
	mu.Unlock()
	wantDisabled, err := liveTailDisableRestartCandidate(original)
	if err != nil {
		t.Fatal("build expected candidate:", err)
	}
	equal, err := liveTailSettingsEqual(wantDisabled, disabled)
	if err != nil || !equal {
		t.Fatal("candidate lost or changed a settings member")
	}
	restore()

	mu.Lock()
	restored := liveTailCloneSettings(current)
	gotPaths := append([]string(nil), paths...)
	mu.Unlock()
	equal, err = liveTailSettingsEqual(original, restored)
	if err != nil || !equal {
		t.Fatal("restoration lost or changed a settings member")
	}
	wantPaths := []string{
		"/panel/api/setting/all",
		"/panel/api/setting/update",
		"/panel/api/setting/all",
		"/panel/api/setting/all",
		"/panel/api/setting/update",
		"/panel/api/setting/all",
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatal("settings helper used an unexpected HTTP request sequence")
	}
}

func TestLiveTailDisableRestartCandidateRejectsConfiguredSecretsAndPreservesInput(t *testing.T) {
	base := map[string]json.RawMessage{
		"restartXrayOnClientDisable": json.RawMessage("true"),
		"hasTgBotToken":              json.RawMessage("false"),
		"hasTwoFactorToken":          json.RawMessage("false"),
		"hasLdapPassword":            json.RawMessage("false"),
		"hasWarpSecret":              json.RawMessage("false"),
		"hasNordSecret":              json.RawMessage("false"),
		"hasSmtpPassword":            json.RawMessage("false"),
		"hasDiscordBotToken":         json.RawMessage("false"),
		"hasApiToken":                json.RawMessage("true"),
		"tgBotToken":                 json.RawMessage(`""`),
		"twoFactorToken":             json.RawMessage(`""`),
		"ldapPassword":               json.RawMessage(`""`),
		"smtpPassword":               json.RawMessage(`""`),
		"discordBotToken":            json.RawMessage(`""`),
		"sentinel":                   json.RawMessage(`{"b":2,"a":1}`),
	}
	original := liveTailCloneSettings(base)
	candidate, err := liveTailDisableRestartCandidate(base)
	if err != nil {
		t.Fatal("candidate:", err)
	}
	if string(base["restartXrayOnClientDisable"]) != "true" || string(candidate["restartXrayOnClientDisable"]) != "false" {
		t.Fatal("candidate mutated its input or did not disable restart")
	}
	equal, err := liveTailSettingsEqual(base, original)
	if err != nil || !equal {
		t.Fatal("candidate mutated another input member")
	}

	configured := liveTailCloneSettings(base)
	configured["hasSmtpPassword"] = json.RawMessage("true")
	if _, err := liveTailDisableRestartCandidate(configured); err == nil {
		t.Fatal("configured secret was accepted")
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage("null"),
		json.RawMessage(`"false"`),
		json.RawMessage("0"),
		json.RawMessage("{}"),
	} {
		invalidRestart := liveTailCloneSettings(base)
		invalidRestart["restartXrayOnClientDisable"] = invalid
		if _, err := liveTailDisableRestartCandidate(invalidRestart); err == nil {
			t.Fatal("non-boolean restart setting was accepted")
		}
		invalidPresence := liveTailCloneSettings(base)
		invalidPresence["hasSmtpPassword"] = invalid
		if _, err := liveTailDisableRestartCandidate(invalidPresence); err == nil {
			t.Fatal("non-boolean secret-presence setting was accepted")
		}
	}
	secretValue := liveTailCloneSettings(base)
	secretValue["smtpPassword"] = json.RawMessage(`"not-redacted"`)
	if _, err := liveTailDisableRestartCandidate(secretValue); err == nil {
		t.Fatal("non-redacted secret value was accepted")
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage("null"),
		json.RawMessage("false"),
		json.RawMessage("0"),
		json.RawMessage("{}"),
	} {
		invalidRedaction := liveTailCloneSettings(base)
		invalidRedaction["smtpPassword"] = invalid
		if _, err := liveTailDisableRestartCandidate(invalidRedaction); err == nil {
			t.Fatal("non-string redacted secret was accepted")
		}
	}
}
