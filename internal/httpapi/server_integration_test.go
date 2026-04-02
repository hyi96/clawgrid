package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clawgrid/internal/app"
	"clawgrid/internal/config"
	"clawgrid/internal/db"
	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

var testGitHubUserSeq int64

type integrationHarness struct {
	app       *Server
	server    *httptest.Server
	adminPool *pgxpool.Pool
	appPool   *pgxpool.Pool
	baseURL   string
	schema    string
}

func TestResponderWorkReturnsEligibleSystemPoolCandidate(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "hello from tom")

	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)

	if work.Mode != "pool" {
		t.Fatalf("mode = %q, want %q", work.Mode, "pool")
	}
	if len(work.Candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(work.Candidates))
	}
	if work.Candidates[0].ID != jobID {
		t.Fatalf("candidate id = %q, want %q", work.Candidates[0].ID, jobID)
	}
}

func TestRoutingJobAndSessionContentAreNotPublicToOtherAccounts(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	other := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "private routing content")

	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, other.apiKey, nil, http.StatusForbidden, nil)
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/messages", other.apiKey, nil, http.StatusForbidden, nil)
}

func TestResponderWorkLongPollsBeforeReturningPoolCandidates(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 200 * time.Millisecond
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "hello after wait")

	start := time.Now()
	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	elapsed := time.Since(start)

	if elapsed < 180*time.Millisecond {
		t.Fatalf("responders/work returned too quickly: %v", elapsed)
	}
	if work.Mode != "pool" {
		t.Fatalf("mode = %q, want %q", work.Mode, "pool")
	}
	if len(work.Candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(work.Candidates))
	}
	if work.Candidates[0].ID != jobID {
		t.Fatalf("candidate id = %q, want %q", work.Candidates[0].ID, jobID)
	}
}

func TestResponderStateReportsExternalPolling(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 300 * time.Millisecond
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	_ = h.postMessage(t, prompter.apiKey, sessionID, "hello after wait")

	client := &http.Client{}
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, h.baseURL+"/responders/work", nil)
		if err != nil {
			done <- err
			return
		}
		req.Header.Set("Authorization", "Bearer "+responder.apiKey)
		resp, err := client.Do(req)
		if err != nil {
			done <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			done <- fmt.Errorf("responders/work status = %d", resp.StatusCode)
			return
		}
		done <- nil
	}()

	time.Sleep(100 * time.Millisecond)

	var state struct {
		Mode             string `json:"mode"`
		RemainingSeconds int    `json:"remaining_seconds"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/state", responder.apiKey, nil, http.StatusOK, &state)
	if state.Mode != "polling" {
		t.Fatalf("mode = %q, want %q", state.Mode, "polling")
	}
	if state.RemainingSeconds < 0 {
		t.Fatalf("remaining_seconds = %d, want >= 0", state.RemainingSeconds)
	}

	if err := <-done; err != nil {
		t.Fatalf("long poll request failed: %v", err)
	}
}

func TestResponderStateReturnsExternalPoolSnapshot(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 200 * time.Millisecond
		cfg.PoolDwellWindow = 5 * time.Second
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "hello after wait")

	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" {
		t.Fatalf("work mode = %q, want %q", work.Mode, "pool")
	}
	if len(work.Candidates) != 1 || work.Candidates[0].ID != jobID {
		t.Fatalf("unexpected work candidates: %+v", work.Candidates)
	}

	time.Sleep(25 * time.Millisecond)

	var state struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/state", responder.apiKey, nil, http.StatusOK, &state)
	if state.Mode != "pool" {
		t.Fatalf("state mode = %q, want %q", state.Mode, "pool")
	}
	if len(state.Candidates) != 1 || state.Candidates[0].ID != jobID {
		t.Fatalf("unexpected state candidates: %+v", state.Candidates)
	}
}

func TestResponderStatePrefersPoolSnapshotOverStalePollingRow(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PoolDwellWindow = 5 * time.Second
		cfg.PollAssignmentWait = 30 * time.Second
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "hello after wait")

	h.execSQL(t, `
INSERT INTO responder_availability(id, owner_type, owner_id, last_seen_at, poll_started_at)
VALUES ($1, 'account', $2, now(), now())`,
		domain.NewID("av"), responder.accountID)

	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, nil)

	var state struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/state", responder.apiKey, nil, http.StatusOK, &state)
	if state.Mode != "pool" {
		t.Fatalf("state mode = %q, want %q", state.Mode, "pool")
	}
	if len(state.Candidates) != 1 || state.Candidates[0].ID != jobID {
		t.Fatalf("unexpected state candidates: %+v", state.Candidates)
	}
}

func TestJobClaimReturnsJobPayload(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "hello from tom")

	var claim struct {
		OK               bool    `json:"ok"`
		JobID            string  `json:"job_id"`
		ID               string  `json:"id"`
		Status           string  `json:"status"`
		TipAmount        float64 `json:"tip_amount"`
		TimeLimitMinutes int     `json:"time_limit_minutes"`
		SessionID        string  `json:"session_id"`
		RequestMessageID string  `json:"request_message_id"`
		ClaimOwnerType   string  `json:"claim_owner_type"`
		ClaimOwnerID     string  `json:"claim_owner_id"`
		WorkDeadlineAt   string  `json:"work_deadline_at"`
	}
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, &claim)

	if !claim.OK {
		t.Fatal("claim ok = false, want true")
	}
	if claim.JobID != jobID || claim.ID != jobID {
		t.Fatalf("unexpected claim ids: %+v", claim)
	}
	if claim.SessionID != sessionID {
		t.Fatalf("session_id = %q, want %q", claim.SessionID, sessionID)
	}
	if claim.Status != "system_pool" {
		t.Fatalf("status = %q, want %q", claim.Status, "system_pool")
	}
	if claim.ClaimOwnerType != "account" || claim.ClaimOwnerID != responder.accountID {
		t.Fatalf("unexpected claim owner: %+v", claim)
	}
	if claim.RequestMessageID == "" || claim.WorkDeadlineAt == "" {
		t.Fatalf("claim response missing job payload fields: %+v", claim)
	}
}

func TestMessageCreateRejectsTimeLimitBelowOneMinute(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	sessionID := h.createSession(t, prompter.apiKey)

	h.requestJSON(t, http.MethodPost, "/sessions/"+sessionID+"/messages", prompter.apiKey, map[string]any{
		"content":            "hello from tom",
		"time_limit_minutes": 0,
	}, http.StatusBadRequest, nil)
}

func TestRepeatedClaimBySameResponderIsIdempotent(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "hello from tom")

	startBalance := h.walletBalance(t, responder.apiKey)

	var first struct {
		JobID          string `json:"job_id"`
		ClaimExpiresAt string `json:"claim_expires_at"`
		ClaimOwnerID   string `json:"claim_owner_id"`
	}
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, &first)

	afterFirstBalance := h.walletBalance(t, responder.apiKey)
	if afterFirstBalance != startBalance-0.6 {
		t.Fatalf("balance after first claim = %v, want %v", afterFirstBalance, startBalance-0.6)
	}

	var second struct {
		JobID          string `json:"job_id"`
		ClaimExpiresAt string `json:"claim_expires_at"`
		ClaimOwnerID   string `json:"claim_owner_id"`
	}
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, &second)

	afterSecondBalance := h.walletBalance(t, responder.apiKey)
	if afterSecondBalance != afterFirstBalance {
		t.Fatalf("balance after second claim = %v, want unchanged %v", afterSecondBalance, afterFirstBalance)
	}
	if second.JobID != first.JobID {
		t.Fatalf("second job_id = %q, want %q", second.JobID, first.JobID)
	}
	if second.ClaimOwnerID != responder.accountID {
		t.Fatalf("second claim_owner_id = %q, want %q", second.ClaimOwnerID, responder.accountID)
	}
	if second.ClaimExpiresAt != first.ClaimExpiresAt {
		t.Fatalf("claim_expires_at changed on repeated claim: first=%q second=%q", first.ClaimExpiresAt, second.ClaimExpiresAt)
	}

	holdCount := h.scalarInt(t, `
SELECT COUNT(*)::int
FROM wallet_ledger
WHERE owner_type = 'account'
  AND owner_id = $1
  AND job_id = $2
  AND reason = 'responder_stake_hold'`,
		responder.accountID, jobID)
	if holdCount != 1 {
		t.Fatalf("stake hold ledger count = %d, want 1", holdCount)
	}
}

func TestJobClaimRateLimitedAfterRepeatedFailures(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	holder := h.registerAccount(t, "holder")
	spammer := h.registerAccount(t, "spammer")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "hello from tom")

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", holder.apiKey, nil, http.StatusOK, nil)

	for i := 0; i < claimFailureLimit; i++ {
		h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", spammer.apiKey, nil, http.StatusConflict, nil)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", spammer.apiKey, nil, http.StatusTooManyRequests, nil)
}

func TestResponderWorkRejectsSecondConcurrentPollForSameAccount(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 300 * time.Millisecond
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	_ = h.postMessage(t, prompter.apiKey, sessionID, "hello after wait")

	client := &http.Client{}
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, h.baseURL+"/responders/work", nil)
		if err != nil {
			done <- err
			return
		}
		req.Header.Set("Authorization", "Bearer "+responder.apiKey)
		resp, err := client.Do(req)
		if err != nil {
			done <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			done <- fmt.Errorf("first responders/work status = %d", resp.StatusCode)
			return
		}
		done <- nil
	}()

	time.Sleep(100 * time.Millisecond)
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusConflict, nil)

	if err := <-done; err != nil {
		t.Fatalf("first long poll request failed: %v", err)
	}
}

func TestJobClaimRejectsResponderWithOtherActiveWork(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	responder := h.registerAccount(t, "noah")
	prompterA := h.registerAccount(t, "tom")
	prompterB := h.registerAccount(t, "jerry")

	sessionA := h.createSession(t, prompterA.apiKey)
	jobAssigned := h.postMessage(t, prompterA.apiKey, sessionA, "assigned job")
	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobAssigned,
		"responder_id": responder.accountID,
	}, http.StatusCreated, nil)

	sessionB := h.createSession(t, prompterB.apiKey)
	jobPool := h.postMessage(t, prompterB.apiKey, sessionB, "pool job")
	h.execSQL(t, `UPDATE jobs SET status = 'system_pool', last_system_pool_entered_at = now() WHERE id = $1`, jobPool)

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobPool+"/claim", responder.apiKey, nil, http.StatusConflict, nil)
}

func TestJobClaimRejectsSecondClaimForSameResponder(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	responder := h.registerAccount(t, "noah")
	prompterA := h.registerAccount(t, "tom")
	prompterB := h.registerAccount(t, "jerry")

	sessionA := h.createSession(t, prompterA.apiKey)
	jobA := h.postMessage(t, prompterA.apiKey, sessionA, "pool job a")
	h.execSQL(t, `UPDATE jobs SET status = 'system_pool', last_system_pool_entered_at = now() WHERE id = $1`, jobA)

	sessionB := h.createSession(t, prompterB.apiKey)
	jobB := h.postMessage(t, prompterB.apiKey, sessionB, "pool job b")
	h.execSQL(t, `UPDATE jobs SET status = 'system_pool', last_system_pool_entered_at = now() WHERE id = $1`, jobB)

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobA+"/claim", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobB+"/claim", responder.apiKey, nil, http.StatusConflict, nil)
}

func TestJobClaimAllowsOnlyOneConcurrentClaimForSameResponder(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	responder := h.registerAccount(t, "noah")
	prompterA := h.registerAccount(t, "tom")
	prompterB := h.registerAccount(t, "jerry")

	sessionA := h.createSession(t, prompterA.apiKey)
	jobA := h.postMessage(t, prompterA.apiKey, sessionA, "pool job a")
	h.execSQL(t, `UPDATE jobs SET status = 'system_pool', last_system_pool_entered_at = now() WHERE id = $1`, jobA)

	sessionB := h.createSession(t, prompterB.apiKey)
	jobB := h.postMessage(t, prompterB.apiKey, sessionB, "pool job b")
	h.execSQL(t, `UPDATE jobs SET status = 'system_pool', last_system_pool_entered_at = now() WHERE id = $1`, jobB)

	type claimResult struct {
		jobID  string
		status int
		body   string
		err    error
	}

	client := &http.Client{}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup

	claim := func(jobID string) {
		defer wg.Done()
		<-start
		req, err := http.NewRequest(http.MethodPost, h.baseURL+"/jobs/"+jobID+"/claim", nil)
		if err != nil {
			results <- claimResult{jobID: jobID, err: err}
			return
		}
		req.Header.Set("Authorization", "Bearer "+responder.apiKey)
		resp, err := client.Do(req)
		if err != nil {
			results <- claimResult{jobID: jobID, err: err}
			return
		}
		defer resp.Body.Close()
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		results <- claimResult{jobID: jobID, status: resp.StatusCode, body: buf.String()}
	}

	wg.Add(2)
	go claim(jobA)
	go claim(jobB)
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	claimedJobID := ""
	for result := range results {
		if result.err != nil {
			t.Fatalf("claim request for %s failed: %v", result.jobID, result.err)
		}
		switch result.status {
		case http.StatusOK:
			successes++
			claimedJobID = result.jobID
		case http.StatusConflict:
			conflicts++
			if !strings.Contains(result.body, "responder_busy") {
				t.Fatalf("conflict body = %q, want responder_busy", result.body)
			}
		default:
			t.Fatalf("unexpected status for %s: %d body=%s", result.jobID, result.status, result.body)
		}
	}

	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}

	activeClaimCount := h.scalarInt(t, `
SELECT COUNT(*)::int
FROM jobs
WHERE status = 'system_pool'
  AND response_message_id IS NULL
  AND claim_owner_type = 'account'
  AND claim_owner_id = $1
  AND claim_expires_at > now()`, responder.accountID)
	if activeClaimCount != 1 {
		t.Fatalf("active claim count = %d, want 1", activeClaimCount)
	}

	holdCount := h.scalarInt(t, `
SELECT COUNT(*)::int
FROM wallet_ledger
WHERE owner_type = 'account'
  AND owner_id = $1
  AND reason = 'responder_stake_hold'`, responder.accountID)
	if holdCount != 1 {
		t.Fatalf("stake hold ledger count = %d, want 1", holdCount)
	}

	if claimedJobID == "" {
		t.Fatal("claimedJobID empty, want one successful claim")
	}
}

func TestAssignmentRejectsUnknownResponderIdentity(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	prompter := h.registerAccount(t, "tom")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this directly")

	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": "acct_missing",
	}, http.StatusNotFound, nil)
}

func TestAssignmentAcceptsLegacyResponderOwnerID(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this directly")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_owner_id": responder.accountID,
	}, http.StatusCreated, nil)
}

func TestAssignmentRequiresLiveResponderAvailability(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this directly")

	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusConflict, nil)
}

func TestAssignmentRequiresDispatcherBalanceForPenaltyHold(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this directly")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	h.execSQL(t, `UPDATE wallets SET balance = 0.1 WHERE owner_type = 'account' AND owner_id = $1`, dispatcher.accountID)

	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusPaymentRequired, nil)

	if got := h.walletBalance(t, dispatcher.apiKey); math.Abs(got-0.1) > 1e-9 {
		t.Fatalf("dispatcher balance = %v, want %v", got, 0.1)
	}
	if got := h.walletBalance(t, responder.apiKey); math.Abs(got-100.0) > 1e-9 {
		t.Fatalf("responder balance = %v, want %v", got, 100.0)
	}
	var job struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, prompter.apiKey, nil, http.StatusOK, &job)
	if job.Status != "routing" {
		t.Fatalf("job status = %q, want %q", job.Status, "routing")
	}
}

func TestCancellingResponderAvailabilityPreventsLaterAssignment(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this directly")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodDelete, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusConflict, nil)
}

func TestCancellingResponderAvailabilityReturnsAssignedJobIfAssignmentWonRace(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this directly")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, nil)

	var cancel struct {
		OK    bool   `json:"ok"`
		Mode  string `json:"mode"`
		JobID string `json:"job_id"`
	}
	h.requestJSON(t, http.MethodDelete, "/responders/availability", responder.apiKey, nil, http.StatusOK, &cancel)
	if cancel.OK {
		t.Fatal("cancel ok = true, want false when assignment already exists")
	}
	if cancel.Mode != "assigned" {
		t.Fatalf("cancel mode = %q, want %q", cancel.Mode, "assigned")
	}
	if cancel.JobID != jobID {
		t.Fatalf("cancel job_id = %q, want %q", cancel.JobID, jobID)
	}
}

func TestAssignmentRateLimitedAfterRepeatedFailures(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	prompter := h.registerAccount(t, "tom")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this directly")

	for i := 0; i < assignmentFailureLimit; i++ {
		h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
			"job_id":             jobID,
			"responder_id": "acct_missing",
		}, http.StatusNotFound, nil)
	}

	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": "acct_missing",
	}, http.StatusTooManyRequests, nil)
}

func TestPrompterCannotSendNewMessageWhileFeedbackIsPending(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "first prompt")
	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	h.requestJSON(t, http.MethodPost, "/assignments", prompter.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, nil)

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{
		"content": "first reply",
	}, http.StatusOK, nil)

	var job struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, prompter.apiKey, nil, http.StatusOK, &job)
	if job.Status != "review_pending" {
		t.Fatalf("job status = %q, want %q", job.Status, "review_pending")
	}

	h.requestJSON(t, http.MethodPost, "/sessions/"+sessionID+"/messages", prompter.apiKey, map[string]any{
		"content": "second prompt should be blocked",
	}, http.StatusConflict, nil)
}

func TestSessionStateTracksPromptToFeedbackCycle(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)

	var initial struct {
		State     string `json:"state"`
		ActiveJob any    `json:"active_job"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/state", prompter.apiKey, nil, http.StatusOK, &initial)
	if initial.State != "ready_for_prompt" {
		t.Fatalf("initial state = %q, want %q", initial.State, "ready_for_prompt")
	}
	if initial.ActiveJob != nil {
		t.Fatalf("initial active_job = %#v, want nil", initial.ActiveJob)
	}

	jobID := h.postMessage(t, prompter.apiKey, sessionID, "first prompt")
	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var waiting struct {
		State     string `json:"state"`
		ActiveJob struct {
			ID string `json:"id"`
		} `json:"active_job"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/state", prompter.apiKey, nil, http.StatusOK, &waiting)
	if waiting.State != "waiting_for_responder" {
		t.Fatalf("waiting state = %q, want %q", waiting.State, "waiting_for_responder")
	}
	if waiting.ActiveJob.ID != jobID {
		t.Fatalf("waiting active job = %q, want %q", waiting.ActiveJob.ID, jobID)
	}

	h.requestJSON(t, http.MethodPost, "/assignments", prompter.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, nil)

	var working struct {
		State     string `json:"state"`
		ActiveJob struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"active_job"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/state", prompter.apiKey, nil, http.StatusOK, &working)
	if working.State != "responder_working" {
		t.Fatalf("working state = %q, want %q", working.State, "responder_working")
	}
	if working.ActiveJob.ID != jobID || working.ActiveJob.Status != "assigned" {
		t.Fatalf("unexpected working payload: %+v", working.ActiveJob)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{
		"content": "first reply",
	}, http.StatusOK, nil)

	var feedback struct {
		State     string `json:"state"`
		CanVote   bool   `json:"can_vote"`
		ActiveJob struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"active_job"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/state", prompter.apiKey, nil, http.StatusOK, &feedback)
	if feedback.State != "feedback_required" {
		t.Fatalf("feedback state = %q, want %q", feedback.State, "feedback_required")
	}
	if !feedback.CanVote {
		t.Fatal("feedback state should allow voting")
	}
	if feedback.ActiveJob.ID != jobID || feedback.ActiveJob.Status != "review_pending" {
		t.Fatalf("unexpected feedback payload: %+v", feedback.ActiveJob)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/vote", prompter.apiKey, map[string]any{
		"vote": "up",
	}, http.StatusOK, nil)

	var readyAgain struct {
		State     string `json:"state"`
		ActiveJob any    `json:"active_job"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/state", prompter.apiKey, nil, http.StatusOK, &readyAgain)
	if readyAgain.State != "ready_for_prompt" {
		t.Fatalf("readyAgain state = %q, want %q", readyAgain.State, "ready_for_prompt")
	}
	if readyAgain.ActiveJob != nil {
		t.Fatalf("readyAgain active_job = %#v, want nil", readyAgain.ActiveJob)
	}
}

func TestSessionsListMarksPendingFeedback(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)

	var sessions struct {
		Items []struct {
			ID              string `json:"id"`
			PendingFeedback bool   `json:"pending_feedback"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions", prompter.apiKey, nil, http.StatusOK, &sessions)
	if len(sessions.Items) != 1 || sessions.Items[0].ID != sessionID || sessions.Items[0].PendingFeedback {
		t.Fatalf("initial sessions payload = %+v, want pending_feedback false", sessions.Items)
	}

	jobID := h.postMessage(t, prompter.apiKey, sessionID, "first prompt")
	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/assignments", prompter.apiKey, map[string]any{
		"job_id":       jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{
		"content": "first reply",
	}, http.StatusOK, nil)

	h.requestJSON(t, http.MethodGet, "/sessions", prompter.apiKey, nil, http.StatusOK, &sessions)
	if len(sessions.Items) != 1 || !sessions.Items[0].PendingFeedback {
		t.Fatalf("after reply sessions payload = %+v, want pending_feedback true", sessions.Items)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/vote", prompter.apiKey, map[string]any{
		"vote": "up",
	}, http.StatusOK, nil)

	h.requestJSON(t, http.MethodGet, "/sessions", prompter.apiKey, nil, http.StatusOK, &sessions)
	if len(sessions.Items) != 1 || sessions.Items[0].PendingFeedback {
		t.Fatalf("after vote sessions payload = %+v, want pending_feedback false", sessions.Items)
	}
}

func TestMessagesListSupportsLatestLimit(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	sessionID := h.createSession(t, prompter.apiKey)
	msg1 := domain.NewID("msg")
	msg2 := domain.NewID("msg")
	msg3 := domain.NewID("msg")
	msg4 := domain.NewID("msg")
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content, created_at) VALUES ($1,$2,'account',$3,'text','prompter','m1', now() - interval '4 seconds')`, msg1, sessionID, prompter.accountID)
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content, created_at) VALUES ($1,$2,'account',$3,'text','prompter','m2', now() - interval '3 seconds')`, msg2, sessionID, prompter.accountID)
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content, created_at) VALUES ($1,$2,'account',$3,'text','prompter','m3', now() - interval '2 seconds')`, msg3, sessionID, prompter.accountID)
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content, created_at) VALUES ($1,$2,'account',$3,'text','prompter','m4', now() - interval '1 second')`, msg4, sessionID, prompter.accountID)

	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"items"`
		HasMoreOlder bool   `json:"has_more_older"`
		NextBeforeID string `json:"next_before_id"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/messages?limit=2", prompter.apiKey, nil, http.StatusOK, &out)
	if len(out.Items) != 2 {
		t.Fatalf("message count = %d, want 2", len(out.Items))
	}
	if out.Items[0].Content != "m3" || out.Items[1].Content != "m4" {
		t.Fatalf("unexpected limited messages: %+v", out.Items)
	}
	if !out.HasMoreOlder {
		t.Fatal("has_more_older = false, want true")
	}
	if out.NextBeforeID != msg3 {
		t.Fatalf("next_before_id = %q, want %q", out.NextBeforeID, msg3)
	}

	var older struct {
		Items []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"items"`
		HasMoreOlder bool   `json:"has_more_older"`
		NextBeforeID string `json:"next_before_id"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/messages?limit=2&before_id="+msg3, prompter.apiKey, nil, http.StatusOK, &older)
	if len(older.Items) != 2 {
		t.Fatalf("older message count = %d, want 2", len(older.Items))
	}
	if older.Items[0].Content != "m1" || older.Items[1].Content != "m2" {
		t.Fatalf("unexpected older messages: %+v", older.Items)
	}
	if older.HasMoreOlder {
		t.Fatal("older has_more_older = true, want false")
	}
	if older.NextBeforeID != "" {
		t.Fatalf("older next_before_id = %q, want empty", older.NextBeforeID)
	}

	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/messages?limit=0", prompter.apiKey, nil, http.StatusBadRequest, nil)
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/messages?before_id="+msg3, prompter.apiKey, nil, http.StatusBadRequest, nil)
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/messages?limit=2&before_id=msg_missing", prompter.apiKey, nil, http.StatusBadRequest, nil)
}

func TestSessionCreateAcceptsOptionalTitle(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	account := h.registerAccount(t, "tom")
	var created struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	h.requestJSON(t, http.MethodPost, "/sessions", account.apiKey, map[string]any{
		"title": "incident thread",
	}, http.StatusCreated, &created)

	if created.Title != "incident thread" {
		t.Fatalf("created title = %q, want %q", created.Title, "incident thread")
	}

	var session struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+created.ID, account.apiKey, nil, http.StatusOK, &session)
	if session.Title != "incident thread" {
		t.Fatalf("stored title = %q, want %q", session.Title, "incident thread")
	}
}

func TestGuestSessionEndpointUnavailable(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	h.rawRequest(t, &http.Client{}, http.MethodPost, "/guest/sessions", nil, nil, http.StatusNotFound)
}

func TestSessionDeleteSoftDeletesWithoutRemovingHistory(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	dispatcher := h.registerAccount(t, "dora")

	now := time.Now().UTC()
	sessionID := domain.NewID("ses")
	requestMessageID := domain.NewID("msg")
	responseMessageID := domain.NewID("msg")
	jobID := domain.NewID("job")
	assignmentID := domain.NewID("asn")

	h.execSQL(t, `INSERT INTO sessions(id, owner_type, owner_id, status, title, created_at) VALUES ($1, 'account', $2, 'active', 'history', $3)`,
		sessionID, prompter.accountID, now.Add(-2*time.Hour))
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content, created_at) VALUES ($1, $2, 'account', $3, 'text', 'prompter', 'prompt', $4)`,
		requestMessageID, sessionID, prompter.accountID, now.Add(-2*time.Hour))
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content, created_at) VALUES ($1, $2, 'account', $3, 'text', 'responder', 'reply', $4)`,
		responseMessageID, sessionID, responder.accountID, now.Add(-90*time.Minute))
	h.execSQL(t, `
INSERT INTO jobs(
  id, session_id, request_message_id, owner_type, owner_id, status,
  created_at, activated_at, routing_ends_at, response_message_id,
  tip_amount, post_fee_amount, prompter_vote, review_deadline_at
)
VALUES ($1,$2,$3,'account',$4,'completed',$5,$5,$5,$6,0,2,'up',$7)`,
		jobID, sessionID, requestMessageID, prompter.accountID, now.Add(-2*time.Hour), responseMessageID, now.Add(-80*time.Minute))
h.execSQL(t, `INSERT INTO assignments(id, job_id, dispatcher_owner_type, dispatcher_owner_id, responder_owner_type, responder_owner_id, assigned_at, deadline_at, status) VALUES ($1,$2,'account',$3,'account',$4,$5,$6,'success')`,
		assignmentID, jobID, dispatcher.accountID, responder.accountID, now.Add(-100*time.Minute), now.Add(-95*time.Minute))

	messagesBefore := h.scalarInt(t, `SELECT COUNT(*)::int FROM messages WHERE session_id = $1`, sessionID)
	jobsBefore := h.scalarInt(t, `SELECT COUNT(*)::int FROM jobs WHERE session_id = $1`, sessionID)
	assignmentsBefore := h.scalarInt(t, `SELECT COUNT(*)::int FROM assignments WHERE job_id = $1`, jobID)

	h.requestJSON(t, http.MethodDelete, "/sessions/"+sessionID, prompter.apiKey, nil, http.StatusOK, nil)

	if deletedCount := h.scalarInt(t, `SELECT COUNT(*)::int FROM sessions WHERE id = $1 AND deleted_at IS NOT NULL`, sessionID); deletedCount != 1 {
		t.Fatalf("deleted session count = %d, want 1", deletedCount)
	}
	if messagesAfter := h.scalarInt(t, `SELECT COUNT(*)::int FROM messages WHERE session_id = $1`, sessionID); messagesAfter != messagesBefore {
		t.Fatalf("messages after delete = %d, want %d", messagesAfter, messagesBefore)
	}
	if jobsAfter := h.scalarInt(t, `SELECT COUNT(*)::int FROM jobs WHERE session_id = $1`, sessionID); jobsAfter != jobsBefore {
		t.Fatalf("jobs after delete = %d, want %d", jobsAfter, jobsBefore)
	}
	if assignmentsAfter := h.scalarInt(t, `SELECT COUNT(*)::int FROM assignments WHERE job_id = $1`, jobID); assignmentsAfter != assignmentsBefore {
		t.Fatalf("assignments after delete = %d, want %d", assignmentsAfter, assignmentsBefore)
	}

	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID, prompter.apiKey, nil, http.StatusNotFound, nil)

	var listed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions", prompter.apiKey, nil, http.StatusOK, &listed)
	for _, item := range listed.Items {
		if item.ID == sessionID {
			t.Fatal("soft-deleted session was still listed")
		}
	}
}

func TestGitHubOAuthStartRequiresFrontendOrigin(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PublicAPIBase = "http://localhost:8080"
		cfg.GitHubClientID = "github-client-id"
		cfg.GitHubClientSecret = "github-client-secret"
	})
	h.requestJSON(t, http.MethodPost, "/accounts/oauth/github/start", "", map[string]any{"turnstile_token": "good-token"}, http.StatusForbidden, nil)
}

func TestSessionsCreateRejectsSecondEmptySession(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	account := h.registerAccount(t, "tom")

	firstSessionID := h.createSession(t, account.apiKey)

	var out struct {
		Error             string `json:"error"`
		ExistingSessionID string `json:"existing_session_id"`
	}
	h.requestJSON(t, http.MethodPost, "/sessions", account.apiKey, nil, http.StatusConflict, &out)
	if out.Error != "empty_session_exists" {
		t.Fatalf("error = %q, want %q", out.Error, "empty_session_exists")
	}
	if out.ExistingSessionID != firstSessionID {
		t.Fatalf("existing_session_id = %q, want %q", out.ExistingSessionID, firstSessionID)
	}
}

func TestSessionsCreateAllowsNewSessionAfterFirstMessage(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	account := h.registerAccount(t, "tom")

	firstSessionID := h.createSession(t, account.apiKey)
	_ = h.postMessage(t, account.apiKey, firstSessionID, "hello")

	var created struct {
		ID string `json:"id"`
	}
	h.requestJSON(t, http.MethodPost, "/sessions", account.apiKey, nil, http.StatusCreated, &created)
	if created.ID == "" || created.ID == firstSessionID {
		t.Fatalf("created session id = %q, want new non-empty id", created.ID)
	}
}

func TestLocalDevSessionRequiresBypass(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	h.requestJSONWithHeaders(t, http.MethodPost, "/dev/auth/local-session", "", map[string]string{
		"Origin": h.app.cfg.FrontendOrigin,
	}, map[string]any{"browser_id": "chrome-profile"}, http.StatusNotFound, nil)
}

func TestLocalDevSessionCreatesStablePerBrowserAccount(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.DevAuthBypass = true
	})

	var chromeFirst struct {
		AccountID    string `json:"account_id"`
		SessionToken string `json:"session_token"`
	}
	h.requestJSONWithHeaders(t, http.MethodPost, "/dev/auth/local-session", "", map[string]string{
		"Origin": h.app.cfg.FrontendOrigin,
	}, map[string]any{"browser_id": "chrome-profile"}, http.StatusOK, &chromeFirst)
	if chromeFirst.AccountID == "" || chromeFirst.SessionToken == "" {
		t.Fatalf("unexpected first local dev login result: %+v", chromeFirst)
	}

	var chromeMe struct {
		ID string `json:"id"`
	}
	h.requestJSON(t, http.MethodGet, "/account/me", chromeFirst.SessionToken, nil, http.StatusOK, &chromeMe)
	if chromeMe.ID != chromeFirst.AccountID {
		t.Fatalf("chrome me.id = %q, want %q", chromeMe.ID, chromeFirst.AccountID)
	}

	var chromeSecond struct {
		AccountID    string `json:"account_id"`
		SessionToken string `json:"session_token"`
	}
	h.requestJSONWithHeaders(t, http.MethodPost, "/dev/auth/local-session", "", map[string]string{
		"Origin": h.app.cfg.FrontendOrigin,
	}, map[string]any{"browser_id": "chrome-profile"}, http.StatusOK, &chromeSecond)
	if chromeSecond.AccountID != chromeFirst.AccountID {
		t.Fatalf("same browser created different account: %q vs %q", chromeSecond.AccountID, chromeFirst.AccountID)
	}
	if chromeSecond.SessionToken == chromeFirst.SessionToken {
		t.Fatalf("same browser reused session token: %q", chromeSecond.SessionToken)
	}

	var edge struct {
		AccountID    string `json:"account_id"`
		SessionToken string `json:"session_token"`
	}
	h.requestJSONWithHeaders(t, http.MethodPost, "/dev/auth/local-session", "", map[string]string{
		"Origin": h.app.cfg.FrontendOrigin,
	}, map[string]any{"browser_id": "edge-profile"}, http.StatusOK, &edge)
	if edge.AccountID == chromeFirst.AccountID {
		t.Fatalf("different browsers shared account: %q", edge.AccountID)
	}

	if got := h.scalarInt(t, `SELECT COUNT(*)::int FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL`, chromeFirst.AccountID); got != 1 {
		t.Fatalf("chrome api key count = %d, want 1", got)
	}
	if got := h.scalarInt(t, `SELECT COUNT(*)::int FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL`, edge.AccountID); got != 1 {
		t.Fatalf("edge api key count = %d, want 1", got)
	}
}

func TestGitHubOAuthStartRequiresTurnstileWhenConfigured(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PublicAPIBase = "http://localhost:8080"
		cfg.GitHubClientID = "github-client-id"
		cfg.GitHubClientSecret = "github-client-secret"
		cfg.TurnstileSecretKey = "turnstile-test-secret"
	})
	h.app.verifyTurnstile = func(_ context.Context, token, _ string) error {
		if token == "good-token" {
			return nil
		}
		return errors.New("invalid_turnstile")
	}

	h.requestJSONWithHeaders(t, http.MethodPost, "/accounts/oauth/github/start", "", map[string]string{
		"Origin": h.app.cfg.FrontendOrigin,
	}, map[string]any{}, http.StatusBadRequest, nil)

	var start struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	h.requestJSONWithHeaders(t, http.MethodPost, "/accounts/oauth/github/start", "", map[string]string{
		"Origin": h.app.cfg.FrontendOrigin,
	}, map[string]any{"turnstile_token": "good-token"}, http.StatusOK, &start)
	if !strings.Contains(start.AuthorizeURL, "github.com/login/oauth/authorize") {
		t.Fatalf("authorize_url = %q, want github authorize url", start.AuthorizeURL)
	}
	parsed, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize_url: %v", err)
	}
	if parsed.Query().Get("code_challenge") == "" {
		t.Fatalf("authorize_url missing code_challenge: %q", start.AuthorizeURL)
	}
	if parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want %q", parsed.Query().Get("code_challenge_method"), "S256")
	}
}

func TestGitHubOAuthStartRateLimited(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PublicAPIBase = "http://localhost:8080"
		cfg.GitHubClientID = "github-client-id"
		cfg.GitHubClientSecret = "github-client-secret"
	})

	for i := 0; i < githubOAuthStartIPLimit; i++ {
		h.requestJSONWithHeaders(t, http.MethodPost, "/accounts/oauth/github/start", "", map[string]string{
			"Origin": h.app.cfg.FrontendOrigin,
		}, map[string]any{}, http.StatusOK, nil)
	}

	h.requestJSONWithHeaders(t, http.MethodPost, "/accounts/oauth/github/start", "", map[string]string{
		"Origin": h.app.cfg.FrontendOrigin,
	}, map[string]any{}, http.StatusTooManyRequests, nil)
}

func TestGitHubOAuthFlowCreatesAccountAndUpsertsByGitHubUserID(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PublicAPIBase = "http://localhost:8080"
		cfg.GitHubClientID = "github-client-id"
		cfg.GitHubClientSecret = "github-client-secret"
		cfg.TurnstileSecretKey = "turnstile-test-secret"
	})
	h.app.verifyTurnstile = func(_ context.Context, token, _ string) error {
		if token == "good-token" {
			return nil
		}
		return errors.New("invalid_turnstile")
	}

	accountID, sessionToken := h.completeGitHubOAuthLogin(t, 1001, "tom", "good-token")
	if accountID == "" || sessionToken == "" {
		t.Fatalf("unexpected oauth login result: account=%q session=%q", accountID, sessionToken)
	}

	var me struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		GitHubLogin string `json:"github_login"`
	}
	h.requestJSON(t, http.MethodGet, "/account/me", sessionToken, nil, http.StatusOK, &me)
	if me.ID != accountID {
		t.Fatalf("me.id = %q, want %q", me.ID, accountID)
	}
	if me.Name != "tom" {
		t.Fatalf("me.name = %q, want %q", me.Name, "tom")
	}
	if me.GitHubLogin != "tom" {
		t.Fatalf("me.github_login = %q, want %q", me.GitHubLogin, "tom")
	}

	secondAccountID, secondSessionToken := h.completeGitHubOAuthLogin(t, 1001, "tom-renamed", "good-token")
	if secondAccountID != accountID {
		t.Fatalf("second oauth login account_id = %q, want %q", secondAccountID, accountID)
	}
	if secondSessionToken == "" {
		t.Fatal("second session token was empty")
	}

	h.requestJSON(t, http.MethodGet, "/account/me", secondSessionToken, nil, http.StatusOK, &me)
	if me.Name != "tom-renamed" {
		t.Fatalf("updated me.name = %q, want %q", me.Name, "tom-renamed")
	}
	if me.GitHubLogin != "tom-renamed" {
		t.Fatalf("updated me.github_login = %q, want %q", me.GitHubLogin, "tom-renamed")
	}

	activeKeyCount := h.scalarInt(t, `SELECT COUNT(*)::int FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL`, accountID)
	if activeKeyCount != 1 {
		t.Fatalf("active api key count = %d, want 1", activeKeyCount)
	}
}

func TestAccountLogoutRevokesOAuthSessionButLeavesAPIKeyUsable(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PublicAPIBase = "http://localhost:8080"
		cfg.GitHubClientID = "github-client-id"
		cfg.GitHubClientSecret = "github-client-secret"
	})

	accountID, sessionToken := h.completeGitHubOAuthLogin(t, 1002, "tom", "")

	var keys struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/account/api-keys", sessionToken, nil, http.StatusOK, &keys)
	if len(keys.Items) != 1 {
		t.Fatalf("api key count = %d, want 1", len(keys.Items))
	}

	h.requestJSON(t, http.MethodPost, "/account/logout", sessionToken, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodGet, "/account/me", sessionToken, nil, http.StatusUnauthorized, nil)

	var me struct {
		ID string `json:"id"`
	}
	h.requestJSON(t, http.MethodGet, "/account/me", keys.Items[0].ID, nil, http.StatusOK, &me)
	if me.ID != accountID {
		t.Fatalf("api key auth me.id = %q, want %q", me.ID, accountID)
	}
}

func TestAPIKeyCreationRateLimited(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	account := h.registerAccount(t, "tom")

	for i := 0; i < apiKeyCreateLimit; i++ {
		h.requestJSON(t, http.MethodPost, "/account/api-keys", account.apiKey, map[string]any{
			"label": fmt.Sprintf("key-%d", i),
		}, http.StatusCreated, nil)
	}

	h.requestJSON(t, http.MethodPost, "/account/api-keys", account.apiKey, map[string]any{
		"label": "too-many",
	}, http.StatusTooManyRequests, nil)
}

func TestAccountAPIKeyLimitIsCappedAtFiveActiveKeys(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	account := h.registerAccount(t, "tom")

	for i := 0; i < 4; i++ {
		h.requestJSON(t, http.MethodPost, "/account/api-keys", account.apiKey, map[string]any{
			"label": fmt.Sprintf("key-%d", i+1),
		}, http.StatusCreated, nil)
	}

	activeKeyCount := h.scalarInt(t, `SELECT COUNT(*)::int FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL`, account.accountID)
	if activeKeyCount != 5 {
		t.Fatalf("active api key count = %d, want 5", activeKeyCount)
	}

	h.requestJSON(t, http.MethodPost, "/account/api-keys", account.apiKey, map[string]any{
		"label": "overflow",
	}, http.StatusConflict, nil)

	var keyToDelete struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/account/api-keys", account.apiKey, nil, http.StatusOK, &keyToDelete)
	if len(keyToDelete.Items) != 5 {
		t.Fatalf("api key list count = %d, want 5", len(keyToDelete.Items))
	}
	h.requestJSON(t, http.MethodDelete, "/account/api-keys/"+keyToDelete.Items[0].ID, account.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/account/api-keys", account.apiKey, map[string]any{
		"label": "replacement",
	}, http.StatusCreated, nil)
}

func TestAccountHookRegisterVerifyToggleAndAssignmentVisibility(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	prompter := h.registerAccount(t, "tom")
	dispatcher := h.registerAccount(t, "dora")
	responder := h.registerAccount(t, "noah")

	var delivered agentHookDelivery
	h.app.deliverAgentHook = func(_ context.Context, delivery agentHookDelivery) error {
		delivered = delivery
		return nil
	}

	var hookResponse struct {
		Hook struct {
			URL                      string `json:"url"`
			Enabled                  bool   `json:"enabled"`
			NotifyAssignmentReceived bool   `json:"notify_assignment_received"`
			NotifyReplyReceived      bool   `json:"notify_reply_received"`
			Status                   string `json:"status"`
			Failure                  string `json:"failure_reason"`
		} `json:"hook"`
	}
	h.requestJSON(t, http.MethodPut, "/account/hook", responder.apiKey, map[string]any{
		"url":                        "http://localhost:18789/hooks/agent",
		"auth_token":                 "hook-secret",
		"notify_assignment_received": true,
		"notify_reply_received":      false,
	}, http.StatusOK, &hookResponse)
	if hookResponse.Hook.Status != accountHookStatusPending {
		t.Fatalf("hook status = %q, want %q", hookResponse.Hook.Status, accountHookStatusPending)
	}
	if !hookResponse.Hook.Enabled {
		t.Fatal("hook should be enabled after registration")
	}
	if !hookResponse.Hook.NotifyAssignmentReceived || hookResponse.Hook.NotifyReplyReceived {
		t.Fatalf("hook notification flags = %+v, want assignment=true reply=false", hookResponse.Hook)
	}
	if delivered.URL != "http://localhost:18789/hooks/agent" {
		t.Fatalf("delivery url = %q, want %q", delivered.URL, "http://localhost:18789/hooks/agent")
	}
	if delivered.AuthToken != "hook-secret" {
		t.Fatalf("delivery auth token = %q, want %q", delivered.AuthToken, "hook-secret")
	}
	if !strings.Contains(delivered.Message, "/agent-hooks/verify/") {
		t.Fatalf("delivery message = %q, want verification callback url", delivered.Message)
	}
	if !strings.Contains(delivered.Message, "local Clawgrid skill, script, or hook tool") {
		t.Fatalf("delivery message = %q, want local hook tooling instruction", delivered.Message)
	}
	if !strings.Contains(delivered.Message, "https://clawgrid.hyi96.dev/skill.md") {
		t.Fatalf("delivery message = %q, want hosted skill doc reference", delivered.Message)
	}

	var available struct {
		Items []struct {
			OwnerID string `json:"owner_id"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/available", "", nil, http.StatusOK, &available)
	if len(available.Items) != 0 {
		t.Fatalf("available responders before verify = %+v, want empty", available.Items)
	}

	verifyToken := h.scalarString(t, `SELECT verification_token FROM account_hooks WHERE account_id = $1`, responder.accountID)
	h.requestJSON(t, http.MethodPost, "/agent-hooks/verify/"+verifyToken, "", nil, http.StatusOK, nil)

	h.requestJSON(t, http.MethodGet, "/responders/available", "", nil, http.StatusOK, &available)
	if len(available.Items) != 1 || available.Items[0].OwnerID != responder.accountID {
		t.Fatalf("available responders = %+v, want responder present", available.Items)
	}

	h.requestJSON(t, http.MethodPost, "/account/hook/disable", responder.apiKey, nil, http.StatusOK, &hookResponse)
	if hookResponse.Hook.Enabled {
		t.Fatal("hook should be disabled")
	}

	h.requestJSON(t, http.MethodGet, "/responders/available", "", nil, http.StatusOK, &available)
	if len(available.Items) != 0 {
		t.Fatalf("available responders after disable = %+v, want empty", available.Items)
	}

	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign me if you can")
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusConflict, nil)

	h.requestJSON(t, http.MethodPost, "/account/hook/enable", responder.apiKey, nil, http.StatusOK, &hookResponse)
	if !hookResponse.Hook.Enabled {
		t.Fatal("hook should be enabled again")
	}

	h.requestJSON(t, http.MethodGet, "/responders/available", "", nil, http.StatusOK, &available)
	if len(available.Items) != 1 || available.Items[0].OwnerID != responder.accountID {
		t.Fatalf("available responders after enable = %+v, want responder present", available.Items)
	}

	h.requestJSON(t, http.MethodPut, "/account/hook", responder.apiKey, map[string]any{
		"url":                        "http://localhost:18789/hooks/agent",
		"auth_token":                 "",
		"notify_assignment_received": false,
		"notify_reply_received":      true,
	}, http.StatusOK, &hookResponse)
	if hookResponse.Hook.NotifyAssignmentReceived || !hookResponse.Hook.NotifyReplyReceived {
		t.Fatalf("hook notification flags after update = %+v, want assignment=false reply=true", hookResponse.Hook)
	}

	h.requestJSON(t, http.MethodGet, "/responders/available", "", nil, http.StatusOK, &available)
	if len(available.Items) != 0 {
		t.Fatalf("available responders while reverify pending = %+v, want empty", available.Items)
	}

	verifyToken = h.scalarString(t, `SELECT verification_token FROM account_hooks WHERE account_id = $1`, responder.accountID)
	h.requestJSON(t, http.MethodPost, "/agent-hooks/verify/"+verifyToken, "", nil, http.StatusOK, nil)

	h.requestJSON(t, http.MethodGet, "/responders/available", "", nil, http.StatusOK, &available)
	if len(available.Items) != 0 {
		t.Fatalf("available responders with assignment notifications off = %+v, want empty", available.Items)
	}

	sessionID = h.createSession(t, prompter.apiKey)
	jobID = h.postMessage(t, prompter.apiKey, sessionID, "assign me if you can")
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusConflict, nil)
}

func TestAccountHookUpdateKeepsExistingBearerToken(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	account := h.registerAccount(t, "noah")

	deliveries := []agentHookDelivery{}
	h.app.deliverAgentHook = func(_ context.Context, delivery agentHookDelivery) error {
		deliveries = append(deliveries, delivery)
		return nil
	}

	h.requestJSON(t, http.MethodPut, "/account/hook", account.apiKey, map[string]any{
		"url":                        "http://localhost:18789/hooks/agent",
		"auth_token":                 "hook-secret",
		"notify_assignment_received": false,
		"notify_reply_received":      true,
	}, http.StatusOK, nil)

	h.requestJSON(t, http.MethodPut, "/account/hook", account.apiKey, map[string]any{
		"url":        "http://localhost:18789/hooks/agent",
		"auth_token": "",
	}, http.StatusOK, nil)

	if len(deliveries) != 2 {
		t.Fatalf("delivery count = %d, want 2", len(deliveries))
	}
	if deliveries[1].AuthToken != "hook-secret" {
		t.Fatalf("second delivery auth token = %q, want %q", deliveries[1].AuthToken, "hook-secret")
	}
	var notifyAssignment, notifyReply bool
	if err := h.appPool.QueryRow(context.Background(), `SELECT notify_assignment_received, notify_reply_received FROM account_hooks WHERE account_id = $1`, account.accountID).Scan(&notifyAssignment, &notifyReply); err != nil {
		t.Fatalf("load hook notification flags failed: %v", err)
	}
	if notifyAssignment || !notifyReply {
		t.Fatalf("stored hook flags = assignment:%v reply:%v, want assignment:false reply:true", notifyAssignment, notifyReply)
	}
}

func TestAccountHookDeliversAssignmentAndReplyNotifications(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	prompter := h.registerAccount(t, "tom")
	dispatcher := h.registerAccount(t, "dora")
	responder := h.registerAccount(t, "noah")

	deliveries := []agentHookDelivery{}
	h.app.deliverAgentHook = func(_ context.Context, delivery agentHookDelivery) error {
		deliveries = append(deliveries, delivery)
		return nil
	}

	h.requestJSON(t, http.MethodPut, "/account/hook", prompter.apiKey, map[string]any{
		"url":                        "http://localhost:18789/hooks/prompter",
		"auth_token":                 "prompter-secret",
		"notify_assignment_received": false,
		"notify_reply_received":      true,
	}, http.StatusOK, nil)
	prompterVerifyToken := h.scalarString(t, `SELECT verification_token FROM account_hooks WHERE account_id = $1`, prompter.accountID)
	h.requestJSON(t, http.MethodPost, "/agent-hooks/verify/"+prompterVerifyToken, "", nil, http.StatusOK, nil)

	h.requestJSON(t, http.MethodPut, "/account/hook", responder.apiKey, map[string]any{
		"url":                        "http://localhost:18789/hooks/responder",
		"auth_token":                 "responder-secret",
		"notify_assignment_received": true,
		"notify_reply_received":      false,
	}, http.StatusOK, nil)
	responderVerifyToken := h.scalarString(t, `SELECT verification_token FROM account_hooks WHERE account_id = $1`, responder.accountID)
	h.requestJSON(t, http.MethodPost, "/agent-hooks/verify/"+responderVerifyToken, "", nil, http.StatusOK, nil)

	deliveries = nil

	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "please take this assignment")

	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, nil)

	if len(deliveries) != 1 {
		t.Fatalf("assignment delivery count = %d, want 1", len(deliveries))
	}
	if deliveries[0].URL != "http://localhost:18789/hooks/responder" {
		t.Fatalf("assignment delivery url = %q, want responder hook", deliveries[0].URL)
	}
	if !strings.Contains(deliveries[0].Message, "/jobs/"+jobID) || !strings.Contains(deliveries[0].Message, "/sessions/"+sessionID+"/messages") {
		t.Fatalf("assignment delivery message = %q, want job and session urls", deliveries[0].Message)
	}
	if !strings.Contains(deliveries[0].Message, "Authorization: Bearer <api key>") {
		t.Fatalf("assignment delivery message = %q, want auth instruction", deliveries[0].Message)
	}
	if !strings.Contains(deliveries[0].Message, "Prefer that local Clawgrid tool over ad-hoc curl or hand-built JSON.") {
		t.Fatalf("assignment delivery message = %q, want local Clawgrid tool preference", deliveries[0].Message)
	}
	if !strings.Contains(deliveries[0].Message, "https://clawgrid.hyi96.dev/skill.md") {
		t.Fatalf("assignment delivery message = %q, want hosted skill doc reference", deliveries[0].Message)
	}

	deliveries = nil

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{
		"content": "done",
	}, http.StatusOK, nil)

	if len(deliveries) != 1 {
		t.Fatalf("reply delivery count = %d, want 1", len(deliveries))
	}
	if deliveries[0].URL != "http://localhost:18789/hooks/prompter" {
		t.Fatalf("reply delivery url = %q, want prompter hook", deliveries[0].URL)
	}
	if !strings.Contains(deliveries[0].Message, "new responder message") || !strings.Contains(deliveries[0].Message, "/sessions/"+sessionID+"/messages") {
		t.Fatalf("reply delivery message = %q, want responder session notification", deliveries[0].Message)
	}
	if !strings.Contains(deliveries[0].Message, "Authorization: Bearer <api key>") {
		t.Fatalf("reply delivery message = %q, want auth instruction", deliveries[0].Message)
	}
	if !strings.Contains(deliveries[0].Message, "Prefer that local Clawgrid tool over ad-hoc curl or hand-built JSON.") {
		t.Fatalf("reply delivery message = %q, want local Clawgrid tool preference", deliveries[0].Message)
	}
	if !strings.Contains(deliveries[0].Message, "https://clawgrid.hyi96.dev/skill.md") {
		t.Fatalf("reply delivery message = %q, want hosted skill doc reference", deliveries[0].Message)
	}
}

func TestAccountHookAutoDisablesAfterFiveConsecutiveFailures(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	account := h.registerAccount(t, "noah")

	h.app.deliverAgentHook = func(_ context.Context, _ agentHookDelivery) error {
		return nil
	}

	h.requestJSON(t, http.MethodPut, "/account/hook", account.apiKey, map[string]any{
		"url":                        "http://localhost:18789/hooks/agent",
		"auth_token":                 "hook-secret",
		"notify_assignment_received": true,
		"notify_reply_received":      false,
	}, http.StatusOK, nil)

	verifyToken := h.scalarString(t, `SELECT verification_token FROM account_hooks WHERE account_id = $1`, account.accountID)
	h.requestJSON(t, http.MethodPost, "/agent-hooks/verify/"+verifyToken, "", nil, http.StatusOK, nil)

	attempts := 0
	h.app.deliverAgentHook = func(_ context.Context, _ agentHookDelivery) error {
		attempts++
		return errors.New("hook_delivery_failed")
	}

	for i := 0; i < accountHookAutoDisableFailureLimit+1; i++ {
		h.app.notifyAssignmentReceived(context.Background(), account.accountID, "job_test", "ses_test")
	}

	if attempts != accountHookAutoDisableFailureLimit {
		t.Fatalf("delivery attempts = %d, want %d before auto-disable", attempts, accountHookAutoDisableFailureLimit)
	}

	var enabled bool
	var consecutiveFailures int
	if err := h.appPool.QueryRow(context.Background(), `SELECT enabled, consecutive_failures FROM account_hooks WHERE account_id = $1`, account.accountID).Scan(&enabled, &consecutiveFailures); err != nil {
		t.Fatalf("load hook state failed: %v", err)
	}
	if enabled {
		t.Fatal("hook should be auto-disabled after repeated failures")
	}
	if consecutiveFailures != 0 {
		t.Fatalf("consecutive_failures = %d, want 0 after auto-disable", consecutiveFailures)
	}
}

func TestLeaderboardsReturnRealSnapshotData(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "prompter")
	responder := h.registerAccount(t, "alice")
	dispatcher := h.registerAccount(t, "dora")
	rich := h.registerAccount(t, "richie")

	h.execSQL(t, `UPDATE wallets SET balance = 200 WHERE owner_type = 'account' AND owner_id = $1`, responder.accountID)
	h.execSQL(t, `UPDATE wallets SET balance = 250 WHERE owner_type = 'account' AND owner_id = $1`, dispatcher.accountID)
	h.execSQL(t, `UPDATE wallets SET balance = 999 WHERE owner_type = 'account' AND owner_id = $1`, rich.accountID)

	for i := 0; i < 50; i++ {
		vote := "up"
		if i >= 40 {
			vote = "down"
		}
		h.seedRatedCompletedJob(t, prompter.accountID, responder.accountID, dispatcher.accountID, vote)
	}

	var successBoard struct {
		Items []struct {
			AccountName   string `json:"account_name"`
			MetricDisplay string `json:"metric_display"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/leaderboards?category="+app.LeaderboardCategoryJobSuccess, "", nil, http.StatusOK, &successBoard)
	if len(successBoard.Items) == 0 {
		t.Fatal("job success leaderboard was empty")
	}
	if successBoard.Items[0].AccountName != "alice" {
		t.Fatalf("job success leaderboard first account = %q, want %q", successBoard.Items[0].AccountName, "alice")
	}
	if successBoard.Items[0].MetricDisplay != "80.0%" {
		t.Fatalf("job success leaderboard metric = %q, want %q", successBoard.Items[0].MetricDisplay, "80.0%")
	}

	var dispatchBoard struct {
		Items []struct {
			AccountName   string `json:"account_name"`
			MetricDisplay string `json:"metric_display"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/leaderboards?category="+app.LeaderboardCategoryDispatchAccuracy, "", nil, http.StatusOK, &dispatchBoard)
	if len(dispatchBoard.Items) == 0 {
		t.Fatal("dispatch leaderboard was empty")
	}
	if dispatchBoard.Items[0].AccountName != "dora" {
		t.Fatalf("dispatch leaderboard first account = %q, want %q", dispatchBoard.Items[0].AccountName, "dora")
	}
	if dispatchBoard.Items[0].MetricDisplay != "80.0%" {
		t.Fatalf("dispatch leaderboard metric = %q, want %q", dispatchBoard.Items[0].MetricDisplay, "80.0%")
	}

	var creditsBoard struct {
		Items []struct {
			AccountName   string `json:"account_name"`
			MetricDisplay string `json:"metric_display"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/leaderboards?category="+app.LeaderboardCategoryTotalAvailableFunds, "", nil, http.StatusOK, &creditsBoard)
	if len(creditsBoard.Items) == 0 {
		t.Fatal("credits leaderboard was empty")
	}
	if creditsBoard.Items[0].AccountName != "dora" {
		t.Fatalf("credits leaderboard first account = %q, want %q", creditsBoard.Items[0].AccountName, "dora")
	}
	if creditsBoard.Items[0].MetricDisplay != "250.00 credits" {
		t.Fatalf("credits leaderboard metric = %q, want %q", creditsBoard.Items[0].MetricDisplay, "250.00 credits")
	}
	for _, item := range creditsBoard.Items {
		if item.AccountName == "richie" {
			t.Fatal("credits leaderboard included unqualified rich account")
		}
	}
}

func TestRoutingAndPoolExposeTimeLimitAndBonusTip(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 0
	})

	prompter := h.registerAccount(t, "tom")
	dispatcher := h.registerAccount(t, "dora")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)

	var messageOut struct {
		JobID string `json:"job_id"`
	}
	h.requestJSON(t, http.MethodPost, "/sessions/"+sessionID+"/messages", prompter.apiKey, map[string]any{
		"content":            "with bonus tip",
		"time_limit_minutes": 10,
		"tip_amount":         1.5,
	}, http.StatusCreated, &messageOut)

	var routing struct {
		Items []struct {
			ID               string  `json:"id"`
			TimeLimitMinutes int     `json:"time_limit_minutes"`
			TipAmount        float64 `json:"tip_amount"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/routing/jobs", dispatcher.apiKey, nil, http.StatusOK, &routing)
	if len(routing.Items) != 1 {
		t.Fatalf("routing item count = %d, want 1", len(routing.Items))
	}
	if routing.Items[0].ID != messageOut.JobID {
		t.Fatalf("routing job id = %q, want %q", routing.Items[0].ID, messageOut.JobID)
	}
	if routing.Items[0].TimeLimitMinutes != 10 {
		t.Fatalf("routing time_limit_minutes = %d, want 10", routing.Items[0].TimeLimitMinutes)
	}
	if routing.Items[0].TipAmount != 1.5 {
		t.Fatalf("routing tip_amount = %v, want 1.5", routing.Items[0].TipAmount)
	}

	h.execSQL(t, `UPDATE jobs SET status = 'system_pool', last_system_pool_entered_at = now() WHERE id = $1`, messageOut.JobID)
	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID               string  `json:"id"`
			SessionTitle     string  `json:"session_title"`
			SessionSnippet   string  `json:"session_snippet"`
			TimeLimitMinutes int     `json:"time_limit_minutes"`
			TipAmount        float64 `json:"tip_amount"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" || len(work.Candidates) != 1 {
		t.Fatalf("unexpected pool payload: %+v", work)
	}
	if work.Candidates[0].ID != messageOut.JobID {
		t.Fatalf("pool candidate id = %q, want %q", work.Candidates[0].ID, messageOut.JobID)
	}
	if work.Candidates[0].TimeLimitMinutes != 10 {
		t.Fatalf("pool time_limit_minutes = %d, want 10", work.Candidates[0].TimeLimitMinutes)
	}
	if work.Candidates[0].TipAmount != 1.5 {
		t.Fatalf("pool tip_amount = %v, want 1.5", work.Candidates[0].TipAmount)
	}
	if work.Candidates[0].SessionTitle != "incident thread" {
		t.Fatalf("pool session_title = %q, want %q", work.Candidates[0].SessionTitle, "incident thread")
	}
	if work.Candidates[0].SessionSnippet == "" {
		t.Fatal("pool session_snippet was empty")
	}
}

func TestPublicDispatchViewsWorkWithoutAuth(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "public routing preview")

	h.execSQL(t, `
INSERT INTO responder_availability(id, owner_type, owner_id, last_seen_at, poll_started_at)
VALUES ($1, 'account', $2, now(), now())`,
		domain.NewID("av"), responder.accountID)

	var routing struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/routing/jobs", "", nil, http.StatusOK, &routing)
	if len(routing.Items) != 1 || routing.Items[0].ID != jobID {
		t.Fatalf("unexpected public routing payload: %+v", routing)
	}

	var available struct {
		Items []struct {
			OwnerID string `json:"owner_id"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/available", "", nil, http.StatusOK, &available)
	if len(available.Items) != 1 || available.Items[0].OwnerID != responder.accountID {
		t.Fatalf("unexpected public responders payload: %+v", available)
	}
}

func TestSessionDispatchSnippetStoredOnMessageAndReply(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 30 * time.Second
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "first prompt")

	var snippetAfterPrompt string
	if err := h.appPool.QueryRow(context.Background(), `SELECT dispatch_snippet FROM sessions WHERE id = $1`, sessionID).Scan(&snippetAfterPrompt); err != nil {
		t.Fatalf("load prompt snippet: %v", err)
	}
	if !strings.Contains(snippetAfterPrompt, "prompter: first prompt") {
		t.Fatalf("dispatch_snippet after prompt = %q", snippetAfterPrompt)
	}

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/assignments", prompter.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{
		"content": "first reply",
	}, http.StatusOK, nil)

	var snippetAfterReply string
	if err := h.appPool.QueryRow(context.Background(), `SELECT dispatch_snippet FROM sessions WHERE id = $1`, sessionID).Scan(&snippetAfterReply); err != nil {
		t.Fatalf("load reply snippet: %v", err)
	}
	if !strings.Contains(snippetAfterReply, "prompter: first prompt") || !strings.Contains(snippetAfterReply, "responder: first reply") {
		t.Fatalf("dispatch_snippet after reply = %q", snippetAfterReply)
	}
}

func TestRoutingJobsLazyBackfillsStoredSessionSnippet(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "lazy backfill prompt")

	h.execSQL(t, `UPDATE sessions SET dispatch_snippet = '' WHERE id = $1`, sessionID)

	var routing struct {
		Items []struct {
			ID             string `json:"id"`
			SessionSnippet string `json:"session_snippet"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/routing/jobs", "", nil, http.StatusOK, &routing)
	if len(routing.Items) != 1 || routing.Items[0].ID != jobID {
		t.Fatalf("unexpected routing payload: %+v", routing)
	}
	if !strings.Contains(routing.Items[0].SessionSnippet, "prompter: lazy backfill prompt") {
		t.Fatalf("routing session_snippet = %q", routing.Items[0].SessionSnippet)
	}

	var stored string
	if err := h.appPool.QueryRow(context.Background(), `SELECT dispatch_snippet FROM sessions WHERE id = $1`, sessionID).Scan(&stored); err != nil {
		t.Fatalf("load stored snippet: %v", err)
	}
	if stored != routing.Items[0].SessionSnippet {
		t.Fatalf("stored dispatch_snippet = %q, want %q", stored, routing.Items[0].SessionSnippet)
	}
}

func TestWalletLedgerPagination(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	account := h.registerAccount(t, "tom")
	ledger1 := domain.NewID("led")
	ledger2 := domain.NewID("led")
	ledger3 := domain.NewID("led")
	ledger4 := domain.NewID("led")

	h.execSQL(t, `DELETE FROM wallet_ledger WHERE owner_type = 'account' AND owner_id = $1`, account.accountID)
	h.execSQL(t, `
INSERT INTO wallet_ledger(id, owner_type, owner_id, delta, reason, created_at)
VALUES
  ($1, 'account', $5, 1.0, 'r1', now() - interval '4 seconds'),
  ($2, 'account', $5, 2.0, 'r2', now() - interval '3 seconds'),
  ($3, 'account', $5, 3.0, 'r3', now() - interval '2 seconds'),
  ($4, 'account', $5, 4.0, 'r4', now() - interval '1 second')`,
		ledger1, ledger2, ledger3, ledger4, account.accountID)

	var latest struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		HasMoreOlder bool   `json:"has_more_older"`
		NextBeforeID string `json:"next_before_id"`
	}
	h.requestJSON(t, http.MethodGet, "/wallets/current/ledger?limit=2", account.apiKey, nil, http.StatusOK, &latest)
	if len(latest.Items) != 2 {
		t.Fatalf("latest item count = %d, want 2", len(latest.Items))
	}
	if latest.Items[0].ID != ledger4 || latest.Items[1].ID != ledger3 {
		t.Fatalf("latest items = %+v, want [%s %s]", latest.Items, ledger4, ledger3)
	}
	if !latest.HasMoreOlder {
		t.Fatal("has_more_older = false, want true")
	}
	if latest.NextBeforeID != ledger3 {
		t.Fatalf("next_before_id = %q, want %q", latest.NextBeforeID, ledger3)
	}

	var older struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		HasMoreOlder bool   `json:"has_more_older"`
		NextBeforeID string `json:"next_before_id"`
	}
	h.requestJSON(t, http.MethodGet, "/wallets/current/ledger?limit=2&before_id="+ledger3, account.apiKey, nil, http.StatusOK, &older)
	if len(older.Items) != 2 {
		t.Fatalf("older item count = %d, want 2", len(older.Items))
	}
	if older.Items[0].ID != ledger2 || older.Items[1].ID != ledger1 {
		t.Fatalf("older items = %+v, want [%s %s]", older.Items, ledger2, ledger1)
	}
	if older.HasMoreOlder {
		t.Fatal("older has_more_older = true, want false")
	}
	if older.NextBeforeID != "" {
		t.Fatalf("older next_before_id = %q, want empty", older.NextBeforeID)
	}

	h.requestJSON(t, http.MethodGet, "/wallets/current/ledger?limit=0", account.apiKey, nil, http.StatusBadRequest, nil)
	h.requestJSON(t, http.MethodGet, "/wallets/current/ledger?before_id=led_missing", account.apiKey, nil, http.StatusBadRequest, nil)
}

func TestPoolClaimReplyVoteFlowUpdatesStats(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "tell me something useful")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" || len(work.Candidates) != 1 {
		t.Fatalf("unexpected work payload: mode=%q candidates=%d", work.Mode, len(work.Candidates))
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{"content": "here is a reply"}, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/vote", prompter.apiKey, map[string]any{"vote": "up"}, http.StatusOK, nil)

	if got := h.walletBalance(t, responder.apiKey); got != 101.4 {
		t.Fatalf("responder balance = %v, want %v", got, 101.4)
	}

	var prompterStats struct {
		FeedbackRate string `json:"feedback_rate"`
	}
	h.requestJSON(t, http.MethodGet, "/account/stats", prompter.apiKey, nil, http.StatusOK, &prompterStats)
	if prompterStats.FeedbackRate != "1 / 1" {
		t.Fatalf("prompter feedback_rate = %q, want %q", prompterStats.FeedbackRate, "1 / 1")
	}

	var responderStats struct {
		JobSuccessRate string `json:"job_success_rate"`
		FeedbackRate   string `json:"feedback_rate"`
	}
	h.requestJSON(t, http.MethodGet, "/account/stats", responder.apiKey, nil, http.StatusOK, &responderStats)
	if responderStats.JobSuccessRate != "100.0%" {
		t.Fatalf("responder job_success_rate = %q, want %q", responderStats.JobSuccessRate, "100.0%")
	}
	if responderStats.FeedbackRate != "n/a" {
		t.Fatalf("responder feedback_rate = %q, want %q", responderStats.FeedbackRate, "n/a")
	}
}

func TestPoolClaimReplyDownvoteSlashesStake(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	var created struct {
		JobID string `json:"job_id"`
	}
	h.requestJSON(t, http.MethodPost, "/sessions/"+sessionID+"/messages", prompter.apiKey, map[string]any{
		"content":            "say something bad",
		"time_limit_minutes": 5,
		"tip_amount":         2.0,
	}, http.StatusCreated, &created)
	jobID := created.JobID

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" || len(work.Candidates) != 1 || work.Candidates[0].ID != jobID {
		t.Fatalf("unexpected work payload: %+v", work)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{"content": "garbage"}, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/vote", prompter.apiKey, map[string]any{"vote": "down"}, http.StatusOK, nil)

	if got := h.walletBalance(t, responder.apiKey); got != 99.4 {
		t.Fatalf("responder balance = %v, want %v", got, 99.4)
	}
	if got := h.walletBalance(t, prompter.apiKey); got != 97.0 {
		t.Fatalf("prompter balance = %v, want %v", got, 97.0)
	}
	if got := h.scalarInt(t, `
SELECT COUNT(*)::int
FROM wallet_ledger
WHERE owner_type = 'account'
  AND owner_id = $1
  AND reason = 'tip_bad_feedback_refund'`, prompter.accountID); got != 1 {
		t.Fatalf("bad-feedback tip refund ledger count = %d, want 1", got)
	}
}

func TestAutoReviewPenalizesPrompterKeepsTipAndRewardsResponder(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	var created struct {
		JobID string `json:"job_id"`
	}
	h.requestJSON(t, http.MethodPost, "/sessions/"+sessionID+"/messages", prompter.apiKey, map[string]any{
		"content":            "ghost this one",
		"time_limit_minutes": 5,
		"tip_amount":         2.0,
	}, http.StatusCreated, &created)
	jobID := created.JobID

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" || len(work.Candidates) != 1 || work.Candidates[0].ID != jobID {
		t.Fatalf("unexpected work payload: %+v", work)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{"content": "reply with no rating"}, http.StatusOK, nil)
	h.execSQL(t, `UPDATE jobs SET review_deadline_at = now() - interval '1 second' WHERE id = $1`, jobID)

	var internal struct {
		Affected int64 `json:"affected"`
	}
	h.requestJSON(t, http.MethodPost, "/internal/jobs/auto-review", "", nil, http.StatusOK, &internal)
	if internal.Affected != 1 {
		t.Fatalf("affected = %d, want 1", internal.Affected)
	}

	var job struct {
		Status       string `json:"status"`
		PrompterVote string `json:"prompter_vote"`
	}
	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, prompter.apiKey, nil, http.StatusOK, &job)
	if job.Status != "auto_settled" {
		t.Fatalf("job status = %q, want %q", job.Status, "auto_settled")
	}
	if job.PrompterVote != "auto" {
		t.Fatalf("prompter_vote = %q, want %q", job.PrompterVote, "auto")
	}

	var messages struct {
		Items []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/sessions/"+sessionID+"/messages", prompter.apiKey, nil, http.StatusOK, &messages)
	foundNoFeedback := false
	for _, item := range messages.Items {
		if item.Type == "feedback" && item.Content == "no feedback" {
			foundNoFeedback = true
			break
		}
	}
	if !foundNoFeedback {
		t.Fatal("expected auto-review to append a no-feedback message")
	}

	if got := h.walletBalance(t, responder.apiKey); got != 100.4 {
		t.Fatalf("responder balance = %v, want %v", got, 100.4)
	}
	if got := h.walletBalance(t, prompter.apiKey); got != 95.4 {
		t.Fatalf("prompter balance = %v, want %v", got, 95.4)
	}
}

func TestResponderWorkReturnsAssignedForAlreadyClaimedPoolJob(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "one job at a time")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var initialWork struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &initialWork)
	if initialWork.Mode != "pool" || len(initialWork.Candidates) != 1 || initialWork.Candidates[0].ID != jobID {
		t.Fatalf("unexpected initial work payload: %+v", initialWork)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, nil)

	var resumedWork struct {
		Mode  string `json:"mode"`
		JobID string `json:"job_id"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &resumedWork)
	if resumedWork.Mode != "assigned" {
		t.Fatalf("mode = %q, want %q", resumedWork.Mode, "assigned")
	}
	if resumedWork.JobID != jobID {
		t.Fatalf("job_id = %q, want %q", resumedWork.JobID, jobID)
	}
}

func TestPrompterCancelClaimedJobAppliesPenalty(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "cancel this after claim")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" || len(work.Candidates) != 1 || work.Candidates[0].ID != jobID {
		t.Fatalf("unexpected work payload: %+v", work)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, nil)

	var cancelResult struct {
		OK            bool    `json:"ok"`
		Penalized     bool    `json:"penalized"`
		PenaltyAmount float64 `json:"penalty_amount"`
	}
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/cancel", prompter.apiKey, nil, http.StatusOK, &cancelResult)
	if !cancelResult.OK || !cancelResult.Penalized {
		t.Fatalf("unexpected cancel result: %+v", cancelResult)
	}
	if math.Abs(cancelResult.PenaltyAmount-0.2) > 1e-9 {
		t.Fatalf("penalty_amount = %v, want %v", cancelResult.PenaltyAmount, 0.2)
	}

	balance := h.walletBalance(t, prompter.apiKey)
	if math.Abs(balance-97.8) > 1e-9 {
		t.Fatalf("prompter balance = %v, want %v", balance, 97.8)
	}

	var job struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, prompter.apiKey, nil, http.StatusOK, &job)
	if job.Status != "cancelled" {
		t.Fatalf("job status = %q, want %q", job.Status, "cancelled")
	}
}

func TestResponderCancelsClaimedJobSlashesStakeAndKeepsJobCirculating(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 0
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "claim and then cancel")

	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" || len(work.Candidates) != 1 || work.Candidates[0].ID != jobID {
		t.Fatalf("unexpected work payload: %+v", work)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/responder-cancel", responder.apiKey, map[string]any{
		"reason": "not a good fit",
	}, http.StatusOK, nil)

	if got := h.walletBalance(t, responder.apiKey); math.Abs(got-99.4) > 1e-9 {
		t.Fatalf("responder balance = %v, want %v", got, 99.4)
	}

	var jobStatus, responderStakeStatus string
	var claimOwnerType, claimOwnerID *string
	if err := h.appPool.QueryRow(context.Background(), `SELECT status, responder_stake_status, claim_owner_type, claim_owner_id FROM jobs WHERE id = $1`, jobID).Scan(&jobStatus, &responderStakeStatus, &claimOwnerType, &claimOwnerID); err != nil {
		t.Fatalf("load job after claimed cancel: %v", err)
	}
	if jobStatus != "system_pool" {
		t.Fatalf("job status = %q, want %q", jobStatus, "system_pool")
	}
	if responderStakeStatus != "slashed" {
		t.Fatalf("responder stake status = %q, want %q", responderStakeStatus, "slashed")
	}
	if claimOwnerType != nil || claimOwnerID != nil {
		t.Fatalf("claim owner not cleared: type=%v id=%v", claimOwnerType, claimOwnerID)
	}

	if got := h.scalarString(t, `SELECT content FROM messages WHERE session_id = $1 AND type = 'feedback' AND role = 'responder' ORDER BY created_at DESC LIMIT 1`, sessionID); got != `a responder cancelled the claimed job due to "not a good fit"` {
		t.Fatalf("feedback message = %q", got)
	}
	otherResponder := h.registerAccount(t, "mia")
	var refreshedWork struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID                        string `json:"id"`
			SessionSnippet            string `json:"session_snippet"`
			LastResponderCancelReason string `json:"last_responder_cancel_reason"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", otherResponder.apiKey, nil, http.StatusOK, &refreshedWork)
	if refreshedWork.Mode != "pool" || len(refreshedWork.Candidates) != 1 {
		t.Fatalf("unexpected refreshed work payload: %+v", refreshedWork)
	}
	if got := refreshedWork.Candidates[0].LastResponderCancelReason; got != "not a good fit" {
		t.Fatalf("last_responder_cancel_reason = %q, want %q", got, "not a good fit")
	}
	if strings.Contains(refreshedWork.Candidates[0].SessionSnippet, "not a good fit") {
		t.Fatalf("session_snippet = %q, should not include cancel reason", refreshedWork.Candidates[0].SessionSnippet)
	}
}

func TestResponderCancelsAssignedJobRefundsResponderStakeAndPartiallyRefundsDispatcherStake(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PoolDwellWindow = 0
	})

	prompter := h.registerAccount(t, "tom")
	dispatcher := h.registerAccount(t, "dora")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign and then refuse")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var assignment struct {
		ID string `json:"id"`
	}
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, &assignment)

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/responder-cancel", responder.apiKey, map[string]any{
		"reason": "time limit too tight",
	}, http.StatusOK, nil)

	if got := h.walletBalance(t, responder.apiKey); math.Abs(got-100.0) > 1e-9 {
		t.Fatalf("responder balance = %v, want %v", got, 100.0)
	}
	if got := h.walletBalance(t, dispatcher.apiKey); math.Abs(got-99.9) > 1e-9 {
		t.Fatalf("dispatcher balance = %v, want %v", got, 99.9)
	}

	var jobStatus, responderStakeStatus, dispatcherStakeStatus, assignmentStatus string
	if err := h.appPool.QueryRow(context.Background(), `SELECT status, responder_stake_status, dispatcher_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&jobStatus, &responderStakeStatus, &dispatcherStakeStatus); err != nil {
		t.Fatalf("load job after assigned cancel: %v", err)
	}
	if jobStatus != "system_pool" {
		t.Fatalf("job status = %q, want %q", jobStatus, "system_pool")
	}
	if responderStakeStatus != "returned" {
		t.Fatalf("responder stake status = %q, want %q", responderStakeStatus, "returned")
	}
	if dispatcherStakeStatus != "partial_returned" {
		t.Fatalf("dispatcher stake status = %q, want %q", dispatcherStakeStatus, "partial_returned")
	}
	if err := h.appPool.QueryRow(context.Background(), `SELECT status FROM assignments WHERE id = $1`, assignment.ID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("load assignment status: %v", err)
	}
	if assignmentStatus != "refused" {
		t.Fatalf("assignment status = %q, want %q", assignmentStatus, "refused")
	}
	if got := h.scalarInt(t, `SELECT COUNT(*)::int FROM responder_availability WHERE owner_type = 'account' AND owner_id = $1`, responder.accountID); got != 0 {
		t.Fatalf("responder availability count = %d, want 0", got)
	}
	if got := h.scalarString(t, `SELECT content FROM messages WHERE session_id = $1 AND type = 'feedback' AND role = 'responder' ORDER BY created_at DESC LIMIT 1`, sessionID); got != `a responder cancelled the assigned job due to "time limit too tight"` {
		t.Fatalf("feedback message = %q", got)
	}
	if _, err := h.app.svc.ProcessPoolRotation(context.Background()); err != nil {
		t.Fatalf("process pool rotation: %v", err)
	}
	var routing struct {
		Items []struct {
			ID                        string `json:"id"`
			SessionSnippet            string `json:"session_snippet"`
			LastResponderCancelReason string `json:"last_responder_cancel_reason"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/routing/jobs", dispatcher.apiKey, nil, http.StatusOK, &routing)
	if len(routing.Items) != 1 || routing.Items[0].ID != jobID {
		t.Fatalf("unexpected routing payload: %+v", routing)
	}
	if got := routing.Items[0].LastResponderCancelReason; got != "time limit too tight" {
		t.Fatalf("last_responder_cancel_reason = %q, want %q", got, "time limit too tight")
	}
	if strings.Contains(routing.Items[0].SessionSnippet, "time limit too tight") {
		t.Fatalf("session_snippet = %q, should not include cancel reason", routing.Items[0].SessionSnippet)
	}
}

func TestResponderJobCancelRequiresReasonAndTruncatesLongReason(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)
	responder := h.registerAccount(t, "noah")

	h.requestJSON(t, http.MethodPost, "/jobs/job_missing/responder-cancel", responder.apiKey, map[string]any{
		"reason": "",
	}, http.StatusBadRequest, nil)

	h.requestJSON(t, http.MethodPost, "/jobs/job_missing/responder-cancel", responder.apiKey, map[string]any{
		"reason": strings.Repeat("x", responderCancelReasonLimit+1),
	}, http.StatusNotFound, nil)
}

func TestResponderJobCancelTruncatesLongReasonBeforePersisting(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 0
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "claim and cancel with long reason")

	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, nil)

	longReason := strings.Repeat("x", responderCancelReasonLimit+7)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/responder-cancel", responder.apiKey, map[string]any{
		"reason": longReason,
	}, http.StatusOK, nil)

	wantReason := strings.Repeat("x", responderCancelReasonLimit)
	if got := h.scalarString(t, `SELECT content FROM messages WHERE session_id = $1 AND type = 'feedback' AND role = 'responder' ORDER BY created_at DESC LIMIT 1`, sessionID); got != fmt.Sprintf(`a responder cancelled the claimed job due to %q`, wantReason) {
		t.Fatalf("feedback message = %q", got)
	}
}

func TestDirectAssignmentResponderWorkReturnsAssignedJob(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.RoutingWindow = 5 * time.Minute
		cfg.PollAssignmentWait = 30 * time.Second
	})

	prompter := h.registerAccount(t, "tom")
	dispatcher := h.registerAccount(t, "dora")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this directly")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)

	var assignment struct {
		ID string `json:"id"`
	}
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, &assignment)

	var work struct {
		Mode  string `json:"mode"`
		JobID string `json:"job_id"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "assigned" {
		t.Fatalf("mode = %q, want %q", work.Mode, "assigned")
	}
	if work.JobID != jobID {
		t.Fatalf("job_id = %q, want %q", work.JobID, jobID)
	}

	var assignmentState struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/assignments/"+assignment.ID, dispatcher.apiKey, nil, http.StatusOK, &assignmentState)
	if assignmentState.Status != "active" {
		t.Fatalf("assignment status = %q, want %q", assignmentState.Status, "active")
	}
}

func TestDirectAssignmentBadVoteConsumesHeldDispatcherStakeWithoutGoingNegative(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	prompter := h.registerAccount(t, "tom")
	dispatcher := h.registerAccount(t, "dora")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "assign this badly")

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	h.execSQL(t, `UPDATE wallets SET balance = 0.2 WHERE owner_type = 'account' AND owner_id = $1`, dispatcher.accountID)

	var assignment struct {
		ID string `json:"id"`
	}
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, &assignment)

	if got := h.walletBalance(t, dispatcher.apiKey); math.Abs(got-0.0) > 1e-9 {
		t.Fatalf("dispatcher balance after assignment = %v, want %v", got, 0.0)
	}

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/reply", responder.apiKey, map[string]any{
		"content": "bad reply",
	}, http.StatusOK, nil)
	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/vote", prompter.apiKey, map[string]any{
		"vote": "down",
	}, http.StatusOK, nil)

	if got := h.walletBalance(t, dispatcher.apiKey); math.Abs(got-0.0) > 1e-9 {
		t.Fatalf("dispatcher balance after bad vote = %v, want %v", got, 0.0)
	}

	var dispatcherStakeStatus string
	if err := h.appPool.QueryRow(context.Background(), `SELECT dispatcher_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&dispatcherStakeStatus); err != nil {
		t.Fatalf("load dispatcher_stake_status: %v", err)
	}
	if dispatcherStakeStatus != "slashed" {
		t.Fatalf("dispatcher_stake_status = %q, want %q", dispatcherStakeStatus, "slashed")
	}
}

func TestAssignmentTimeoutReturnsJobToSystemPool(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.RoutingWindow = 5 * time.Minute
		cfg.PollAssignmentWait = 0
	})

	prompter := h.registerAccount(t, "tom")
	dispatcher := h.registerAccount(t, "dora")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "time out this assignment")

	var assignment struct {
		ID string `json:"id"`
	}
	h.requestJSON(t, http.MethodPost, "/assignments", dispatcher.apiKey, map[string]any{
		"job_id":             jobID,
		"responder_id": responder.accountID,
	}, http.StatusCreated, &assignment)

	h.execSQL(t, `UPDATE assignments SET deadline_at = now() - interval '1 second' WHERE id = $1`, assignment.ID)

	var internal struct {
		Affected int64 `json:"affected"`
	}
	h.requestJSON(t, http.MethodPost, "/internal/assignments/process-timeouts", "", nil, http.StatusOK, &internal)
	if internal.Affected != 1 {
		t.Fatalf("affected = %d, want 1", internal.Affected)
	}

	var assignmentState struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/assignments/"+assignment.ID, dispatcher.apiKey, nil, http.StatusOK, &assignmentState)
	if assignmentState.Status != "timeout" {
		t.Fatalf("assignment status = %q, want %q", assignmentState.Status, "timeout")
	}

	var job struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, prompter.apiKey, nil, http.StatusOK, &job)
	if job.Status != "system_pool" {
		t.Fatalf("job status = %q, want %q", job.Status, "system_pool")
	}

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" || len(work.Candidates) != 1 || work.Candidates[0].ID != jobID {
		t.Fatalf("unexpected work payload after timeout: %+v", work)
	}

	var responderStats struct {
		JobSuccessRate    string `json:"job_success_rate"`
		JobsCompleted     int    `json:"jobs_completed"`
		TotalJobsReceived int    `json:"total_jobs_received"`
	}
	h.requestJSON(t, http.MethodGet, "/account/stats", responder.apiKey, nil, http.StatusOK, &responderStats)
	if responderStats.JobSuccessRate != "0.0%" {
		t.Fatalf("responder job_success_rate = %q, want %q", responderStats.JobSuccessRate, "0.0%")
	}
	if responderStats.JobsCompleted != 0 || responderStats.TotalJobsReceived != 1 {
		t.Fatalf("responder completed/received = %d/%d, want 0/1", responderStats.JobsCompleted, responderStats.TotalJobsReceived)
	}
}

func TestRoutingExpiryMovesJobIntoSystemPool(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.RoutingWindow = 5 * time.Minute
		cfg.PollAssignmentWait = 0
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "let routing expire")

	h.execSQL(t, `UPDATE jobs SET routing_ends_at = now() - interval '1 second' WHERE id = $1`, jobID)

	var internal struct {
		Affected int64 `json:"affected"`
	}
	h.requestJSON(t, http.MethodPost, "/internal/jobs/process-routing-expiry", "", nil, http.StatusOK, &internal)
	if internal.Affected != 1 {
		t.Fatalf("affected = %d, want 1", internal.Affected)
	}

	var job struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, prompter.apiKey, nil, http.StatusOK, &job)
	if job.Status != "system_pool" {
		t.Fatalf("job status = %q, want %q", job.Status, "system_pool")
	}

	h.requestJSON(t, http.MethodPost, "/responders/availability", responder.apiKey, nil, http.StatusOK, nil)
	var work struct {
		Mode       string `json:"mode"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "pool" || len(work.Candidates) != 1 || work.Candidates[0].ID != jobID {
		t.Fatalf("unexpected work payload after routing expiry: %+v", work)
	}
}

func TestPoolRotationMovesUnclaimedJobBackToRouting(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.RoutingWindow = 5 * time.Minute
		cfg.PoolDwellWindow = 30 * time.Second
		cfg.PollAssignmentWait = 0
	})

	prompter := h.registerAccount(t, "tom")
	dispatcher := h.registerAccount(t, "dora")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "rotate this pool job back to routing")

	h.execSQL(t, `UPDATE jobs SET status = 'system_pool', last_system_pool_entered_at = now() - interval '31 seconds' WHERE id = $1`, jobID)

	var internal struct {
		Affected int64 `json:"affected"`
	}
	h.requestJSON(t, http.MethodPost, "/internal/jobs/process-pool-rotation", "", nil, http.StatusOK, &internal)
	if internal.Affected != 1 {
		t.Fatalf("affected = %d, want 1", internal.Affected)
	}

	var job struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, prompter.apiKey, nil, http.StatusOK, &job)
	if job.Status != "routing" {
		t.Fatalf("job status = %q, want %q", job.Status, "routing")
	}

	var routing struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/routing/jobs", dispatcher.apiKey, nil, http.StatusOK, &routing)
	if len(routing.Items) != 1 || routing.Items[0].ID != jobID {
		t.Fatalf("unexpected routing jobs payload: %+v", routing)
	}
}

func TestRoutingJobsOrderedByCycleThenTipThenAge(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarness(t)

	dispatcher := h.registerAccount(t, "dispatch")
	prompter := h.registerAccount(t, "tom")

	sessionA := h.createSession(t, prompter.apiKey)
	jobA := h.postMessage(t, prompter.apiKey, sessionA, "job a")
	sessionB := h.createSession(t, prompter.apiKey)
	jobB := h.postMessage(t, prompter.apiKey, sessionB, "job b")
	sessionC := h.createSession(t, prompter.apiKey)
	jobC := h.postMessage(t, prompter.apiKey, sessionC, "job c")
	sessionD := h.createSession(t, prompter.apiKey)
	jobD := h.postMessage(t, prompter.apiKey, sessionD, "job d")

	h.execSQL(t, `UPDATE jobs SET routing_cycle_count = 2, tip_amount = 0, created_at = now() - interval '2 minutes' WHERE id = $1`, jobA)
	h.execSQL(t, `UPDATE jobs SET routing_cycle_count = 1, tip_amount = 10, created_at = now() - interval '4 minutes' WHERE id = $1`, jobB)
	h.execSQL(t, `UPDATE jobs SET routing_cycle_count = 2, tip_amount = 5, created_at = now() - interval '90 seconds' WHERE id = $1`, jobC)
	h.execSQL(t, `UPDATE jobs SET routing_cycle_count = 2, tip_amount = 5, created_at = now() - interval '3 minutes' WHERE id = $1`, jobD)

	var routing struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	h.requestJSON(t, http.MethodGet, "/routing/jobs", dispatcher.apiKey, nil, http.StatusOK, &routing)

	if len(routing.Items) < 4 {
		t.Fatalf("routing item count = %d, want at least 4", len(routing.Items))
	}
	got := []string{routing.Items[0].ID, routing.Items[1].ID, routing.Items[2].ID, routing.Items[3].ID}
	want := []string{jobD, jobC, jobA, jobB}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("routing order = %v, want %v", got, want)
		}
	}
}

func TestPoolRotationDoesNotMoveActivelyClaimedJob(t *testing.T) {
	t.Parallel()

	h := newIntegrationHarnessWithConfig(t, func(cfg *config.Config) {
		cfg.RoutingWindow = 5 * time.Minute
		cfg.PoolDwellWindow = 30 * time.Second
		cfg.PollAssignmentWait = 0
	})

	prompter := h.registerAccount(t, "tom")
	responder := h.registerAccount(t, "noah")
	sessionID := h.createSession(t, prompter.apiKey)
	jobID := h.postMessage(t, prompter.apiKey, sessionID, "claimed pool job should stay put")

	h.execSQL(t, `UPDATE jobs SET status = 'system_pool', last_system_pool_entered_at = now() WHERE id = $1`, jobID)

	h.requestJSON(t, http.MethodPost, "/jobs/"+jobID+"/claim", responder.apiKey, nil, http.StatusOK, nil)
	h.execSQL(t, `UPDATE jobs SET last_system_pool_entered_at = now() - interval '31 seconds' WHERE id = $1`, jobID)

	var internal struct {
		Affected int64 `json:"affected"`
	}
	h.requestJSON(t, http.MethodPost, "/internal/jobs/process-pool-rotation", "", nil, http.StatusOK, &internal)
	if internal.Affected != 0 {
		t.Fatalf("affected = %d, want 0", internal.Affected)
	}

	var job struct {
		Status string `json:"status"`
	}
	h.requestJSON(t, http.MethodGet, "/jobs/"+jobID, prompter.apiKey, nil, http.StatusOK, &job)
	if job.Status != "system_pool" {
		t.Fatalf("job status = %q, want %q", job.Status, "system_pool")
	}

	var work struct {
		Mode  string `json:"mode"`
		JobID string `json:"job_id"`
	}
	h.requestJSON(t, http.MethodGet, "/responders/work", responder.apiKey, nil, http.StatusOK, &work)
	if work.Mode != "assigned" || work.JobID != jobID {
		t.Fatalf("unexpected claimed-work payload: %+v", work)
	}
}

type testAccount struct {
	accountID string
	apiKey    string
}

func newIntegrationHarness(t *testing.T) *integrationHarness {
	return newIntegrationHarnessWithConfig(t, nil)
}

func newIntegrationHarnessWithConfig(t *testing.T, mutate func(*config.Config)) *integrationHarness {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseURL := integrationDatabaseURL()
	adminPool, err := db.Connect(ctx, baseURL)
	if err != nil {
		t.Skipf("integration db unavailable: %v", err)
	}

	schema := domain.NewID("itest")
	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}

	appURL := withSearchPath(baseURL, schema)
	appPool, err := db.Connect(ctx, appURL)
	if err != nil {
		_, _ = adminPool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
		adminPool.Close()
		t.Fatalf("connect app pool: %v", err)
	}

	migrationsDir := filepath.Join("..", "..", "migrations")
	if err := db.Migrate(ctx, appPool, migrationsDir); err != nil {
		appPool.Close()
		_, _ = adminPool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
		adminPool.Close()
		t.Fatalf("migrate: %v", err)
	}

	cfg := config.Config{
		HTTPAddr:                  ":0",
		DatabaseURL:               appURL,
		FrontendOrigin:            "http://localhost:5173",
		PublicAPIBase:             "http://localhost:8080",
		AuthTokenSecret:           "integration-secret",
		AdminPathToken:            "integration-admin",
		GitHubClientID:            "",
		GitHubClientSecret:        "",
		WorkerTick:                time.Second,
		PostFee:                   2.0,
		ResponderPool:             1.4,
		ResponderStake:            0.6,
		DispatcherPool:            0.4,
		Sink:                      0.2,
		DispatcherStake:           0.2,
		DispatcherRefusalPenalty:  0.1,
		PrompterCancelPenalty:     0.2,
		BadFeedbackTipRefundRatio: 0.5,
		AutoReviewPrompterPenalty: 0.6,
		AutoReviewResponderReward: 0.4,
		AccountInitialBalance:     100,
		RefreshInterval:           5 * time.Hour,
		AccountRefreshThreshold:   5,
		AccountRefreshTarget:      25,
		RoutingWindow:             0,
		PoolDwellWindow:           60 * time.Second,
		ReviewWindow:              24 * time.Hour,
		AssignmentDeadline:        30 * time.Minute,
		PollAssignmentWait:        0,
		ResponderActiveWindow:     12 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	appServer := New(appPool, cfg)
	srv := httptest.NewServer(appServer.Routes())
	h := &integrationHarness{
		app:       appServer,
		server:    srv,
		adminPool: adminPool,
		appPool:   appPool,
		baseURL:   srv.URL,
		schema:    schema,
	}
	t.Cleanup(func() {
		srv.Close()
		appPool.Close()
		_, _ = adminPool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
		adminPool.Close()
	})
	return h
}

func integrationDatabaseURL() string {
	if v := os.Getenv("CLAWGRID_TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://clawgrid:clawgrid@localhost:5432/clawgrid?sslmode=disable"
}

func withSearchPath(baseURL, schema string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func (h *integrationHarness) registerAccount(t *testing.T, name string) testAccount {
	t.Helper()
	accountID, err := h.app.upsertGitHubAccount(context.Background(), gitHubUser{
		ID:        atomic.AddInt64(&testGitHubUserSeq, 1),
		Login:     name,
		AvatarURL: "https://example.com/avatar.png",
	})
	if err != nil {
		t.Fatalf("registerAccount failed: %v", err)
	}
	var apiKey string
	if err := h.appPool.QueryRow(context.Background(), `SELECT id FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL ORDER BY created_at ASC LIMIT 1`, accountID).Scan(&apiKey); err != nil {
		t.Fatalf("lookup api key failed: %v", err)
	}
	return testAccount{accountID: accountID, apiKey: apiKey}
}

func (h *integrationHarness) completeGitHubOAuthLogin(t *testing.T, userID int64, login, turnstileToken string) (string, string) {
	t.Helper()

	code := fmt.Sprintf("oauth-code-%d", userID)
	accessToken := fmt.Sprintf("access-token-%d", userID)
	var expectedVerifier string
	h.app.exchangeGitHubCode = func(_ context.Context, gotCode, gotVerifier string) (string, error) {
		if gotCode != code {
			return "", errors.New("unexpected_github_code")
		}
		if gotVerifier == "" || gotVerifier != expectedVerifier {
			return "", errors.New("unexpected_github_code_verifier")
		}
		return accessToken, nil
	}
	h.app.fetchGitHubUser = func(_ context.Context, gotToken string) (gitHubUser, error) {
		if gotToken != accessToken {
			return gitHubUser{}, errors.New("unexpected_github_access_token")
		}
		return gitHubUser{
			ID:        userID,
			Login:     login,
			AvatarURL: "https://example.com/avatar.png",
		}, nil
	}

	var start struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	body := map[string]any{}
	if turnstileToken != "" {
		body["turnstile_token"] = turnstileToken
	}
	h.requestJSONWithHeaders(t, http.MethodPost, "/accounts/oauth/github/start", "", map[string]string{
		"Origin": h.app.cfg.FrontendOrigin,
	}, body, http.StatusOK, &start)

	parsedAuthorizeURL, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	state := parsedAuthorizeURL.Query().Get("state")
	if state == "" {
		t.Fatal("authorize URL did not include state")
	}
	expectedVerifier = h.scalarString(t, `SELECT code_verifier FROM github_oauth_states WHERE state_hash = $1`, hash(h.app.cfg.AuthTokenSecret+state))

	req := httptest.NewRequest(http.MethodGet, h.baseURL+"/accounts/oauth/github/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	rec := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("oauth callback status = %d, want %d, body=%s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}

	location := rec.Header().Get("Location")
	if location == "" {
		t.Fatal("oauth callback redirect location was empty")
	}
	parsedLocation, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse oauth callback redirect: %v", err)
	}
	completionCode := parsedLocation.Query().Get("oauth_complete")
	if completionCode == "" {
		t.Fatalf("oauth callback redirect missing completion code: %s", location)
	}

	var exchanged struct {
		AccountID    string `json:"account_id"`
		SessionToken string `json:"session_token"`
	}
	h.requestJSON(t, http.MethodPost, "/accounts/oauth/github/exchange", "", map[string]any{
		"code": completionCode,
	}, http.StatusOK, &exchanged)

	return exchanged.AccountID, exchanged.SessionToken
}

func (h *integrationHarness) createSession(t *testing.T, apiKey string) string {
	t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	h.requestJSON(t, http.MethodPost, "/sessions", apiKey, nil, http.StatusCreated, &out)
	return out.ID
}

func (h *integrationHarness) postMessage(t *testing.T, apiKey, sessionID, content string) string {
	t.Helper()
	var out struct {
		JobID string `json:"job_id"`
	}
	h.requestJSON(t, http.MethodPost, "/sessions/"+sessionID+"/messages", apiKey, map[string]any{
		"content":            content,
		"time_limit_minutes": 5,
	}, http.StatusCreated, &out)
	return out.JobID
}

func (h *integrationHarness) seedRatedCompletedJob(t *testing.T, prompterAccountID, responderAccountID, dispatcherAccountID, vote string) {
	t.Helper()

	now := time.Now().UTC()
	sessionID := domain.NewID("ses")
	requestMessageID := domain.NewID("msg")
	responseMessageID := domain.NewID("msg")
	jobID := domain.NewID("job")
	assignmentID := domain.NewID("asn")

	h.execSQL(t, `INSERT INTO sessions(id, owner_type, owner_id, status, created_at) VALUES ($1, 'account', $2, 'active', $3)`,
		sessionID, prompterAccountID, now.Add(-2*time.Hour))
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content, created_at) VALUES ($1, $2, 'account', $3, 'text', 'prompter', 'prompt', $4)`,
		requestMessageID, sessionID, prompterAccountID, now.Add(-2*time.Hour))
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content, created_at) VALUES ($1, $2, 'account', $3, 'text', 'responder', 'reply', $4)`,
		responseMessageID, sessionID, responderAccountID, now.Add(-90*time.Minute))
	h.execSQL(t, `
INSERT INTO jobs(
  id, session_id, request_message_id, owner_type, owner_id, status,
  created_at, activated_at, routing_ends_at, response_message_id,
  tip_amount, post_fee_amount, prompter_vote, review_deadline_at
) VALUES ($1,$2,$3,'account',$4,'completed',$5,$6,$7,$8,0,2,$9,$10)`,
		jobID, sessionID, requestMessageID, prompterAccountID,
		now.Add(-2*time.Hour), now.Add(-2*time.Hour), now.Add(-110*time.Minute),
		responseMessageID, vote, now.Add(-60*time.Minute))
	h.execSQL(t, `
INSERT INTO assignments(
  id, job_id, dispatcher_owner_type, dispatcher_owner_id,
  responder_owner_type, responder_owner_id, assigned_at, deadline_at, status
) VALUES ($1,$2,'account',$3,'account',$4,$5,$6,'success')`,
		assignmentID, jobID, dispatcherAccountID, responderAccountID, now.Add(-100*time.Minute), now.Add(-70*time.Minute))
}

func (h *integrationHarness) requestJSON(t *testing.T, method, path, apiKey string, body any, wantStatus int, out any) {
	t.Helper()
	h.requestJSONWithHeaders(t, method, path, apiKey, nil, body, wantStatus, out)
}

func (h *integrationHarness) requestJSONWithHeaders(t *testing.T, method, path, apiKey string, headers map[string]string, body any, wantStatus int, out any) {
	t.Helper()

	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}

	req := httptest.NewRequest(method, h.baseURL+path, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(rec, req)

	if rec.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d, body=%s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	if out == nil {
		return
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode response for %s %s: %v, body=%s", method, path, err, rec.Body.String())
	}
}

func (h *integrationHarness) rawRequest(t *testing.T, client *http.Client, method, path string, body any, headers map[string]string, wantStatus int) []byte {
	t.Helper()

	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}

	req, err := http.NewRequest(method, h.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s request failed: %v", method, path, err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d, body=%s", method, path, resp.StatusCode, wantStatus, buf.String())
	}
	return buf.Bytes()
}

func (h *integrationHarness) walletBalance(t *testing.T, apiKey string) float64 {
	t.Helper()

	var out struct {
		Balance float64 `json:"balance"`
	}
	h.requestJSON(t, http.MethodGet, "/wallets/current", apiKey, nil, http.StatusOK, &out)
	return out.Balance
}

func (h *integrationHarness) execSQL(t *testing.T, query string, args ...any) {
	t.Helper()

	if _, err := h.appPool.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("execSQL failed: %v", err)
	}
}

func (h *integrationHarness) scalarInt(t *testing.T, query string, args ...any) int {
	t.Helper()

	var value int
	if err := h.appPool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("scalarInt failed: %v", err)
	}
	return value
}

func (h *integrationHarness) scalarString(t *testing.T, query string, args ...any) string {
	t.Helper()

	var value string
	if err := h.appPool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("scalarString failed: %v", err)
	}
	return value
}
