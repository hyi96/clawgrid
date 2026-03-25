package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type rateLimitSpec struct {
	Scope  string
	Key    string
	Limit  int
	Window time.Duration
}

func normalizeRateLimitSpecs(specs []rateLimitSpec) []rateLimitSpec {
	filtered := make([]rateLimitSpec, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Scope) == "" || strings.TrimSpace(spec.Key) == "" || spec.Limit <= 0 || spec.Window <= 0 {
			continue
		}
		filtered = append(filtered, rateLimitSpec{
			Scope:  strings.TrimSpace(spec.Scope),
			Key:    strings.TrimSpace(spec.Key),
			Limit:  spec.Limit,
			Window: spec.Window,
		})
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Scope == filtered[j].Scope {
			return filtered[i].Key < filtered[j].Key
		}
		return filtered[i].Scope < filtered[j].Scope
	})
	return filtered
}

func rateLimitWindowSeconds(window time.Duration) int {
	seconds := int(window / time.Second)
	if seconds <= 0 {
		return 1
	}
	return seconds
}

func lockRateLimitKeyTx(ctx context.Context, tx pgx.Tx, scope, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, scope, key)
	return err
}

func (s *Server) enforceRateLimit(ctx context.Context, errorCode string, specs ...rateLimitSpec) error {
	specs = normalizeRateLimitSpecs(specs)
	if len(specs) == 0 {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, spec := range specs {
		if err := lockRateLimitKeyTx(ctx, tx, spec.Scope, spec.Key); err != nil {
			return err
		}
	}

	limited := false
	for _, spec := range specs {
		if _, err := tx.Exec(ctx, `INSERT INTO rate_limit_events(scope, key, created_at) VALUES ($1,$2,now())`, spec.Scope, spec.Key); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM rate_limit_events
WHERE scope = $1
  AND key = $2
  AND created_at > now() - make_interval(secs => $3::int)`,
			spec.Scope, spec.Key, rateLimitWindowSeconds(spec.Window),
		).Scan(&count); err != nil {
			return err
		}
		if count > spec.Limit {
			limited = true
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if limited {
		return errors.New(errorCode)
	}
	return nil
}

func (s *Server) isRateLimited(ctx context.Context, specs ...rateLimitSpec) (bool, error) {
	specs = normalizeRateLimitSpecs(specs)
	if len(specs) == 0 {
		return false, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	for _, spec := range specs {
		if err := lockRateLimitKeyTx(ctx, tx, spec.Scope, spec.Key); err != nil {
			return false, err
		}
	}

	limited := false
	for _, spec := range specs {
		var count int
		if err := tx.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM rate_limit_events
WHERE scope = $1
  AND key = $2
  AND created_at > now() - make_interval(secs => $3::int)`,
			spec.Scope, spec.Key, rateLimitWindowSeconds(spec.Window),
		).Scan(&count); err != nil {
			return false, err
		}
		if count >= spec.Limit {
			limited = true
			break
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return limited, nil
}

func (s *Server) recordRateLimitEvent(ctx context.Context, scope, key string) error {
	specs := normalizeRateLimitSpecs([]rateLimitSpec{{Scope: scope, Key: key, Limit: 1, Window: time.Second}})
	if len(specs) == 0 {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	spec := specs[0]
	if err := lockRateLimitKeyTx(ctx, tx, spec.Scope, spec.Key); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO rate_limit_events(scope, key, created_at) VALUES ($1,$2,now())`, spec.Scope, spec.Key); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func respondRateLimit(w http.ResponseWriter, errorCode string) {
	respondErr(w, http.StatusTooManyRequests, errorCode)
}

func githubOAuthStartRateLimitSpecs(ip string) []rateLimitSpec {
	return []rateLimitSpec{
		{Scope: "github_oauth_start_ip", Key: ip, Limit: githubOAuthStartIPLimit, Window: githubOAuthStartIPWindow},
	}
}

func apiKeyCreateRateLimitSpecs(accountID string) []rateLimitSpec {
	return []rateLimitSpec{
		{Scope: "api_key_create_account", Key: accountID, Limit: apiKeyCreateLimit, Window: apiKeyCreateWindow},
	}
}

func claimAttemptRateLimitSpecs(accountID string) []rateLimitSpec {
	return []rateLimitSpec{
		{Scope: "claim_attempt_account", Key: accountID, Limit: claimAttemptLimit, Window: claimAttemptWindow},
	}
}

func claimFailureRateLimitSpecs(accountID string) []rateLimitSpec {
	return []rateLimitSpec{
		{Scope: "claim_failure_account", Key: accountID, Limit: claimFailureLimit, Window: claimFailureWindow},
	}
}

func assignmentAttemptRateLimitSpecs(accountID string) []rateLimitSpec {
	return []rateLimitSpec{
		{Scope: "assignment_attempt_account", Key: accountID, Limit: assignmentAttemptLimit, Window: assignmentAttemptWindow},
	}
}

func assignmentFailureRateLimitSpecs(accountID string) []rateLimitSpec {
	return []rateLimitSpec{
		{Scope: "assignment_failure_account", Key: accountID, Limit: assignmentFailureLimit, Window: assignmentFailureWindow},
	}
}

func (s *Server) recordClaimFailure(ctx context.Context, accountID string) {
	_ = s.recordRateLimitEvent(ctx, "claim_failure_account", accountID)
}

func (s *Server) recordAssignmentFailure(ctx context.Context, accountID string) {
	_ = s.recordRateLimitEvent(ctx, "assignment_failure_account", accountID)
}
