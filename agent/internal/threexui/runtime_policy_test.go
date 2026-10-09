package threexui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadRuntimePolicyExposesOnlyExplicitRestartSetting(t *testing.T) {
	const secret = "private-panel-settings-secret"
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			var requests atomic.Int32
			client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/base/panel/api/setting/all" || r.URL.RawQuery != "" {
					t.Error("unexpected runtime policy request")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) != 0 {
					t.Error("runtime policy read must not send a settings write")
				}
				fmt.Fprintf(w, `{"success":true,"obj":{"restartXrayOnClientDisable":%t,"secret":"%s","smtpPassword":{"nested":"%s"}}}`, enabled, secret, secret)
			})
			policy, err := client.ReadRuntimePolicy(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if policy.RestartXrayOnClientDisable != enabled {
				t.Fatalf("restart setting = %v, want %v", policy.RestartXrayOnClientDisable, enabled)
			}
			if reflect.TypeOf(policy).NumField() != 1 || strings.Contains(fmt.Sprintf("%+v", policy), secret) {
				t.Fatal("runtime policy exposed other settings")
			}
			if requests.Load() != 1 {
				t.Fatal("runtime policy read performed additional HTTP operations")
			}
		})
	}
}

func TestReadRuntimePolicyRejectsMissingAmbiguousAndNonBooleanSettings(t *testing.T) {
	const secret = "private-setting-secret"
	tests := []struct {
		name   string
		object string
	}{
		{"missing", `{"secret":"` + secret + `"}`},
		{"null", `{"restartXrayOnClientDisable":null}`},
		{"string", `{"restartXrayOnClientDisable":"` + secret + `"}`},
		{"false string", `{"restartXrayOnClientDisable":"false"}`},
		{"number", `{"restartXrayOnClientDisable":0}`},
		{"array value", `{"restartXrayOnClientDisable":[]}`},
		{"object value", `{"restartXrayOnClientDisable":{"secret":"` + secret + `"}}`},
		{"array object", `[]`},
		{"string object", `"` + secret + `"`},
		{"wrong case", `{"RestartXrayOnClientDisable":false}`},
		{"duplicate", `{"restartXrayOnClientDisable":true,"restartXrayOnClientDisable":false}`},
		{"duplicate same value", `{"restartXrayOnClientDisable":false,"restartXrayOnClientDisable":false}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"success":true,"obj":%s}`, test.object)
			})
			policy, err := client.ReadRuntimePolicy(context.Background())
			var contractErr *ContractError
			if !errors.As(err, &contractErr) || err.Error() != "3x-ui returned an invalid runtime policy" || policy != (RuntimePolicy{}) {
				t.Fatalf("invalid policy result = %+v, error = %v", policy, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), testToken) {
				t.Fatal("runtime policy error leaked a secret")
			}
		})
	}
}

func TestReadRuntimePolicySuppressesEnvelopeAndTransportSecrets(t *testing.T) {
	const secret = "private-settings-response-secret"
	for _, body := range []string{
		`{"success":false,"msg":"` + secret + `"}`,
		`{"success":"` + secret + `"}`,
		`{"success":true,"obj":null,"msg":"` + secret + `"}`,
		`{"success":true,"obj":{"secret":"` + secret + `"}`,
	} {
		client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, body)
		})
		_, err := client.ReadRuntimePolicy(context.Background())
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), testToken) {
			t.Fatal("settings envelope was accepted or leaked a secret")
		}
	}
	client, err := New(Config{
		BaseURL: "http://127.0.0.1", Token: testToken,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("%s %s: %w", secret, r.Header.Get("Authorization"), context.DeadlineExceeded)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ReadRuntimePolicy(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "3x-ui request timed out" {
		t.Fatalf("transport error = %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), testToken) {
		t.Fatal("runtime policy transport error leaked a secret")
	}
}
