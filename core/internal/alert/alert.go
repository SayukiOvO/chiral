// Package alert notices when a node changes availability and tells someone.
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Debounce is how long a node must hold a new state before it is worth
// telling anyone. A flapping link would otherwise generate a message per
// heartbeat, which trains people to ignore the channel — the failure mode
// that makes alerting useless.
const Debounce = 2 * time.Minute

// deliveryTimeout bounds one notification attempt. A hanging webhook must not
// stall the sweep that found the change.
const deliveryTimeout = 10 * time.Second

// Liveness reports which nodes are currently up. Implemented by node.Manager.
type Liveness interface {
	IsOnline(nodeID string) bool
}

type Service struct {
	st     *store.Store
	live   Liveness
	client *http.Client
	logger *slog.Logger
	// now is injectable so the debounce can be tested without sleeping
	// through it.
	now func() time.Time
}

func NewService(st *store.Store, live Liveness, logger *slog.Logger) *Service {
	return &Service{
		st:     st,
		live:   live,
		client: &http.Client{Timeout: deliveryTimeout},
		logger: logger,
		now:    time.Now,
	}
}

// Sweep records what every node currently looks like and announces anything
// that has settled into a new state. Returns how many notifications were sent.
//
// Announcing from a swept state rather than from the moment of disconnection
// is deliberate: it survives a panel restart, and it is where the debounce
// naturally lives.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	nodes, err := s.st.ListNodes()
	if err != nil {
		return 0, err
	}
	now := s.now()
	for _, n := range nodes {
		if err := s.st.ObserveNode(n.ID, s.live.IsOnline(n.ID), now); err != nil {
			s.logger.Error("recording node liveness failed", "node", n.ID, "err", err)
		}
	}

	states, err := s.st.NodeAlertStates()
	if err != nil {
		return 0, err
	}
	targets, err := s.enabledTargets()
	if err != nil {
		return 0, err
	}

	sent := 0
	for _, n := range nodes {
		st, ok := states[n.ID]
		if !ok || st.ObservedOnline == st.AnnouncedOnline {
			continue
		}
		if now.Sub(time.Unix(st.ChangedAt, 0)) < Debounce {
			continue // still settling
		}
		// Mark first: a delivery failure must not queue the same message
		// forever, and the next real change will be announced regardless.
		if err := s.st.MarkAnnounced(n.ID, st.ObservedOnline, now); err != nil {
			s.logger.Error("recording announcement failed", "node", n.ID, "err", err)
			continue
		}
		if len(targets) == 0 {
			continue
		}
		s.notify(ctx, targets, message(n.Name, st.ObservedOnline, st.ChangedAt))
		sent++
	}
	return sent, nil
}

func (s *Service) enabledTargets() ([]store.AlertTarget, error) {
	all, err := s.st.ListAlertTargets()
	if err != nil {
		return nil, err
	}
	out := make([]store.AlertTarget, 0, len(all))
	for _, t := range all {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out, nil
}

// Event is one thing worth telling someone about.
type Event struct {
	Title string
	Body  string
	// Online is the state being announced, so a webhook consumer can act on
	// it without parsing prose.
	Node   string
	Online bool
	At     int64
}

func message(nodeName string, online bool, changedAt int64) Event {
	if online {
		return Event{
			Title: "Node back online",
			Body:  fmt.Sprintf("%s is reachable again.", nodeName),
			Node:  nodeName, Online: true, At: changedAt,
		}
	}
	return Event{
		Title: "Node offline",
		Body:  fmt.Sprintf("%s stopped sending heartbeats.", nodeName),
		Node:  nodeName, Online: false, At: changedAt,
	}
}

// notify delivers to every target, recording each outcome. One target's
// failure must not stop the others.
func (s *Service) notify(ctx context.Context, targets []store.AlertTarget, e Event) {
	for _, t := range targets {
		err := s.deliver(ctx, t, e)
		msg := ""
		if err != nil {
			msg = err.Error()
			s.logger.Warn("alert delivery failed", "target", t.Name, "err", err)
		}
		if err := s.st.RecordAlertResult(t.ID, msg); err != nil {
			s.logger.Error("recording alert result failed", "target", t.Name, "err", err)
		}
	}
}

// Deliver sends one event to one target. Exported so the API can offer a
// "send a test message" button, which is the only way to find out a chat id is
// wrong before an incident.
func (s *Service) Deliver(ctx context.Context, t store.AlertTarget, e Event) error {
	return s.deliver(ctx, t, e)
}

func (s *Service) deliver(ctx context.Context, t store.AlertTarget, e Event) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	switch t.Kind {
	case store.AlertTelegram:
		return s.deliverTelegram(ctx, t.Config, e)
	case store.AlertWebhook:
		return s.deliverWebhook(ctx, t.Config, e)
	default:
		return fmt.Errorf("unknown target kind %q", t.Kind)
	}
}

// deliverTelegram posts to the Bot API. Config is "<bot-token>:<chat-id>";
// bot tokens themselves contain a colon, so the chat id is split from the
// right.
func (s *Service) deliverTelegram(ctx context.Context, config string, e Event) error {
	idx := strings.LastIndex(config, ":")
	if idx <= 0 || idx == len(config)-1 {
		return fmt.Errorf("telegram config must be \"<bot-token>:<chat-id>\"")
	}
	token, chatID := config[:idx], config[idx+1:]

	body := url.Values{
		"chat_id": {chatID},
		"text":    {fmt.Sprintf("%s\n%s", e.Title, e.Body)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+token+"/sendMessage",
		strings.NewReader(body.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(req)
}

func (s *Service) deliverWebhook(ctx context.Context, endpoint string, e Event) error {
	payload, err := json.Marshal(map[string]any{
		"title":  e.Title,
		"body":   e.Body,
		"node":   e.Node,
		"online": e.Online,
		"at":     e.At,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return s.do(req)
}

func (s *Service) do(req *http.Request) error {
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Read a little of the body: an API that reports failure in a 200 is
	// common enough that the status alone is not the whole story, and the
	// snippet is what an operator will see in the UI.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}
