package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/subscription"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// serveSubscription is the one endpoint end users hit. It is deliberately
// unauthenticated apart from the token in the path: subscription URLs are
// pasted into clients that cannot log in.
//
// The token is treated as a credential: lookup is by hash alone, and an
// unknown one gets a flat 404 with no hint about whether the user exists.
//
// It used to be true that only the hash was stored. Migration 0009 added a
// sealed copy so the portal can show someone their own link; the lookup path
// here is unchanged and never opens it.
func (s *Server) serveSubscription(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	u, err := s.st.FindUserBySubTokenHash(auth.HashSecret(token))
	if err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		s.internalErr(w, "subscription lookup", err)
		return
	}

	// A cut-off user still gets their subscription: the credentials in it are
	// already removed from the nodes, and returning the list keeps clients
	// from erroring in confusing ways when a quota is topped up again.
	// The headers below tell the client where they stand.
	client := subscription.DetectClient(r)
	res, err := s.subs.Render(u, client)
	if err != nil {
		s.internalErr(w, "rendering subscription", err)
		return
	}

	// An empty subscription is not a successful one.
	//
	// Clash and Stash overwrite the profile they hold with whatever a 200
	// returns, so serving an empty document replaces a customer's working
	// config with nothing — a self-inflicted outage that looks, from their
	// side, exactly like the panel deciding to cut them off. A non-2xx makes
	// every client keep what it already has and show the update as failed,
	// which is the truthful outcome: we could not produce a subscription.
	//
	// This is a different condition from a cut-off user, who still gets their
	// (populated) list — see above.
	if res.Fragments == 0 {
		reason := "no access point is available for this subscription"
		if len(res.Skipped) > 0 {
			reason = "no access point could be rendered: " + strings.Join(res.Skipped, "; ")
		}
		s.logger.Warn("subscription came out empty", "user", u.Name,
			"client", res.Client, "skipped", res.Skipped)
		writeErr(w, http.StatusConflict, reason)
		return
	}
	if len(res.Skipped) > 0 {
		// Served, but short. The customer cannot tell; the operator can.
		s.logger.Warn("subscription served with access points missing",
			"user", u.Name, "client", res.Client,
			"fragments", res.Fragments, "skipped", res.Skipped)
	}

	w.Header().Set("Content-Type", res.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, res.Filename))
	// The conventional header clients read to show quota and expiry.
	w.Header().Set("Subscription-Userinfo", userinfoHeader(u))
	// Suspension has no representation in Subscription-Userinfo: expiry shows
	// as a past `expire`, an exhausted quota as download > total, and a
	// disabled account as nothing at all — the client draws a perfectly
	// healthy subscription for somebody who cannot connect. This header is
	// not a standard, and it is the only place to say so before the customer
	// concludes the service is broken rather than switched off.
	if reason := user.Reason(u, time.Now().Unix()); reason != "" {
		w.Header().Set("Subscription-Status", reason)
	}
	w.Header().Set("Profile-Update-Interval", "12")
	// Subscriptions carry credentials; they must not be cached by anything in
	// between.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, res.Body)

	s.logger.Info("subscription served", "user", u.Name, "client", res.Client,
		"fragments", res.Fragments, "allowed", user.Allowed(u, time.Now().Unix()))
}

// userinfoHeader is the widely-supported `Subscription-Userinfo` format:
// upload/download/total in bytes plus an expiry timestamp. Chiral counts both
// directions against one quota, so the whole usage is reported as download and
// upload stays 0 rather than double-counting.
func userinfoHeader(u store.User) string {
	return fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d",
		u.UsedBytes, u.QuotaBytes, u.ExpiresAt)
}
