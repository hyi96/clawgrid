package httpapi

import (
	"context"
	"net/http"

	"clawgrid/internal/app"
	"clawgrid/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	cfg                config.Config
	db                 *pgxpool.Pool
	svc                *app.Service
	verifyTurnstile    func(context.Context, string, string) error
	exchangeGitHubCode func(context.Context, string, string) (string, error)
	fetchGitHubUser    func(context.Context, string) (gitHubUser, error)
	deliverAgentHook   func(context.Context, agentHookDelivery) error
}

func New(db *pgxpool.Pool, cfg config.Config) *Server {
	s := &Server{cfg: cfg, db: db, svc: app.NewService(db, cfg)}
	s.verifyTurnstile = s.verifyTurnstileToken
	s.exchangeGitHubCode = s.exchangeGitHubAccessToken
	s.fetchGitHubUser = s.fetchGitHubUserProfile
	s.deliverAgentHook = s.deliverAgentHookRequest
	s.svc.SetHookDeliveryFunc(func(ctx context.Context, delivery app.HookDelivery) error {
		return s.deliverAgentHook(ctx, agentHookDelivery{
			URL:       delivery.URL,
			AuthToken: delivery.AuthToken,
			Message:   delivery.Message,
			Name:      delivery.Name,
		})
	})
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /dev/auth/local-session", s.handleLocalDevSession)
	mux.HandleFunc("POST /accounts/oauth/github/start", s.handleGitHubOAuthStart)
	mux.HandleFunc("GET /accounts/oauth/github/callback", s.handleGitHubOAuthCallback)
	mux.HandleFunc("POST /accounts/oauth/github/exchange", s.handleGitHubOAuthExchange)
	mux.HandleFunc("POST /account/logout", s.auth(s.handleAccountLogout))

	mux.HandleFunc("GET /account/me", s.auth(s.handleAccountMe))
	mux.HandleFunc("GET /account/stats", s.auth(s.handleAccountStats))
	mux.HandleFunc("PATCH /account/me", s.auth(s.handleAccountMePatch))
	mux.HandleFunc("GET /account/hook", s.auth(s.handleAccountHookGet))
	mux.HandleFunc("PUT /account/hook", s.auth(s.handleAccountHookPut))
	mux.HandleFunc("DELETE /account/hook", s.auth(s.handleAccountHookDelete))
	mux.HandleFunc("POST /account/hook/enable", s.auth(s.handleAccountHookEnable))
	mux.HandleFunc("POST /account/hook/disable", s.auth(s.handleAccountHookDisable))
	mux.HandleFunc("GET /account/api-keys", s.auth(s.handleAPIKeysList))
	mux.HandleFunc("POST /account/api-keys", s.auth(s.handleAPIKeysCreate))
	mux.HandleFunc("DELETE /account/api-keys/{key_id}", s.auth(s.handleAPIKeysDelete))

	mux.HandleFunc("GET /sessions", s.auth(s.handleSessionsList))
	mux.HandleFunc("POST /sessions", s.auth(s.handleSessionsCreate))
	mux.HandleFunc("GET /sessions/{id}", s.auth(s.handleSessionsGet))
	mux.HandleFunc("GET /sessions/{id}/state", s.auth(s.handleSessionState))
	mux.HandleFunc("PATCH /sessions/{id}", s.auth(s.handleSessionsPatch))
	mux.HandleFunc("DELETE /sessions/{id}", s.auth(s.handleSessionsDelete))
	mux.HandleFunc("GET /sessions/{id}/messages", s.auth(s.handleMessagesList))
	mux.HandleFunc("POST /sessions/{id}/messages", s.auth(s.handleMessagesCreate))

	mux.HandleFunc("GET /jobs/{id}", s.auth(s.handleJobGet))
	mux.HandleFunc("GET /jobs", s.auth(s.handleJobList))
	mux.HandleFunc("GET /routing/jobs", s.handleRoutingJobsPublic)

	mux.HandleFunc("GET /responders/available", s.handleRespondersAvailablePublic)
	mux.HandleFunc("GET /responders/state", s.auth(s.handleResponderState))
	mux.HandleFunc("POST /responders/availability", s.auth(s.handleResponderAvailability))
	mux.HandleFunc("DELETE /responders/availability", s.auth(s.handleResponderAvailabilityDelete))
	mux.HandleFunc("POST /assignments", s.auth(s.handleAssignmentsCreate))
	mux.HandleFunc("GET /assignments/{id}", s.auth(s.handleAssignmentsGet))

	mux.HandleFunc("GET /responders/work", s.auth(s.handleResponderWork))
	mux.HandleFunc("POST /jobs/{id}/claim", s.auth(s.handleJobClaim))
	mux.HandleFunc("POST /jobs/{id}/responder-cancel", s.auth(s.handleResponderJobCancel))
	mux.HandleFunc("POST /jobs/{id}/cancel", s.auth(s.handleJobCancel))
	mux.HandleFunc("POST /jobs/{id}/reply", s.auth(s.handleJobReply))
	mux.HandleFunc("POST /jobs/{id}/vote", s.auth(s.handleJobVote))

	mux.HandleFunc("GET /wallets/current", s.auth(s.handleWalletCurrent))
	mux.HandleFunc("GET /wallets/current/ledger", s.auth(s.handleWalletLedger))
	mux.HandleFunc("GET /leaderboards", s.handleLeaderboardsGet)
	mux.HandleFunc("POST /agent-hooks/verify/{token}", s.handleAgentHookVerify)

	mux.HandleFunc("POST /internal/jobs/auto-review", s.handleInternalAutoReview)
	mux.HandleFunc("POST /internal/jobs/process-routing-expiry", s.handleInternalRoutingExpiry)
	mux.HandleFunc("POST /internal/jobs/process-pool-rotation", s.handleInternalPoolRotation)
	mux.HandleFunc("POST /internal/assignments/process-timeouts", s.handleInternalAssignmentTimeouts)
	mux.HandleFunc("POST /internal/wallets/process-refresh", s.handleInternalWalletRefresh)
	mux.HandleFunc("POST /internal/account-hooks/process-deliveries", s.handleInternalAccountHookDeliveries)
	mux.HandleFunc("POST /internal/sessions/process-empty-cleanup", s.handleInternalSessionCleanup)
	mux.HandleFunc("POST /internal/leaderboards/refresh", s.handleInternalLeaderboardRefresh)
	mux.HandleFunc("GET /admin/jobs/stuck", s.handleAdminStuckJobs)
	if token := s.adminPathToken(); token != "" {
		mux.HandleFunc("GET /_private/"+token+"/admin/overview", s.handlePrivateAdminOverview)
	}

	return withCORS(mux, s.cfg.FrontendOrigin)
}
