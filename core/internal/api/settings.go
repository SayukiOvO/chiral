package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Panel settings: choices about what the panel produces, as opposed to the
// deployment facts that live in environment variables. Readable by any admin,
// writable at write level.
func (s *Server) routeSettings(mux *http.ServeMux) {
	mux.Handle("GET /api/settings", s.requireAdmin(s.getSettings))
	mux.Handle("PUT /api/settings", s.requireWrite(s.putSettings))
}

type settingsView struct {
	// SubscriptionName is what a subscription is called when it reaches a
	// client. Returned resolved rather than raw, so the console shows what
	// subscribers actually see rather than an empty box meaning "the default".
	SubscriptionName string `json:"subscription_name"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsView{
		SubscriptionName: s.st.Setting(store.SettingSubscriptionName, "chiral"),
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubscriptionName *string `json:"subscription_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if req.SubscriptionName != nil {
		name := strings.TrimSpace(*req.SubscriptionName)
		// Bounded because it becomes a filename on somebody else's disk.
		if len([]rune(name)) > 64 {
			writeErr(w, http.StatusBadRequest, "订阅名称最长 64 个字符")
			return
		}
		if err := s.st.SetSetting(store.SettingSubscriptionName, name); err != nil {
			s.internalErr(w, "save setting", err)
			return
		}
		s.audit(r, "settings.update", "settings", store.SettingSubscriptionName, name, "")
	}
	s.getSettings(w, r)
}
