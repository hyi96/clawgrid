package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr                  string
	DatabaseURL               string
	FrontendOrigin            string
	PublicAPIBase             string
	DevAuthBypass             bool
	AuthTokenSecret           string
	AdminPathToken            string
	TurnstileSecretKey        string
	GitHubClientID            string
	GitHubClientSecret        string
	WorkerTick                time.Duration
	PostFee                   float64
	ResponderPool             float64
	ResponderStake            float64
	DispatcherPool            float64
	Sink                      float64
	DispatcherStake           float64
	DispatcherRefusalPenalty  float64
	PrompterCancelPenalty     float64
	BadFeedbackTipRefundRatio float64
	AutoReviewPrompterPenalty float64
	AutoReviewResponderReward float64
	AccountInitialBalance     float64
	RefreshInterval           time.Duration
	AccountRefreshTarget      float64
	RoutingWindow             time.Duration
	PoolDwellWindow           time.Duration
	ReviewWindow              time.Duration
	AssignmentDeadline        time.Duration
	PollAssignmentWait        time.Duration
	ResponderActiveWindow     time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:                  getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:               getenv("DATABASE_URL", "postgres://clawgrid:clawgrid@db:5432/clawgrid?sslmode=disable"),
		FrontendOrigin:            getenv("FRONTEND_ORIGIN", "http://localhost:5173"),
		PublicAPIBase:             getenv("PUBLIC_API_BASE", "http://localhost:8080"),
		DevAuthBypass:             getbool("DEV_AUTH_BYPASS", false),
		AuthTokenSecret:           getenv("AUTH_TOKEN_SECRET", "dev-auth-secret"),
		AdminPathToken:            getenv("ADMIN_PATH_TOKEN", ""),
		TurnstileSecretKey:        getenv("TURNSTILE_SECRET_KEY", ""),
		GitHubClientID:            getenv("GITHUB_CLIENT_ID", ""),
		GitHubClientSecret:        getenv("GITHUB_CLIENT_SECRET", ""),
		WorkerTick:                getdurms("WORKER_TICK_MS", 1000),
		PostFee:                   getfloat("POST_FEE", 2.0),
		ResponderPool:             getfloat("RESPONDER_POOL", 1.4),
		ResponderStake:            getfloat("RESPONDER_STAKE", 0.6),
		DispatcherPool:            getfloat("DISPATCHER_POOL", 0.4),
		Sink:                      getfloat("SINK", 0.2),
		DispatcherStake:           getfloat("DISPATCHER_STAKE", 0.2),
		DispatcherRefusalPenalty:  getfloat("DISPATCHER_REFUSAL_PENALTY", 0.1),
		PrompterCancelPenalty:     getfloat("PROMPTER_CANCEL_PENALTY", 0.2),
		BadFeedbackTipRefundRatio: getfloat("BAD_FEEDBACK_TIP_REFUND_RATIO", 0.5),
		AutoReviewPrompterPenalty: getfloat("AUTO_REVIEW_PROMPTER_PENALTY", 0.6),
		AutoReviewResponderReward: getfloat("AUTO_REVIEW_RESPONDER_REWARD", 0.4),
		AccountInitialBalance:     getfloat("ACCOUNT_INITIAL_BALANCE", 100.0),
		RefreshInterval:           getdurh("REFRESH_INTERVAL_HOURS", 5),
		AccountRefreshTarget:      getfloat("ACCOUNT_REFRESH_TARGET", 25.0),
		RoutingWindow:             getdurs("ROUTING_WINDOW_SECONDS", 30),
		PoolDwellWindow:           getdurs("POOL_DWELL_SECONDS", 30),
		ReviewWindow:              getdurh("REVIEW_WINDOW_HOURS", 24),
		AssignmentDeadline:        getdurm("ASSIGNMENT_DEADLINE_MINUTES", 30),
		PollAssignmentWait:        getdurs("POLL_ASSIGNMENT_WAIT_SECONDS", 30),
		ResponderActiveWindow:     getdurs("RESPONDER_ACTIVE_WINDOW_SECONDS", 12),
	}
	const eps = 1e-9
	if math.Abs((cfg.ResponderPool+cfg.DispatcherPool+cfg.Sink)-cfg.PostFee) > eps {
		return Config{}, fmt.Errorf("invalid fee split: responder+dispatcher+sink must equal post fee")
	}
	if cfg.DispatcherRefusalPenalty < 0 || cfg.DispatcherRefusalPenalty > cfg.DispatcherStake {
		return Config{}, fmt.Errorf("invalid dispatcher refusal penalty: must be between 0 and dispatcher stake")
	}
	if cfg.BadFeedbackTipRefundRatio < 0 || cfg.BadFeedbackTipRefundRatio > 1 {
		return Config{}, fmt.Errorf("invalid bad feedback tip refund ratio: must be between 0 and 1")
	}
	return cfg, nil
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func getfloat(k string, d float64) float64 {
	v := getenv(k, "")
	if v == "" {
		return d
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return d
	}
	return f
}

func getdurh(k string, d int) time.Duration {
	v := getenv(k, "")
	if v == "" {
		return time.Duration(d) * time.Hour
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return time.Duration(d) * time.Hour
	}
	return time.Duration(i) * time.Hour
}

func getdurs(k string, d int) time.Duration {
	v := getenv(k, "")
	if v == "" {
		return time.Duration(d) * time.Second
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return time.Duration(d) * time.Second
	}
	return time.Duration(i) * time.Second
}

func getdurm(k string, d int) time.Duration {
	v := getenv(k, "")
	if v == "" {
		return time.Duration(d) * time.Minute
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return time.Duration(d) * time.Minute
	}
	return time.Duration(i) * time.Minute
}

func getdurms(k string, d int) time.Duration {
	v := getenv(k, "")
	if v == "" {
		return time.Duration(d) * time.Millisecond
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return time.Duration(d) * time.Millisecond
	}
	return time.Duration(i) * time.Millisecond
}

func getbool(k string, d bool) bool {
	v := getenv(k, "")
	if v == "" {
		return d
	}
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES", "on", "ON":
		return true
	case "0", "false", "FALSE", "False", "no", "NO", "off", "OFF":
		return false
	default:
		return d
	}
}
