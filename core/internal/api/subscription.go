package api

import (
	"fmt"
	"net/http"
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
// The token is treated as a credential — only its hash is stored, and an
// unknown one gets a flat 404 with no hint about whether the user exists.
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

	w.Header().Set("Content-Type", res.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, res.Filename))
	// The conventional header clients read to show quota and expiry.
	w.Header().Set("Subscription-Userinfo", userinfoHeader(u))
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
