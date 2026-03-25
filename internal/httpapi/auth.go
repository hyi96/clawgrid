package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminPathToken() string {
	if s.cfg.AdminPathToken != "" {
		return s.cfg.AdminPathToken
	}
	sum := sha256.Sum256([]byte("clawgrid-admin:" + s.cfg.AuthTokenSecret))
	return hex.EncodeToString(sum[:])[:24]
}

type handlerFunc func(http.ResponseWriter, *http.Request, domain.Actor)

func (s *Server) auth(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, err := s.resolveActor(r)
		if err != nil {
			respondErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		next(w, r, actor)
	}
}

func (s *Server) resolveActor(r *http.Request) (domain.Actor, error) {
	ctx := r.Context()
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		accountID, credentialType, credentialID, err := s.resolveAccountFromBearer(ctx, token)
		if err != nil {
			return domain.Actor{}, err
		}
		return domain.Actor{
			OwnerType:          domain.OwnerAccount,
			OwnerID:            accountID,
			AuthCredentialType: credentialType,
			AuthCredentialID:   credentialID,
		}, nil
	}
	return domain.Actor{}, errors.New("missing auth")
}

func (s *Server) resolveAccountFromBearer(ctx context.Context, token string) (string, domain.AuthCredentialType, string, error) {
	var accountID, keyID string
	err := s.db.QueryRow(ctx, `SELECT account_id, id FROM api_keys WHERE id = $1 AND revoked_at IS NULL`, token).Scan(&accountID, &keyID)
	if err == nil {
		_, _ = s.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, keyID)
		return accountID, domain.AuthCredentialAPIKey, keyID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", domain.AuthCredentialNone, "", errors.New("invalid auth token")
	}

	h := hash(s.cfg.AuthTokenSecret + token)
	err = s.db.QueryRow(ctx, `SELECT account_id, id FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`, h).Scan(&accountID, &keyID)
	if err == nil {
		_, _ = s.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, keyID)
		return accountID, domain.AuthCredentialAPIKey, keyID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", domain.AuthCredentialNone, "", errors.New("invalid auth token")
	}

	var sessionID string
	err = s.db.QueryRow(ctx, `SELECT account_id, id FROM account_sessions WHERE token_hash = $1 AND revoked_at IS NULL`, h).Scan(&accountID, &sessionID)
	if err == nil {
		_, _ = s.db.Exec(ctx, `UPDATE account_sessions SET last_used_at = now() WHERE id = $1`, sessionID)
		return accountID, domain.AuthCredentialAccountSession, sessionID, nil
	}
	return "", domain.AuthCredentialNone, "", errors.New("invalid auth token")
}

func hash(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}
