// Package passkey wraps WebAuthn registration and assertion for admin
// accounts.
//
// The protocol itself is delegated to github.com/go-webauthn/webauthn rather
// than implemented here. WebAuthn requires CBOR and COSE parsing, attestation
// handling, signature verification, and binding every ceremony to an origin
// and a relying-party id. A hand-written version would appear to work — a key
// registers, a login succeeds — while failing to actually verify anything,
// and nothing in normal use would reveal it. That is the same class of hazard
// as a mismatched keypair that `xray -test` accepts.
package passkey

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Service holds the relying-party configuration. A passkey is bound to the
// RP id, so this must match the domain the panel is served from — a mismatch
// is exactly what stops a phishing site from replaying a credential.
type Service struct {
	wa *webauthn.WebAuthn
	// rpID is kept for error messages, which is where a misconfiguration
	// actually gets noticed.
	rpID string
}

// New builds the service from the panel's public URL. Returns nil when no
// usable URL is configured: passkeys simply are not offered then, rather than
// being offered and failing at the last step.
func New(publicURL, displayName string) (*Service, error) {
	rpID, origin, err := parseRP(publicURL)
	if err != nil {
		return nil, err
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: displayName,
		RPID:          rpID,
		RPOrigins:     []string{origin},
	})
	if err != nil {
		return nil, err
	}
	return &Service{wa: wa, rpID: rpID}, nil
}

// parseRP derives the relying-party id (a bare domain) and the origin (scheme
// + host) from the panel's URL.
func parseRP(publicURL string) (rpID, origin string, err error) {
	raw := strings.TrimSpace(publicURL)
	if raw == "" {
		return "", "", fmt.Errorf("CHIRAL_PUBLIC_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("CHIRAL_PUBLIC_URL is not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("CHIRAL_PUBLIC_URL must be http or https")
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("CHIRAL_PUBLIC_URL has no host")
	}
	// Browsers only allow WebAuthn on a secure context: https, or localhost.
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return "", "", fmt.Errorf("passkeys need https (or localhost); %s is neither", u.Host)
	}
	return u.Hostname(), u.Scheme + "://" + u.Host, nil
}

// RPID reports the relying-party id in use, for display and diagnostics.
func (s *Service) RPID() string { return s.rpID }

// User is what the library needs to know about an account. Credentials are
// the passkeys already enrolled, which registration uses to refuse enrolling
// the same authenticator twice, and assertion uses to know what to accept.
type User struct {
	ID          string
	Name        string
	DisplayName string
	Creds       []webauthn.Credential
}

func (u User) WebAuthnID() []byte                         { return []byte(u.ID) }
func (u User) WebAuthnName() string                       { return u.Name }
func (u User) WebAuthnDisplayName() string                { return u.DisplayName }
func (u User) WebAuthnCredentials() []webauthn.Credential { return u.Creds }

// BeginRegistration returns the creation options the browser passes to
// navigator.credentials.create, plus the session data that must be kept for
// the finish step. The session carries the challenge; without storing it,
// there is nothing to check the response against.
func (s *Service) BeginRegistration(u User) (options []byte, session []byte, err error) {
	// Require a resident, user-verified credential: this is a second factor,
	// so a key that authenticates without any user gesture would not be one.
	creation, sessionData, err := s.wa.BeginRegistration(u,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationPreferred,
		}),
	)
	if err != nil {
		return nil, nil, err
	}
	options, err = json.Marshal(creation)
	if err != nil {
		return nil, nil, err
	}
	session, err = json.Marshal(sessionData)
	return options, session, err
}

// FinishRegistration verifies the authenticator's response and returns the
// credential to store.
func (s *Service) FinishRegistration(u User, session []byte, response json.RawMessage) (*webauthn.Credential, error) {
	var sessionData webauthn.SessionData
	if err := json.Unmarshal(session, &sessionData); err != nil {
		return nil, fmt.Errorf("stored challenge is unreadable: %w", err)
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(strings.NewReader(string(response)))
	if err != nil {
		return nil, protocolError(err)
	}
	return s.wa.CreateCredential(u, sessionData, parsed)
}

// BeginLogin returns the assertion options for navigator.credentials.get.
func (s *Service) BeginLogin(u User) (options []byte, session []byte, err error) {
	assertion, sessionData, err := s.wa.BeginLogin(u)
	if err != nil {
		return nil, nil, err
	}
	options, err = json.Marshal(assertion)
	if err != nil {
		return nil, nil, err
	}
	session, err = json.Marshal(sessionData)
	return options, session, err
}

// FinishLogin verifies an assertion. The returned credential carries the
// authenticator's signature counter, which the caller should persist: a
// counter that goes backwards is the signal that a credential has been cloned.
func (s *Service) FinishLogin(u User, session []byte, response json.RawMessage) (*webauthn.Credential, error) {
	var sessionData webauthn.SessionData
	if err := json.Unmarshal(session, &sessionData); err != nil {
		return nil, fmt.Errorf("stored challenge is unreadable: %w", err)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(strings.NewReader(string(response)))
	if err != nil {
		return nil, protocolError(err)
	}
	return s.wa.ValidateLogin(u, sessionData, parsed)
}

// EncodeCredential serialises a credential for storage.
func EncodeCredential(c *webauthn.Credential) (string, error) {
	b, err := json.Marshal(c)
	return string(b), err
}

// DecodeCredential restores one.
func DecodeCredential(s string) (webauthn.Credential, error) {
	var c webauthn.Credential
	err := json.Unmarshal([]byte(s), &c)
	return c, err
}

// CredentialID is the base64url form used as the lookup key, matching what the
// browser sends back in an assertion.
func CredentialID(c *webauthn.Credential) string {
	return base64.RawURLEncoding.EncodeToString(c.ID)
}

// protocolError unwraps the library's error into something an operator can
// act on. Its details name the actual mismatch — wrong origin, wrong RP id —
// which is almost always a deployment problem rather than a user error.
func protocolError(err error) error {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr.Details != "" {
		return fmt.Errorf("%s: %s", perr.Details, perr.DevInfo)
	}
	return err
}
