package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

const (
	gitHubAuthorizeURL   = "https://github.com/login/oauth/authorize"
	gitHubAccessTokenURL = "https://github.com/login/oauth/access_token"
	gitHubUserAPIURL     = "https://api.github.com/user"
)

type gitHubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type gitHubAccessTokenResponse struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
}

func (s *Server) githubOAuthConfigured() bool {
	return strings.TrimSpace(s.cfg.GitHubClientID) != "" &&
		strings.TrimSpace(s.cfg.GitHubClientSecret) != "" &&
		strings.TrimSpace(s.cfg.PublicAPIBase) != ""
}

func (s *Server) githubOAuthCallbackURL() string {
	return strings.TrimRight(s.cfg.PublicAPIBase, "/") + "/accounts/oauth/github/callback"
}

func (s *Server) handleGitHubOAuthStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.githubOAuthConfigured() {
		respondErr(w, http.StatusServiceUnavailable, "github_oauth_not_configured")
		return
	}
	if s.cfg.FrontendOrigin != "" && strings.TrimSpace(r.Header.Get("Origin")) != s.cfg.FrontendOrigin {
		respondErr(w, http.StatusForbidden, "oauth_frontend_only")
		return
	}

	var body struct {
		TurnstileToken string `json:"turnstile_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if err := s.enforceRateLimit(ctx, "github_oauth_rate_limited", githubOAuthStartRateLimitSpecs(clientIP(r.RemoteAddr))...); err != nil {
		if err.Error() == "github_oauth_rate_limited" {
			respondRateLimit(w, err.Error())
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.verifyTurnstile(ctx, body.TurnstileToken, r.RemoteAddr); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "turnstile_unavailable" {
			status = http.StatusServiceUnavailable
		}
		respondErr(w, status, err.Error())
		return
	}

	state := domain.NewID("ghst")
	if err := s.persistGitHubOAuthState(ctx, state); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	authorizeURL, err := s.buildGitHubAuthorizeURL(state)
	if err != nil {
		respondErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"authorize_url": authorizeURL})
}

func (s *Server) handleGitHubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !s.githubOAuthConfigured() {
		http.Error(w, "github oauth is not configured", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query()
	if query.Get("error") != "" {
		s.redirectGitHubOAuthResult(w, "github_oauth_denied", "")
		return
	}

	state := strings.TrimSpace(query.Get("state"))
	code := strings.TrimSpace(query.Get("code"))
	if state == "" || code == "" {
		s.redirectGitHubOAuthResult(w, "invalid_github_oauth_callback", "")
		return
	}
	if err := s.consumeGitHubOAuthState(r.Context(), state); err != nil {
		s.redirectGitHubOAuthResult(w, err.Error(), "")
		return
	}
	accessToken, err := s.exchangeGitHubCode(r.Context(), code)
	if err != nil {
		s.redirectGitHubOAuthResult(w, err.Error(), "")
		return
	}
	user, err := s.fetchGitHubUser(r.Context(), accessToken)
	if err != nil {
		s.redirectGitHubOAuthResult(w, "github_oauth_failed", "")
		return
	}
	accountID, err := s.upsertGitHubAccount(r.Context(), user)
	if err != nil {
		s.redirectGitHubOAuthResult(w, "github_oauth_failed", "")
		return
	}
	completionCode, err := s.createGitHubOAuthCompletion(r.Context(), accountID)
	if err != nil {
		s.redirectGitHubOAuthResult(w, "github_oauth_failed", "")
		return
	}
	s.redirectGitHubOAuthResult(w, "", completionCode)
}

func (s *Server) handleGitHubOAuthExchange(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		respondErr(w, http.StatusBadRequest, "oauth_code_required")
		return
	}
	accountID, err := s.consumeGitHubOAuthCompletion(ctx, code)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sessionToken, err := s.createAccountSession(ctx, accountID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"account_id": accountID, "session_token": sessionToken})
}

func (s *Server) buildGitHubAuthorizeURL(state string) (string, error) {
	if !s.githubOAuthConfigured() {
		return "", errors.New("github_oauth_not_configured")
	}
	values := url.Values{}
	values.Set("client_id", s.cfg.GitHubClientID)
	values.Set("redirect_uri", s.githubOAuthCallbackURL())
	values.Set("scope", "read:user")
	values.Set("state", state)
	values.Set("allow_signup", "true")
	values.Set("prompt", "select_account")
	return gitHubAuthorizeURL + "?" + values.Encode(), nil
}

func (s *Server) persistGitHubOAuthState(ctx context.Context, state string) error {
	_ = s.cleanupExpiredGitHubOAuthArtifacts(ctx)
	_, err := s.db.Exec(ctx,
		`INSERT INTO github_oauth_states(id, state_hash) VALUES ($1,$2)`,
		domain.NewID("ghs"), hash(s.cfg.AuthTokenSecret+state),
	)
	return err
}

func (s *Server) consumeGitHubOAuthState(ctx context.Context, state string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var (
		id      string
		created time.Time
		usedAt  *time.Time
	)
	if err := tx.QueryRow(ctx,
		`SELECT id, created_at, used_at FROM github_oauth_states WHERE state_hash = $1 FOR UPDATE`,
		hash(s.cfg.AuthTokenSecret+state),
	).Scan(&id, &created, &usedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("invalid_github_oauth_state")
		}
		return err
	}
	if usedAt != nil || time.Since(created) > githubOAuthStateTTL {
		return errors.New("invalid_github_oauth_state")
	}
	if _, err := tx.Exec(ctx, `UPDATE github_oauth_states SET used_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) createGitHubOAuthCompletion(ctx context.Context, accountID string) (string, error) {
	_ = s.cleanupExpiredGitHubOAuthArtifacts(ctx)
	code := domain.NewID("ghc")
	_, err := s.db.Exec(ctx,
		`INSERT INTO github_oauth_completions(id, code_hash, account_id) VALUES ($1,$2,$3)`,
		domain.NewID("gho"), hash(s.cfg.AuthTokenSecret+code), accountID,
	)
	if err != nil {
		return "", err
	}
	return code, nil
}

func (s *Server) consumeGitHubOAuthCompletion(ctx context.Context, code string) (string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var (
		id        string
		accountID string
		created   time.Time
		usedAt    *time.Time
	)
	if err := tx.QueryRow(ctx,
		`SELECT id, account_id, created_at, used_at FROM github_oauth_completions WHERE code_hash = $1 FOR UPDATE`,
		hash(s.cfg.AuthTokenSecret+code),
	).Scan(&id, &accountID, &created, &usedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("invalid_github_oauth_completion")
		}
		return "", err
	}
	if usedAt != nil || time.Since(created) > githubOAuthCompletionTTL {
		return "", errors.New("invalid_github_oauth_completion")
	}
	if _, err := tx.Exec(ctx, `UPDATE github_oauth_completions SET used_at = now() WHERE id = $1`, id); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return accountID, nil
}

func (s *Server) cleanupExpiredGitHubOAuthArtifacts(ctx context.Context) error {
	retentionSeconds := int(githubOAuthRetentionWindow / time.Second)
	if _, err := s.db.Exec(ctx, `
DELETE FROM github_oauth_states
WHERE created_at < now() - make_interval(secs => $1::int)
   OR (used_at IS NOT NULL AND used_at < now() - make_interval(secs => $1::int))`,
		retentionSeconds,
	); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
DELETE FROM github_oauth_completions
WHERE created_at < now() - make_interval(secs => $1::int)
   OR (used_at IS NOT NULL AND used_at < now() - make_interval(secs => $1::int))`,
		retentionSeconds,
	)
	return err
}

func (s *Server) exchangeGitHubAccessToken(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", s.cfg.GitHubClientID)
	form.Set("client_secret", s.cfg.GitHubClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", s.githubOAuthCallbackURL())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gitHubAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("github_oauth_unavailable")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 8 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", errors.New("github_oauth_unavailable")
	}
	defer res.Body.Close()

	var parsed gitHubAccessTokenResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return "", errors.New("github_oauth_unavailable")
	}
	if strings.TrimSpace(parsed.AccessToken) == "" || parsed.Error != "" {
		return "", errors.New("github_oauth_exchange_failed")
	}
	return parsed.AccessToken, nil
}

func (s *Server) fetchGitHubUserProfile(ctx context.Context, accessToken string) (gitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitHubUserAPIURL, nil)
	if err != nil {
		return gitHubUser{}, errors.New("github_oauth_unavailable")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "clawgrid")

	client := &http.Client{Timeout: 8 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return gitHubUser{}, errors.New("github_oauth_unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return gitHubUser{}, errors.New("github_oauth_user_failed")
	}

	var user gitHubUser
	if err := json.NewDecoder(res.Body).Decode(&user); err != nil {
		return gitHubUser{}, errors.New("github_oauth_user_failed")
	}
	if user.ID == 0 || strings.TrimSpace(user.Login) == "" {
		return gitHubUser{}, errors.New("github_oauth_user_failed")
	}
	return user, nil
}

func (s *Server) upsertGitHubAccount(ctx context.Context, user gitHubUser) (string, error) {
	githubUserID := strconv.FormatInt(user.ID, 10)
	githubLogin := strings.TrimSpace(user.Login)
	if githubUserID == "" || githubLogin == "" {
		return "", errors.New("github_oauth_user_failed")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var accountID string
	err = tx.QueryRow(ctx, `SELECT id FROM accounts WHERE github_user_id = $1 FOR UPDATE`, githubUserID).Scan(&accountID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		accountID = domain.NewID("acct")
		if _, err := tx.Exec(ctx,
			`INSERT INTO accounts(id, name, github_user_id, github_login, avatar_url) VALUES ($1,$2,$3,$4,$5)`,
			accountID, githubLogin, githubUserID, githubLogin, strings.TrimSpace(user.AvatarURL),
		); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		if _, err := tx.Exec(ctx,
			`UPDATE accounts SET name = $2, github_login = $3, avatar_url = $4 WHERE id = $1`,
			accountID, githubLogin, githubLogin, strings.TrimSpace(user.AvatarURL),
		); err != nil {
			return "", err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if err := s.bootstrapGitHubAccount(ctx, accountID); err != nil {
		return "", err
	}
	return accountID, nil
}

func (s *Server) bootstrapGitHubAccount(ctx context.Context, accountID string) error {
	if err := s.ensureWallet(ctx, domain.OwnerAccount, accountID); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE wallets SET balance = GREATEST(balance, $1) WHERE owner_type = 'account' AND owner_id = $2`, s.cfg.AccountInitialBalance, accountID); err != nil {
		return err
	}
	var activeKeyCount int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL`, accountID).Scan(&activeKeyCount); err != nil {
		return err
	}
	if activeKeyCount == 0 {
		if _, _, err := s.createAPIKey(ctx, accountID, "default"); err != nil && !errors.Is(err, errAPIKeyLimitReached) {
			return err
		}
	}
	return nil
}

func (s *Server) redirectGitHubOAuthResult(w http.ResponseWriter, oauthError, oauthComplete string) {
	target := strings.TrimRight(s.cfg.FrontendOrigin, "/")
	if target == "" {
		target = "/"
	}
	parsed, err := url.Parse(target)
	if err != nil {
		http.Error(w, "oauth redirect failed", http.StatusInternalServerError)
		return
	}
	query := parsed.Query()
	if oauthError != "" {
		query.Set("oauth_error", oauthError)
	}
	if oauthComplete != "" {
		query.Set("oauth_complete", oauthComplete)
	}
	parsed.RawQuery = query.Encode()
	w.Header().Set("Location", parsed.String())
	w.WriteHeader(http.StatusSeeOther)
}
