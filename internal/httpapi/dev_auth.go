package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"clawgrid/internal/domain"
)

const localDevBrowserIDLimit = 200

func (s *Server) handleLocalDevSession(w http.ResponseWriter, r *http.Request) {
	if !s.localDevAuthEnabled() {
		http.NotFound(w, r)
		return
	}
	if s.cfg.FrontendOrigin != "" && strings.TrimSpace(r.Header.Get("Origin")) != s.cfg.FrontendOrigin {
		respondErr(w, http.StatusForbidden, "dev_auth_frontend_only")
		return
	}

	var body struct {
		BrowserID string `json:"browser_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	browserID := strings.TrimSpace(body.BrowserID)
	if browserID == "" {
		respondErr(w, http.StatusBadRequest, "browser_id_required")
		return
	}
	if len(browserID) > localDevBrowserIDLimit {
		respondErr(w, http.StatusBadRequest, "browser_id_too_long")
		return
	}

	accountID := "acct_dev_" + hash(s.cfg.AuthTokenSecret + ":dev:" + browserID)[:20]
	displayName := "localdev-" + hash("name:" + browserID)[:8]
	if _, err := s.db.Exec(r.Context(), `
INSERT INTO accounts(id, name)
VALUES ($1, $2)
ON CONFLICT (id) DO NOTHING`,
		accountID, displayName,
	); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.bootstrapGitHubAccount(r.Context(), accountID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessionToken, err := s.createAccountSession(r.Context(), accountID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"account_id":      accountID,
		"session_token":   sessionToken,
		"display_name":    displayName,
		"credential_type": domain.AuthCredentialAccountSession,
	})
}

func (s *Server) localDevAuthEnabled() bool {
	if !s.cfg.DevAuthBypass {
		return false
	}
	if s.cfg.FrontendOrigin == "" {
		return false
	}
	parsed, err := url.Parse(s.cfg.FrontendOrigin)
	if err != nil {
		return false
	}
	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
