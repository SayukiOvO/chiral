// Package mail sends the panel's transactional email: address verification
// and one-time login codes.
package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config comes from the environment, like every other deployment concern here.
//
//	CHIRAL_SMTP_HOST      smtp.example.com        (empty disables email)
//	CHIRAL_SMTP_PORT      587
//	CHIRAL_SMTP_USER      panel@example.com
//	CHIRAL_SMTP_PASSWORD  ...
//	CHIRAL_SMTP_FROM      "Chiral <panel@example.com>"
//	CHIRAL_SMTP_TLS       starttls | implicit | none   (default starttls)
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// TLS selects how the connection is protected. "none" exists for a
	// relay on localhost and is never a good idea across a network.
	TLS string
}

const (
	TLSStartTLS = "starttls"
	TLSImplicit = "implicit"
	TLSNone     = "none"
)

// FromEnv reads the configuration. An empty host means email is switched off,
// which is a valid deployment: the panel simply cannot offer email as a
// factor.
func FromEnv() Config {
	port, _ := strconv.Atoi(os.Getenv("CHIRAL_SMTP_PORT"))
	if port == 0 {
		port = 587
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CHIRAL_SMTP_TLS")))
	switch mode {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		mode = TLSStartTLS
	}
	from := os.Getenv("CHIRAL_SMTP_FROM")
	if from == "" {
		from = os.Getenv("CHIRAL_SMTP_USER")
	}
	return Config{
		Host:     strings.TrimSpace(os.Getenv("CHIRAL_SMTP_HOST")),
		Port:     port,
		Username: os.Getenv("CHIRAL_SMTP_USER"),
		Password: os.Getenv("CHIRAL_SMTP_PASSWORD"),
		From:     from,
		TLS:      mode,
	}
}

// Enabled reports whether email can be sent at all.
func (c Config) Enabled() bool { return c.Host != "" && c.From != "" }

type Sender struct {
	cfg Config
	// dial is swapped in tests; sending real mail from a test suite is not
	// something to leave to chance.
	dial func(cfg Config) (*smtp.Client, error)
}

func NewSender(cfg Config) *Sender {
	return &Sender{cfg: cfg, dial: dialSMTP}
}

func (s *Sender) Enabled() bool { return s.cfg.Enabled() }

// Send delivers one plain-text message.
func (s *Sender) Send(to, subject, body string) error {
	if !s.cfg.Enabled() {
		return fmt.Errorf("email is not configured (set CHIRAL_SMTP_HOST and CHIRAL_SMTP_FROM)")
	}
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("no recipient")
	}
	// A header injected through the subject would let a caller add
	// recipients; the subject is ours, but stripping is cheap insurance.
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)

	client, err := s.dial(s.cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(envelopeAddress(s.cfg.From)); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n"+
		"Date: %s\r\n\r\n%s\r\n",
		s.cfg.From, to, subject, time.Now().Format(time.RFC1123Z), body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func dialSMTP(cfg Config) (*smtp.Client, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	if cfg.TLS == TLSImplicit {
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return nil, err
		}
		return smtp.NewClient(conn, cfg.Host)
	}

	client, err := smtp.Dial(addr)
	if err != nil {
		return nil, err
	}
	if cfg.TLS == TLSStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			client.Close()
			// Failing is the point: silently continuing in the clear would
			// send a login code across the network unprotected.
			return nil, fmt.Errorf("server does not offer STARTTLS (set CHIRAL_SMTP_TLS=none to allow plaintext)")
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			client.Close()
			return nil, err
		}
	}
	return client, nil
}

// envelopeAddress strips a display name: "Chiral <a@b>" -> "a@b".
func envelopeAddress(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.LastIndex(from, ">"); j > i {
			return from[i+1 : j]
		}
	}
	return strings.TrimSpace(from)
}
