package app

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"clawgrid/internal/config"
	"clawgrid/internal/db"
	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type serviceHarness struct {
	adminPool *pgxpool.Pool
	appPool   *pgxpool.Pool
	svc       *Service
	schema    string
	cfg       config.Config
}

type jobSeed struct {
	sessionID               string
	requestMessageID        string
	ownerType               string
	ownerID                 string
	status                  string
	routingEndsAt           time.Time
	responseMessageID       *string
	prompterVote            *string
	reviewDeadlineAt        *time.Time
	routingCycleCount       int
	lastRoutingEnteredAt    *time.Time
	lastSystemPoolEnteredAt *time.Time
	claimOwnerType          *string
	claimOwnerID            *string
	claimExpiresAt          *time.Time
	responderStakeAmount    float64
	responderStakeStatus    string
	dispatcherStakeAmount   float64
	dispatcherStakeStatus   string
}

func TestServiceProcessRoutingExpiry(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	ownerID := h.insertAccount(t, "tom")
	sessionID := h.insertSession(t, "account", ownerID)
	requestID := h.insertMessage(t, sessionID, "account", ownerID, "text", "hello")
	now := time.Now()

	dueJobID := h.insertJob(t, jobSeed{
		sessionID:        sessionID,
		requestMessageID: requestID,
		ownerType:        "account",
		ownerID:          ownerID,
		status:           "routing",
		routingEndsAt:    now.Add(-1 * time.Second),
	})
	freshJobID := h.insertJob(t, jobSeed{
		sessionID:        sessionID,
		requestMessageID: requestID,
		ownerType:        "account",
		ownerID:          ownerID,
		status:           "routing",
		routingEndsAt:    now.Add(10 * time.Minute),
	})

	affected, err := h.svc.ProcessRoutingExpiry(context.Background())
	if err != nil {
		t.Fatalf("ProcessRoutingExpiry: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}
	if got := h.jobStatus(t, dueJobID); got != "system_pool" {
		t.Fatalf("due job status = %q, want %q", got, "system_pool")
	}
	if got := h.jobRoutingCycleCount(t, dueJobID); got != 1 {
		t.Fatalf("due job routing_cycle_count = %d, want 1", got)
	}
	if got := h.jobStatus(t, freshJobID); got != "routing" {
		t.Fatalf("fresh job status = %q, want %q", got, "routing")
	}
}

func TestServiceProcessPoolRotation(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, func(cfg *config.Config) {
		cfg.RoutingWindow = 30 * time.Second
		cfg.PoolDwellWindow = 30 * time.Second
	})
	ownerID := h.insertAccount(t, "tom")
	sessionID := h.insertSession(t, "account", ownerID)
	requestID := h.insertMessage(t, sessionID, "account", ownerID, "text", "hello")
	now := time.Now()

	eligibleJobID := h.insertJob(t, jobSeed{
		sessionID:               sessionID,
		requestMessageID:        requestID,
		ownerType:               "account",
		ownerID:                 ownerID,
		status:                  "system_pool",
		routingEndsAt:           now.Add(1 * time.Hour),
		lastSystemPoolEnteredAt: ptrTime(now.Add(-31 * time.Second)),
	})
	claimOwnerType := "account"
	claimOwnerID := "acct_claimed"
	claimExpiresAt := now.Add(5 * time.Minute)
	claimedJobID := h.insertJob(t, jobSeed{
		sessionID:               sessionID,
		requestMessageID:        requestID,
		ownerType:               "account",
		ownerID:                 ownerID,
		status:                  "system_pool",
		routingEndsAt:           now.Add(1 * time.Hour),
		lastSystemPoolEnteredAt: ptrTime(now.Add(-31 * time.Second)),
		claimOwnerType:          &claimOwnerType,
		claimOwnerID:            &claimOwnerID,
		claimExpiresAt:          &claimExpiresAt,
	})
	nearExpiryJobID := h.insertJob(t, jobSeed{
		sessionID:               sessionID,
		requestMessageID:        requestID,
		ownerType:               "account",
		ownerID:                 ownerID,
		status:                  "system_pool",
		routingEndsAt:           now.Add(1 * time.Hour),
		lastSystemPoolEnteredAt: ptrTime(now.Add(-31 * time.Second)),
	})

	affected, err := h.svc.ProcessPoolRotation(context.Background())
	if err != nil {
		t.Fatalf("ProcessPoolRotation: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}
	if got := h.jobStatus(t, eligibleJobID); got != "routing" {
		t.Fatalf("eligible job status = %q, want %q", got, "routing")
	}
	if got := h.jobStatus(t, claimedJobID); got != "system_pool" {
		t.Fatalf("claimed job status = %q, want %q", got, "system_pool")
	}
	if got := h.jobStatus(t, nearExpiryJobID); got != "system_pool" {
		t.Fatalf("near-expiry job status = %q, want %q", got, "system_pool")
	}
}

func TestServiceProcessDispatchJobSnapshots(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	prompterID := h.insertAccount(t, "tom")
	responderID := h.insertAccount(t, "noah")
	sessionA := h.insertSession(t, "account", prompterID)
	sessionB := h.insertSession(t, "account", prompterID)
	requestA := h.insertMessage(t, sessionA, "account", prompterID, "text", "first prompt")
	requestB := h.insertMessage(t, sessionB, "account", prompterID, "text", "second prompt")
	now := time.Now()

	h.execSQL(t, `UPDATE sessions SET title = $2, dispatch_snippet = $3 WHERE id = $1`, sessionA, "session a", "prompter: first prompt")
	h.execSQL(t, `UPDATE sessions SET title = $2, dispatch_snippet = $3 WHERE id = $1`, sessionB, "session b", "prompter: second prompt")
	jobA := h.insertJob(t, jobSeed{
		sessionID:            sessionA,
		requestMessageID:     requestA,
		ownerType:            "account",
		ownerID:              prompterID,
		status:               "routing",
		routingEndsAt:        now.Add(10 * time.Minute),
		routingCycleCount:    2,
		lastRoutingEnteredAt: ptrTime(now.Add(-2 * time.Minute)),
	})
	jobB := h.insertJob(t, jobSeed{
		sessionID:            sessionB,
		requestMessageID:     requestB,
		ownerType:            "account",
		ownerID:              prompterID,
		status:               "routing",
		routingEndsAt:        now.Add(10 * time.Minute),
		lastRoutingEnteredAt: ptrTime(now.Add(-1 * time.Minute)),
	})
	_ = h.insertMessage(t, sessionA, "account", responderID, "reply", "reply")
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1,$2,'account',$3,'feedback','responder',$4)`,
		domain.NewID("msg"), sessionA, responderID, `a responder cancelled the assigned job due to "not a good fit"`)

	affected, err := h.svc.ProcessDispatchJobSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ProcessDispatchJobSnapshots: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}

	type row struct {
		Rank         int
		JobID        string
		SessionID    string
		Title        string
		Snippet      string
		CancelReason string
	}
	rows, err := h.appPool.Query(context.Background(), `
SELECT rank, job_id, session_id, session_title, session_snippet, last_responder_cancel_reason
FROM dispatch_job_snapshots
ORDER BY rank ASC`)
	if err != nil {
		t.Fatalf("query snapshots: %v", err)
	}
	defer rows.Close()

	got := []row{}
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.Rank, &item.JobID, &item.SessionID, &item.Title, &item.Snippet, &item.CancelReason); err != nil {
			t.Fatalf("scan snapshot row: %v", err)
		}
		got = append(got, item)
	}
	if len(got) != 2 {
		t.Fatalf("snapshot row count = %d, want 2", len(got))
	}
	if got[0].JobID != jobA {
		t.Fatalf("rank 1 job_id = %q, want %q", got[0].JobID, jobA)
	}
	if got[0].SessionID != sessionA || got[0].Title != "session a" || got[0].Snippet != "prompter: first prompt" {
		t.Fatalf("rank 1 snapshot row = %+v", got[0])
	}
	if got[0].CancelReason != "not a good fit" {
		t.Fatalf("rank 1 cancel reason = %q, want %q", got[0].CancelReason, "not a good fit")
	}
	if got[1].JobID != jobB {
		t.Fatalf("rank 2 job_id = %q, want %q", got[1].JobID, jobB)
	}
}

func TestServiceProcessAvailableResponderSnapshots(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 30 * time.Second
		cfg.ResponderActiveWindow = 12 * time.Second
	})
	prompterID := h.insertAccount(t, "tom")
	pollResponderID := h.insertAccount(t, "poll")
	hookResponderID := h.insertAccount(t, "hook")
	busyResponderID := h.insertAccount(t, "busy")
	claimedResponderID := h.insertAccount(t, "claimed")
	now := time.Now()

	h.execSQL(t, `UPDATE accounts SET responder_description = 'poll helper' WHERE id = $1`, pollResponderID)
	h.execSQL(t, `UPDATE accounts SET responder_description = 'hook helper' WHERE id = $1`, hookResponderID)
	h.execSQL(t, `INSERT INTO responder_availability(id, owner_type, owner_id, last_seen_at, poll_started_at) VALUES ($1,'account',$2,$3,$4)`,
		domain.NewID("av"), pollResponderID, now, now)
	h.execSQL(t, `INSERT INTO responder_availability(id, owner_type, owner_id, last_seen_at, poll_started_at) VALUES ($1,'account',$2,$3,$4)`,
		domain.NewID("av"), busyResponderID, now, now)
	h.execSQL(t, `INSERT INTO responder_availability(id, owner_type, owner_id, last_seen_at, poll_started_at) VALUES ($1,'account',$2,$3,$4)`,
		domain.NewID("av"), claimedResponderID, now, now)
	h.execSQL(t, `
INSERT INTO account_hooks(
  id, account_id, url, auth_token, enabled, status, notify_assignment_received, notify_reply_received, verified_at
) VALUES (
  $1, $2, 'https://hook.example/hooks/agent', 'secret', TRUE, 'active', TRUE, FALSE, now()
)`, domain.NewID("ah"), hookResponderID)

	busySessionID := h.insertSession(t, "account", prompterID)
	busyRequestID := h.insertMessage(t, busySessionID, "account", prompterID, "text", "busy")
	busyJobID := h.insertJob(t, jobSeed{
		sessionID:        busySessionID,
		requestMessageID: busyRequestID,
		ownerType:        "account",
		ownerID:          prompterID,
		status:           "assigned",
		routingEndsAt:    now.Add(10 * time.Minute),
	})
	h.insertAssignment(t, busyJobID, prompterID, busyResponderID, now.Add(10*time.Minute), "active")

	claimedSessionID := h.insertSession(t, "account", prompterID)
	claimedRequestID := h.insertMessage(t, claimedSessionID, "account", prompterID, "text", "claimed")
	h.insertJob(t, jobSeed{
		sessionID:        claimedSessionID,
		requestMessageID: claimedRequestID,
		ownerType:        "account",
		ownerID:          prompterID,
		status:           "system_pool",
		routingEndsAt:    now.Add(10 * time.Minute),
		claimOwnerType:   ptrString("account"),
		claimOwnerID:     ptrString(claimedResponderID),
		claimExpiresAt:   ptrTime(now.Add(10 * time.Minute)),
	})

	affected, err := h.svc.ProcessAvailableResponderSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ProcessAvailableResponderSnapshots: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}

	rows, err := h.appPool.Query(context.Background(), `
SELECT owner_id, availability_mode, display_name, responder_description
FROM available_responder_snapshots
ORDER BY rank ASC`)
	if err != nil {
		t.Fatalf("query responder snapshots: %v", err)
	}
	defer rows.Close()

	type row struct {
		OwnerID     string
		Mode        string
		DisplayName string
		Description string
	}
	got := []row{}
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.OwnerID, &item.Mode, &item.DisplayName, &item.Description); err != nil {
			t.Fatalf("scan responder snapshot row: %v", err)
		}
		got = append(got, item)
	}
	if len(got) != 2 {
		t.Fatalf("snapshot row count = %d, want 2", len(got))
	}
	if got[0].OwnerID != pollResponderID || got[0].Mode != "poll" || got[0].Description != "poll helper" {
		t.Fatalf("first responder snapshot row = %+v", got[0])
	}
	if got[1].OwnerID != hookResponderID || got[1].Mode != "hook" || got[1].Description != "hook helper" {
		t.Fatalf("second responder snapshot row = %+v", got[1])
	}
}

func TestServiceProcessAvailableResponderSnapshotsIncludesPollingResponderWithReplyOnlyHook(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, func(cfg *config.Config) {
		cfg.PollAssignmentWait = 30 * time.Second
		cfg.ResponderActiveWindow = 12 * time.Second
	})
	responderID := h.insertAccount(t, "pollhook")
	now := time.Now()

	h.execSQL(t, `UPDATE accounts SET responder_description = 'reply-only helper' WHERE id = $1`, responderID)
	h.execSQL(t, `INSERT INTO responder_availability(id, owner_type, owner_id, last_seen_at, poll_started_at) VALUES ($1,'account',$2,$3,$4)`,
		domain.NewID("av"), responderID, now, now)
	h.execSQL(t, `
INSERT INTO account_hooks(
  id, account_id, url, auth_token, enabled, status, notify_assignment_received, notify_reply_received, verified_at
) VALUES (
  $1, $2, 'https://hook.example/hooks/agent', 'secret', TRUE, 'active', FALSE, TRUE, now()
)`, domain.NewID("ah"), responderID)

	affected, err := h.svc.ProcessAvailableResponderSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ProcessAvailableResponderSnapshots: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}

	var mode string
	var description string
	if err := h.appPool.QueryRow(context.Background(), `
SELECT availability_mode, responder_description
FROM available_responder_snapshots
WHERE owner_id = $1`, responderID).Scan(&mode, &description); err != nil {
		t.Fatalf("load responder snapshot: %v", err)
	}
	if mode != "poll" {
		t.Fatalf("availability_mode = %q, want %q", mode, "poll")
	}
	if description != "reply-only helper" {
		t.Fatalf("responder_description = %q, want %q", description, "reply-only helper")
	}
}

func TestServiceProcessAssignmentTimeouts(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	prompterID := h.insertAccount(t, "tom")
	dispatcherID := h.insertAccount(t, "dora")
	responderID := h.insertAccount(t, "noah")
	h.insertWallet(t, "account", dispatcherID, 0.2, nil)
	h.insertWallet(t, "account", responderID, 10.0, nil)
	sessionID := h.insertSession(t, "account", prompterID)
	requestID := h.insertMessage(t, sessionID, "account", prompterID, "text", "hello")
	now := time.Now()

	timeoutJobID := h.insertJob(t, jobSeed{
		sessionID:             sessionID,
		requestMessageID:      requestID,
		ownerType:             "account",
		ownerID:               prompterID,
		status:                "assigned",
		routingEndsAt:         now.Add(1 * time.Hour),
		responderStakeAmount:  h.cfg.ResponderStake,
		responderStakeStatus:  "held",
		dispatcherStakeAmount: h.cfg.DispatcherStake,
		dispatcherStakeStatus: "held",
	})
	timeoutAssignmentID := h.insertAssignment(t, timeoutJobID, dispatcherID, responderID, now.Add(-1*time.Second), "active")

	freshJobID := h.insertJob(t, jobSeed{
		sessionID:             sessionID,
		requestMessageID:      requestID,
		ownerType:             "account",
		ownerID:               prompterID,
		status:                "assigned",
		routingEndsAt:         now.Add(1 * time.Hour),
		responderStakeAmount:  h.cfg.ResponderStake,
		responderStakeStatus:  "held",
		dispatcherStakeAmount: h.cfg.DispatcherStake,
		dispatcherStakeStatus: "held",
	})
	freshAssignmentID := h.insertAssignment(t, freshJobID, dispatcherID, responderID, now.Add(10*time.Minute), "active")

	claimTimeoutJobID := h.insertJob(t, jobSeed{
		sessionID:               sessionID,
		requestMessageID:        requestID,
		ownerType:               "account",
		ownerID:                 prompterID,
		status:                  "system_pool",
		routingEndsAt:           now.Add(1 * time.Hour),
		lastSystemPoolEnteredAt: ptrTime(now.Add(-1 * time.Minute)),
		claimOwnerType:          ptrString("account"),
		claimOwnerID:            ptrString(responderID),
		claimExpiresAt:          ptrTime(now.Add(-1 * time.Second)),
		responderStakeAmount:    h.cfg.ResponderStake,
		responderStakeStatus:    "held",
	})
	h.execSQL(t, `UPDATE wallets SET balance = balance - $1 WHERE owner_type = 'account' AND owner_id = $2`, h.cfg.ResponderStake*3, responderID)

	affected, err := h.svc.ProcessAssignmentTimeouts(context.Background())
	if err != nil {
		t.Fatalf("ProcessAssignmentTimeouts: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}
	if got := h.assignmentStatus(t, timeoutAssignmentID); got != "timeout" {
		t.Fatalf("timed out assignment status = %q, want %q", got, "timeout")
	}
	if got := h.jobStatus(t, timeoutJobID); got != "system_pool" {
		t.Fatalf("timed out job status = %q, want %q", got, "system_pool")
	}
	if got := h.assignmentStatus(t, freshAssignmentID); got != "active" {
		t.Fatalf("fresh assignment status = %q, want %q", got, "active")
	}
	if got := h.jobStatus(t, freshJobID); got != "assigned" {
		t.Fatalf("fresh job status = %q, want %q", got, "assigned")
	}
	if got := h.jobStakeStatus(t, freshJobID); got != "held" {
		t.Fatalf("fresh job stake status = %q, want %q", got, "held")
	}
	if got := h.jobDispatcherStakeStatus(t, freshJobID); got != "held" {
		t.Fatalf("fresh job dispatcher stake status = %q, want %q", got, "held")
	}
	if got := h.jobStakeStatus(t, timeoutJobID); got != "slashed" {
		t.Fatalf("timed out assignment stake status = %q, want %q", got, "slashed")
	}
	if got := h.jobDispatcherStakeStatus(t, timeoutJobID); got != "returned" {
		t.Fatalf("timed out assignment dispatcher stake status = %q, want %q", got, "returned")
	}
	if got := h.jobStakeStatus(t, claimTimeoutJobID); got != "slashed" {
		t.Fatalf("timed out claim stake status = %q, want %q", got, "slashed")
	}
	if got := h.walletBalance(t, "account", responderID); got != 8.2 {
		t.Fatalf("responder balance = %v, want %v", got, 8.2)
	}
	if got := h.walletBalance(t, "account", dispatcherID); got != 0.2 {
		t.Fatalf("dispatcher balance = %v, want %v", got, 0.2)
	}
}

func TestServiceProcessAutoReviewPenalizesPrompterAndRewardsResponder(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	ownerID := h.insertAccount(t, "tom")
	dispatcherID := h.insertAccount(t, "dora")
	responderID := h.insertAccount(t, "noah")
	h.insertWallet(t, "account", ownerID, 1.0, nil)
	h.insertWallet(t, "account", dispatcherID, 0.2, nil)
	h.insertWallet(t, "account", responderID, 5.0, nil)
	sessionID := h.insertSession(t, "account", ownerID)
	requestID := h.insertMessage(t, sessionID, "account", ownerID, "text", "hello")
	replyID := h.insertMessage(t, sessionID, "account", responderID, "reply", "response")
	now := time.Now()

	dueJobID := h.insertJob(t, jobSeed{
		sessionID:             sessionID,
		requestMessageID:      requestID,
		ownerType:             "account",
		ownerID:               ownerID,
		status:                "assigned",
		routingEndsAt:         now.Add(1 * time.Hour),
		responseMessageID:     &replyID,
		reviewDeadlineAt:      ptrTime(now.Add(-1 * time.Second)),
		responderStakeAmount:  h.cfg.ResponderStake,
		responderStakeStatus:  "held",
		dispatcherStakeAmount: h.cfg.DispatcherStake,
		dispatcherStakeStatus: "held",
	})
	h.execSQL(t, `UPDATE jobs SET tip_amount = 0.6 WHERE id = $1`, dueJobID)
	h.execSQL(t, `UPDATE wallets SET balance = balance - $1 WHERE owner_type = 'account' AND owner_id = $2`, h.cfg.ResponderStake, responderID)
	h.insertAssignment(t, dueJobID, dispatcherID, responderID, now.Add(-2*time.Minute), "success")
	h.execSQL(t, `UPDATE wallets SET balance = balance - $1 WHERE owner_type = 'account' AND owner_id = $2`, h.cfg.DispatcherStake, dispatcherID)
	futureJobID := h.insertJob(t, jobSeed{
		sessionID:         sessionID,
		requestMessageID:  requestID,
		ownerType:         "account",
		ownerID:           ownerID,
		status:            "assigned",
		routingEndsAt:     now.Add(1 * time.Hour),
		responseMessageID: &replyID,
		reviewDeadlineAt:  ptrTime(now.Add(1 * time.Hour)),
	})

	affected, err := h.svc.ProcessAutoReview(context.Background())
	if err != nil {
		t.Fatalf("ProcessAutoReview: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}
	if got := h.jobStatus(t, dueJobID); got != "auto_settled" {
		t.Fatalf("due job status = %q, want %q", got, "auto_settled")
	}
	if got := h.jobVote(t, dueJobID); got != "auto" {
		t.Fatalf("due job vote = %q, want %q", got, "auto")
	}
	if got := h.feedbackCountByContent(t, sessionID, "no feedback"); got != 1 {
		t.Fatalf("no-feedback message count = %d, want 1", got)
	}
	if got := h.jobStakeStatus(t, dueJobID); got != "returned" {
		t.Fatalf("due job stake status = %q, want %q", got, "returned")
	}
	if got := h.jobDispatcherStakeStatus(t, dueJobID); got != "returned" {
		t.Fatalf("due job dispatcher stake status = %q, want %q", got, "returned")
	}
	if got := h.walletBalance(t, "account", responderID); got != 5.4 {
		t.Fatalf("responder balance = %v, want %v", got, 5.4)
	}
	if got := h.walletBalance(t, "account", ownerID); got != 0.4 {
		t.Fatalf("prompter balance = %v, want %v", got, 0.4)
	}
	if got := h.walletBalance(t, "account", dispatcherID); got != 0.2 {
		t.Fatalf("dispatcher balance = %v, want %v", got, 0.2)
	}
	if got := h.jobStatus(t, futureJobID); got != "assigned" {
		t.Fatalf("future job status = %q, want %q", got, "assigned")
	}
	if got := h.jobVote(t, futureJobID); got != "" {
		t.Fatalf("future job vote = %q, want empty", got)
	}
}

func TestServiceProcessWalletRefresh(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	oldRefresh := time.Now().Add(-6 * time.Hour)
	recentRefresh := time.Now().Add(-1 * time.Hour)

	accountRefreshID := h.insertAccount(t, "tom")
	accountBelowTargetID := h.insertAccount(t, "sam")
	accountAtTargetID := h.insertAccount(t, "ava")
	accountTooRecentID := h.insertAccount(t, "noah")

	h.insertWallet(t, "account", accountRefreshID, 4.0, &oldRefresh)
	h.insertWallet(t, "account", accountBelowTargetID, 8.0, &oldRefresh)
	h.insertWallet(t, "account", accountAtTargetID, 25.0, &oldRefresh)
	h.insertWallet(t, "account", accountTooRecentID, 4.0, &recentRefresh)

	affected, err := h.svc.ProcessWalletRefresh(context.Background())
	if err != nil {
		t.Fatalf("ProcessWalletRefresh: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}
	if got := h.walletBalance(t, "account", accountRefreshID); got != 25.0 {
		t.Fatalf("account refresh balance = %v, want 25.0", got)
	}
	if got := h.walletBalance(t, "account", accountBelowTargetID); got != 25.0 {
		t.Fatalf("account below-target balance = %v, want 25.0", got)
	}
	if got := h.walletBalance(t, "account", accountAtTargetID); got != 25.0 {
		t.Fatalf("account at-target balance = %v, want 25.0", got)
	}
	if got := h.walletBalance(t, "account", accountTooRecentID); got != 4.0 {
		t.Fatalf("account too-recent balance = %v, want 4.0", got)
	}
	if got := h.ledgerCountByReason(t, "account", accountRefreshID, "wallet_refresh"); got != 1 {
		t.Fatalf("wallet_refresh ledger count for refreshed account = %d, want 1", got)
	}
	if got := h.ledgerCountByReason(t, "account", accountBelowTargetID, "wallet_refresh"); got != 1 {
		t.Fatalf("wallet_refresh ledger count for below-target account = %d, want 1", got)
	}
	if got := h.ledgerDeltaByReason(t, "account", accountRefreshID, "wallet_refresh"); got != 21.0 {
		t.Fatalf("wallet_refresh delta for refreshed account = %v, want 21.0", got)
	}
	if got := h.ledgerDeltaByReason(t, "account", accountBelowTargetID, "wallet_refresh"); got != 17.0 {
		t.Fatalf("wallet_refresh delta for below-target account = %v, want 17.0", got)
	}
	if got := h.ledgerCountByReason(t, "account", accountAtTargetID, "wallet_refresh"); got != 0 {
		t.Fatalf("wallet_refresh ledger count for at-target account = %d, want 0", got)
	}
	if got := h.ledgerCountByReason(t, "account", accountTooRecentID, "wallet_refresh"); got != 0 {
		t.Fatalf("wallet_refresh ledger count for too-recent account = %d, want 0", got)
	}
}

func newServiceHarness(t *testing.T, mutate func(*config.Config)) *serviceHarness {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseURL := serviceTestDatabaseURL()
	adminPool, err := db.Connect(ctx, baseURL)
	if err != nil {
		t.Skipf("service test db unavailable: %v", err)
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
		DatabaseURL:               appURL,
		PublicAPIBase:             "http://localhost:8080",
		AuthTokenSecret:           "service-test-secret",
		AdminPathToken:            "service-test-admin",
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
		AccountRefreshTarget:      25,
		RoutingWindow:             30 * time.Second,
		PoolDwellWindow:           30 * time.Second,
		ReviewWindow:              24 * time.Hour,
		AssignmentDeadline:        30 * time.Minute,
		PollAssignmentWait:        30 * time.Second,
		ResponderActiveWindow:     12 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	h := &serviceHarness{
		adminPool: adminPool,
		appPool:   appPool,
		svc:       NewService(appPool, cfg),
		schema:    schema,
		cfg:       cfg,
	}
	t.Cleanup(func() {
		appPool.Close()
		_, _ = adminPool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
		adminPool.Close()
	})
	return h
}

func serviceTestDatabaseURL() string {
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

func (h *serviceHarness) insertAccount(t *testing.T, name string) string {
	t.Helper()
	id := domain.NewID("acct")
	h.execSQL(t, `INSERT INTO accounts(id, name) VALUES ($1, $2)`, id, name)
	return id
}

func (h *serviceHarness) insertWallet(t *testing.T, ownerType, ownerID string, balance float64, lastRefreshAt *time.Time) string {
	t.Helper()
	id := domain.NewID("wal")
	h.execSQL(t, `INSERT INTO wallets(id, owner_type, owner_id, balance, last_refresh_at) VALUES ($1, $2, $3, $4, $5)`, id, ownerType, ownerID, balance, lastRefreshAt)
	return id
}

func (h *serviceHarness) insertSession(t *testing.T, ownerType, ownerID string) string {
	t.Helper()
	id := domain.NewID("ses")
	h.execSQL(t, `INSERT INTO sessions(id, owner_type, owner_id, status) VALUES ($1, $2, $3, 'active')`, id, ownerType, ownerID)
	return id
}

func (h *serviceHarness) insertMessage(t *testing.T, sessionID, ownerType, ownerID, typ, content string) string {
	t.Helper()
	id := domain.NewID("msg")
	role := "prompter"
	if typ == "reply" {
		typ = "text"
		role = "responder"
	}
	h.execSQL(t, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, sessionID, ownerType, ownerID, typ, role, content)
	return id
}

func (h *serviceHarness) insertJob(t *testing.T, seed jobSeed) string {
	t.Helper()
	id := domain.NewID("job")
	h.execSQL(t, `
INSERT INTO jobs(
  id, session_id, request_message_id, owner_type, owner_id, status,
  activated_at, routing_ends_at, response_message_id,
  prompter_vote, review_deadline_at, routing_cycle_count,
  last_routing_entered_at, last_system_pool_entered_at,
  claim_owner_type, claim_owner_id, claim_expires_at,
  responder_stake_amount, responder_stake_status,
  dispatcher_stake_amount, dispatcher_stake_status
) VALUES (
  $1, $2, $3, $4, $5, $6,
  now(), $7, $8,
  $9, $10, $11,
  $12, $13,
  $14, $15, $16,
  $17, $18,
  $19, $20
)`,
		id,
		seed.sessionID,
		seed.requestMessageID,
		seed.ownerType,
		seed.ownerID,
		seed.status,
		seed.routingEndsAt,
		seed.responseMessageID,
		seed.prompterVote,
		seed.reviewDeadlineAt,
		seed.routingCycleCount,
		seed.lastRoutingEnteredAt,
		seed.lastSystemPoolEnteredAt,
		seed.claimOwnerType,
		seed.claimOwnerID,
		seed.claimExpiresAt,
		seed.responderStakeAmount,
		seed.responderStakeStatus,
		seed.dispatcherStakeAmount,
		seed.dispatcherStakeStatus,
	)
	return id
}

func (h *serviceHarness) insertAssignment(t *testing.T, jobID, dispatcherID, responderID string, deadline time.Time, status string) string {
	t.Helper()
	id := domain.NewID("asn")
	h.execSQL(t, `
INSERT INTO assignments(id, job_id, dispatcher_owner_type, dispatcher_owner_id, responder_owner_type, responder_owner_id, deadline_at, status)
VALUES ($1, $2, 'account', $3, 'account', $4, $5, $6)`,
		id, jobID, dispatcherID, responderID, deadline, status,
	)
	return id
}

func (h *serviceHarness) jobStatus(t *testing.T, jobID string) string {
	t.Helper()
	var status string
	if err := h.appPool.QueryRow(context.Background(), `SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&status); err != nil {
		t.Fatalf("jobStatus: %v", err)
	}
	return status
}

func (h *serviceHarness) jobRoutingCycleCount(t *testing.T, jobID string) int {
	t.Helper()
	var count int
	if err := h.appPool.QueryRow(context.Background(), `SELECT routing_cycle_count FROM jobs WHERE id = $1`, jobID).Scan(&count); err != nil {
		t.Fatalf("jobRoutingCycleCount: %v", err)
	}
	return count
}

func (h *serviceHarness) assignmentStatus(t *testing.T, assignmentID string) string {
	t.Helper()
	var status string
	if err := h.appPool.QueryRow(context.Background(), `SELECT status FROM assignments WHERE id = $1`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("assignmentStatus: %v", err)
	}
	return status
}

func (h *serviceHarness) jobVote(t *testing.T, jobID string) string {
	t.Helper()
	var vote string
	if err := h.appPool.QueryRow(context.Background(), `SELECT COALESCE(prompter_vote, '') FROM jobs WHERE id = $1`, jobID).Scan(&vote); err != nil {
		t.Fatalf("jobVote: %v", err)
	}
	return vote
}

func (h *serviceHarness) walletBalance(t *testing.T, ownerType, ownerID string) float64 {
	t.Helper()
	var balance float64
	if err := h.appPool.QueryRow(context.Background(), `SELECT balance FROM wallets WHERE owner_type = $1 AND owner_id = $2`, ownerType, ownerID).Scan(&balance); err != nil {
		t.Fatalf("walletBalance: %v", err)
	}
	return balance
}

func (h *serviceHarness) jobStakeStatus(t *testing.T, jobID string) string {
	t.Helper()
	var status string
	if err := h.appPool.QueryRow(context.Background(), `SELECT responder_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&status); err != nil {
		t.Fatalf("jobStakeStatus: %v", err)
	}
	return status
}

func (h *serviceHarness) jobDispatcherStakeStatus(t *testing.T, jobID string) string {
	t.Helper()
	var status string
	if err := h.appPool.QueryRow(context.Background(), `SELECT dispatcher_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&status); err != nil {
		t.Fatalf("jobDispatcherStakeStatus: %v", err)
	}
	return status
}

func (h *serviceHarness) feedbackCountByContent(t *testing.T, sessionID, content string) int {
	t.Helper()
	var count int
	if err := h.appPool.QueryRow(context.Background(), `SELECT COUNT(*)::int FROM messages WHERE session_id = $1 AND type = 'feedback' AND content = $2`, sessionID, content).Scan(&count); err != nil {
		t.Fatalf("feedbackCountByContent: %v", err)
	}
	return count
}

func (h *serviceHarness) ledgerCountByReason(t *testing.T, ownerType, ownerID, reason string) int {
	t.Helper()
	var count int
	if err := h.appPool.QueryRow(context.Background(), `SELECT COUNT(*)::int FROM wallet_ledger WHERE owner_type = $1 AND owner_id = $2 AND reason = $3`, ownerType, ownerID, reason).Scan(&count); err != nil {
		t.Fatalf("ledgerCountByReason: %v", err)
	}
	return count
}

func (h *serviceHarness) ledgerDeltaByReason(t *testing.T, ownerType, ownerID, reason string) float64 {
	t.Helper()
	var delta float64
	if err := h.appPool.QueryRow(context.Background(), `SELECT delta FROM wallet_ledger WHERE owner_type = $1 AND owner_id = $2 AND reason = $3 ORDER BY created_at DESC, id DESC LIMIT 1`, ownerType, ownerID, reason).Scan(&delta); err != nil {
		t.Fatalf("ledgerDeltaByReason: %v", err)
	}
	return delta
}

func (h *serviceHarness) execSQL(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.appPool.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("execSQL failed: %v", err)
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}

func ptrString(v string) *string {
	return &v
}
