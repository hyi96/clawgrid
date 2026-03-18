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
	expiresAt               time.Time
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
		expiresAt:        now.Add(1 * time.Hour),
		routingEndsAt:    now.Add(-1 * time.Second),
	})
	freshJobID := h.insertJob(t, jobSeed{
		sessionID:        sessionID,
		requestMessageID: requestID,
		ownerType:        "account",
		ownerID:          ownerID,
		status:           "routing",
		expiresAt:        now.Add(1 * time.Hour),
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
		expiresAt:               now.Add(1 * time.Hour),
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
		expiresAt:               now.Add(1 * time.Hour),
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
		expiresAt:               now.Add(10 * time.Second),
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

func TestServiceProcessAssignmentTimeouts(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	prompterID := h.insertAccount(t, "tom")
	dispatcherID := h.insertAccount(t, "dora")
	responderID := h.insertAccount(t, "noah")
	h.insertWallet(t, "account", responderID, 10.0, nil)
	sessionID := h.insertSession(t, "account", prompterID)
	requestID := h.insertMessage(t, sessionID, "account", prompterID, "text", "hello")
	now := time.Now()

	timeoutJobID := h.insertJob(t, jobSeed{
		sessionID:            sessionID,
		requestMessageID:     requestID,
		ownerType:            "account",
		ownerID:              prompterID,
		status:               "assigned",
		expiresAt:            now.Add(1 * time.Hour),
		routingEndsAt:        now.Add(1 * time.Hour),
		responderStakeAmount: h.cfg.ResponderStake,
		responderStakeStatus: "held",
	})
	timeoutAssignmentID := h.insertAssignment(t, timeoutJobID, dispatcherID, responderID, now.Add(-1*time.Second), "active")

	freshJobID := h.insertJob(t, jobSeed{
		sessionID:            sessionID,
		requestMessageID:     requestID,
		ownerType:            "account",
		ownerID:              prompterID,
		status:               "assigned",
		expiresAt:            now.Add(1 * time.Hour),
		routingEndsAt:        now.Add(1 * time.Hour),
		responderStakeAmount: h.cfg.ResponderStake,
		responderStakeStatus: "held",
	})
	freshAssignmentID := h.insertAssignment(t, freshJobID, dispatcherID, responderID, now.Add(10*time.Minute), "active")

	claimTimeoutJobID := h.insertJob(t, jobSeed{
		sessionID:               sessionID,
		requestMessageID:        requestID,
		ownerType:               "account",
		ownerID:                 prompterID,
		status:                  "system_pool",
		expiresAt:               now.Add(1 * time.Hour),
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
	if got := h.jobStakeStatus(t, timeoutJobID); got != "slashed" {
		t.Fatalf("timed out assignment stake status = %q, want %q", got, "slashed")
	}
	if got := h.jobStakeStatus(t, claimTimeoutJobID); got != "slashed" {
		t.Fatalf("timed out claim stake status = %q, want %q", got, "slashed")
	}
	if got := h.walletBalance(t, "account", responderID); got != 8.2 {
		t.Fatalf("responder balance = %v, want %v", got, 8.2)
	}
}

func TestServiceProcessExpiry(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	ownerID := h.insertAccount(t, "tom")
	responderID := h.insertAccount(t, "noah")
	sessionID := h.insertSession(t, "account", ownerID)
	requestID := h.insertMessage(t, sessionID, "account", ownerID, "text", "hello")
	now := time.Now()

	routingID := h.insertJob(t, jobSeed{
		sessionID:        sessionID,
		requestMessageID: requestID,
		ownerType:        "account",
		ownerID:          ownerID,
		status:           "routing",
		expiresAt:        now.Add(-1 * time.Second),
		routingEndsAt:    now.Add(1 * time.Hour),
	})
	assignedID := h.insertJob(t, jobSeed{
		sessionID:        sessionID,
		requestMessageID: requestID,
		ownerType:        "account",
		ownerID:          ownerID,
		status:           "assigned",
		expiresAt:        now.Add(-1 * time.Second),
		routingEndsAt:    now.Add(1 * time.Hour),
	})
	systemPoolID := h.insertJob(t, jobSeed{
		sessionID:               sessionID,
		requestMessageID:        requestID,
		ownerType:               "account",
		ownerID:                 ownerID,
		status:                  "system_pool",
		expiresAt:               now.Add(-1 * time.Second),
		routingEndsAt:           now.Add(1 * time.Hour),
		lastSystemPoolEnteredAt: ptrTime(now.Add(-10 * time.Second)),
	})
	activeAssignedID := h.insertJob(t, jobSeed{
		sessionID:            sessionID,
		requestMessageID:     requestID,
		ownerType:            "account",
		ownerID:              ownerID,
		status:               "assigned",
		expiresAt:            now.Add(-1 * time.Second),
		routingEndsAt:        now.Add(1 * time.Hour),
		responderStakeAmount: h.cfg.ResponderStake,
		responderStakeStatus: "held",
	})
	activeAssignmentID := h.insertAssignment(t, activeAssignedID, ownerID, responderID, now.Add(10*time.Minute), "active")
	activeClaimID := h.insertJob(t, jobSeed{
		sessionID:               sessionID,
		requestMessageID:        requestID,
		ownerType:               "account",
		ownerID:                 ownerID,
		status:                  "system_pool",
		expiresAt:               now.Add(-1 * time.Second),
		routingEndsAt:           now.Add(1 * time.Hour),
		lastSystemPoolEnteredAt: ptrTime(now.Add(-10 * time.Second)),
		claimOwnerType:          ptrString("account"),
		claimOwnerID:            ptrString(responderID),
		claimExpiresAt:          ptrTime(now.Add(10 * time.Minute)),
		responderStakeAmount:    h.cfg.ResponderStake,
		responderStakeStatus:    "held",
	})
	replyID := h.insertMessage(t, sessionID, "account", ownerID, "reply", "done")
	repliedID := h.insertJob(t, jobSeed{
		sessionID:         sessionID,
		requestMessageID:  requestID,
		ownerType:         "account",
		ownerID:           ownerID,
		status:            "assigned",
		expiresAt:         now.Add(-1 * time.Second),
		routingEndsAt:     now.Add(1 * time.Hour),
		responseMessageID: &replyID,
	})

	affected, err := h.svc.ProcessExpiry(context.Background())
	if err != nil {
		t.Fatalf("ProcessExpiry: %v", err)
	}
	if affected != 3 {
		t.Fatalf("affected = %d, want 3", affected)
	}
	if got := h.jobStatus(t, routingID); got != "expired" {
		t.Fatalf("routing job status = %q, want %q", got, "expired")
	}
	if got := h.jobStatus(t, assignedID); got != "expired" {
		t.Fatalf("assigned job status = %q, want %q", got, "expired")
	}
	if got := h.jobStatus(t, systemPoolID); got != "expired" {
		t.Fatalf("system pool job status = %q, want %q", got, "expired")
	}
	if got := h.jobStatus(t, activeAssignedID); got != "assigned" {
		t.Fatalf("active assigned job status = %q, want %q", got, "assigned")
	}
	if got := h.assignmentStatus(t, activeAssignmentID); got != "active" {
		t.Fatalf("active assignment status = %q, want %q", got, "active")
	}
	if got := h.jobStakeStatus(t, activeAssignedID); got != "held" {
		t.Fatalf("active assigned stake status = %q, want %q", got, "held")
	}
	if got := h.jobStatus(t, activeClaimID); got != "system_pool" {
		t.Fatalf("active claim job status = %q, want %q", got, "system_pool")
	}
	if got := h.jobStakeStatus(t, activeClaimID); got != "held" {
		t.Fatalf("active claim stake status = %q, want %q", got, "held")
	}
	if got := h.jobStatus(t, repliedID); got != "assigned" {
		t.Fatalf("replied job status = %q, want %q", got, "assigned")
	}
}

func TestServiceProcessGuestExpiry(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	responderID := h.insertAccount(t, "noah")
	oldGuestID := h.insertGuest(t, time.Now().Add(-25*time.Hour), nil)
	revokedAt := time.Now().Add(-1 * time.Hour)
	revokedGuestID := h.insertGuest(t, time.Now(), &revokedAt)
	activeGuestID := h.insertGuest(t, time.Now(), nil)
	accountID := h.insertAccount(t, "tom")

	oldSessionID := h.insertSession(t, "guest", oldGuestID)
	revokedSessionID := h.insertSession(t, "guest", revokedGuestID)
	activeSessionID := h.insertSession(t, "guest", activeGuestID)
	accountSessionID := h.insertSession(t, "account", accountID)

	oldRequestID := h.insertMessage(t, oldSessionID, "guest", oldGuestID, "text", "old")
	revokedRequestID := h.insertMessage(t, revokedSessionID, "guest", revokedGuestID, "text", "revoked")
	activeRequestID := h.insertMessage(t, activeSessionID, "guest", activeGuestID, "text", "active")
	accountRequestID := h.insertMessage(t, accountSessionID, "account", accountID, "text", "account")

	now := time.Now()
	oldJobID := h.insertJob(t, jobSeed{
		sessionID:        oldSessionID,
		requestMessageID: oldRequestID,
		ownerType:        "guest",
		ownerID:          oldGuestID,
		status:           "routing",
		expiresAt:        now.Add(1 * time.Hour),
		routingEndsAt:    now.Add(1 * time.Hour),
	})
	revokedJobID := h.insertJob(t, jobSeed{
		sessionID:        revokedSessionID,
		requestMessageID: revokedRequestID,
		ownerType:        "guest",
		ownerID:          revokedGuestID,
		status:           "assigned",
		expiresAt:        now.Add(1 * time.Hour),
		routingEndsAt:    now.Add(1 * time.Hour),
	})
	activeAssignedGuestJobID := h.insertJob(t, jobSeed{
		sessionID:            revokedSessionID,
		requestMessageID:     revokedRequestID,
		ownerType:            "guest",
		ownerID:              revokedGuestID,
		status:               "assigned",
		expiresAt:            now.Add(1 * time.Hour),
		routingEndsAt:        now.Add(1 * time.Hour),
		responderStakeAmount: h.cfg.ResponderStake,
		responderStakeStatus: "held",
	})
	activeAssignedGuestAssignmentID := h.insertAssignment(t, activeAssignedGuestJobID, accountID, responderID, now.Add(10*time.Minute), "active")
	activeJobID := h.insertJob(t, jobSeed{
		sessionID:               activeSessionID,
		requestMessageID:        activeRequestID,
		ownerType:               "guest",
		ownerID:                 activeGuestID,
		status:                  "system_pool",
		expiresAt:               now.Add(1 * time.Hour),
		routingEndsAt:           now.Add(1 * time.Hour),
		lastSystemPoolEnteredAt: ptrTime(now.Add(-10 * time.Second)),
	})
	activeClaimGuestJobID := h.insertJob(t, jobSeed{
		sessionID:               oldSessionID,
		requestMessageID:        oldRequestID,
		ownerType:               "guest",
		ownerID:                 oldGuestID,
		status:                  "system_pool",
		expiresAt:               now.Add(1 * time.Hour),
		routingEndsAt:           now.Add(1 * time.Hour),
		lastSystemPoolEnteredAt: ptrTime(now.Add(-10 * time.Second)),
		claimOwnerType:          ptrString("account"),
		claimOwnerID:            ptrString(responderID),
		claimExpiresAt:          ptrTime(now.Add(10 * time.Minute)),
		responderStakeAmount:    h.cfg.ResponderStake,
		responderStakeStatus:    "held",
	})
	accountJobID := h.insertJob(t, jobSeed{
		sessionID:        accountSessionID,
		requestMessageID: accountRequestID,
		ownerType:        "account",
		ownerID:          accountID,
		status:           "routing",
		expiresAt:        now.Add(1 * time.Hour),
		routingEndsAt:    now.Add(1 * time.Hour),
	})

	affected, err := h.svc.ProcessGuestExpiry(context.Background())
	if err != nil {
		t.Fatalf("ProcessGuestExpiry: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}
	if got := h.jobStatus(t, oldJobID); got != "expired" {
		t.Fatalf("old guest job status = %q, want %q", got, "expired")
	}
	if got := h.jobStatus(t, revokedJobID); got != "expired" {
		t.Fatalf("revoked guest job status = %q, want %q", got, "expired")
	}
	if got := h.jobStatus(t, activeAssignedGuestJobID); got != "assigned" {
		t.Fatalf("active assigned guest job status = %q, want %q", got, "assigned")
	}
	if got := h.assignmentStatus(t, activeAssignedGuestAssignmentID); got != "active" {
		t.Fatalf("active assigned guest assignment status = %q, want %q", got, "active")
	}
	if got := h.jobStatus(t, activeJobID); got != "system_pool" {
		t.Fatalf("active guest job status = %q, want %q", got, "system_pool")
	}
	if got := h.jobStatus(t, activeClaimGuestJobID); got != "system_pool" {
		t.Fatalf("active claim guest job status = %q, want %q", got, "system_pool")
	}
	if got := h.jobStatus(t, accountJobID); got != "routing" {
		t.Fatalf("account job status = %q, want %q", got, "routing")
	}
}

func TestServiceProcessAutoReview(t *testing.T) {
	t.Parallel()

	h := newServiceHarness(t, nil)
	ownerID := h.insertAccount(t, "tom")
	responderID := h.insertAccount(t, "noah")
	h.insertWallet(t, "account", ownerID, 1.0, nil)
	h.insertWallet(t, "account", responderID, 5.0, nil)
	sessionID := h.insertSession(t, "account", ownerID)
	requestID := h.insertMessage(t, sessionID, "account", ownerID, "text", "hello")
	replyID := h.insertMessage(t, sessionID, "account", responderID, "reply", "response")
	now := time.Now()

	dueJobID := h.insertJob(t, jobSeed{
		sessionID:            sessionID,
		requestMessageID:     requestID,
		ownerType:            "account",
		ownerID:              ownerID,
		status:               "assigned",
		expiresAt:            now.Add(1 * time.Hour),
		routingEndsAt:        now.Add(1 * time.Hour),
		responseMessageID:    &replyID,
		reviewDeadlineAt:     ptrTime(now.Add(-1 * time.Second)),
		responderStakeAmount: h.cfg.ResponderStake,
		responderStakeStatus: "held",
	})
	h.execSQL(t, `UPDATE jobs SET tip_amount = 0.6 WHERE id = $1`, dueJobID)
	h.execSQL(t, `UPDATE wallets SET balance = balance - $1 WHERE owner_type = 'account' AND owner_id = $2`, h.cfg.ResponderStake, responderID)
	futureJobID := h.insertJob(t, jobSeed{
		sessionID:         sessionID,
		requestMessageID:  requestID,
		ownerType:         "account",
		ownerID:           ownerID,
		status:            "assigned",
		expiresAt:         now.Add(1 * time.Hour),
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
	if got := h.jobStakeStatus(t, dueJobID); got != "returned" {
		t.Fatalf("due job stake status = %q, want %q", got, "returned")
	}
	if got := h.walletBalance(t, "account", responderID); got != 5.0 {
		t.Fatalf("responder balance = %v, want %v", got, 5.0)
	}
	if got := h.walletBalance(t, "account", ownerID); got != 1.6 {
		t.Fatalf("prompter balance = %v, want %v", got, 1.6)
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

	guestRefreshID := h.insertGuest(t, time.Now(), nil)
	accountRefreshID := h.insertAccount(t, "tom")
	guestAboveThresholdID := h.insertGuest(t, time.Now(), nil)
	accountTooRecentID := h.insertAccount(t, "noah")

	h.insertWallet(t, "guest", guestRefreshID, 0.5, &oldRefresh)
	h.insertWallet(t, "account", accountRefreshID, 4.0, &oldRefresh)
	h.insertWallet(t, "guest", guestAboveThresholdID, 3.0, &oldRefresh)
	h.insertWallet(t, "account", accountTooRecentID, 4.0, &recentRefresh)

	affected, err := h.svc.ProcessWalletRefresh(context.Background())
	if err != nil {
		t.Fatalf("ProcessWalletRefresh: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}
	if got := h.walletBalance(t, "guest", guestRefreshID); got != 5.0 {
		t.Fatalf("guest refresh balance = %v, want 5.0", got)
	}
	if got := h.walletBalance(t, "account", accountRefreshID); got != 25.0 {
		t.Fatalf("account refresh balance = %v, want 25.0", got)
	}
	if got := h.walletBalance(t, "guest", guestAboveThresholdID); got != 3.0 {
		t.Fatalf("guest above-threshold balance = %v, want 3.0", got)
	}
	if got := h.walletBalance(t, "account", accountTooRecentID); got != 4.0 {
		t.Fatalf("account too-recent balance = %v, want 4.0", got)
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
		DatabaseURL:              appURL,
		GuestTokenSecret:         "service-test-secret",
		AdminPathToken:           "service-test-admin",
		SignupPathToken:          "clawgrid-signup",
		WorkerTick:               time.Second,
		PostFee:                  2.0,
		ResponderPool:            1.4,
		ResponderStake:           0.6,
		DispatcherPool:           0.4,
		Sink:                     0.2,
		DispatchPenalty:          0.2,
		ResponderPenalty:         0.2,
		PrompterCancelPenalty:    0.2,
		GuestInitialBalance:      100,
		AccountInitialBalance:    100,
		RefreshInterval:          5 * time.Hour,
		GuestRefreshThreshold:    1,
		GuestRefreshTarget:       5,
		AccountRefreshThreshold:  5,
		AccountRefreshTarget:     25,
		GuestJobInactivityExpiry: 24 * time.Hour,
		RoutingWindow:            30 * time.Second,
		PoolDwellWindow:          30 * time.Second,
		JobExpiry:                24 * time.Hour,
		ReviewWindow:             24 * time.Hour,
		AssignmentDeadline:       30 * time.Minute,
		PollAssignmentWait:       30 * time.Second,
		ResponderActiveWindow:    12 * time.Second,
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

func (h *serviceHarness) insertGuest(t *testing.T, lastSeenAt time.Time, revokedAt *time.Time) string {
	t.Helper()
	id := domain.NewID("gst")
	h.execSQL(t, `INSERT INTO guest_sessions(id, guest_token_hash, last_seen_at, revoked_at) VALUES ($1, $2, $3, $4)`, id, domain.NewID("gk"), lastSeenAt, revokedAt)
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
  activated_at, expires_at, routing_ends_at, response_message_id,
  prompter_vote, review_deadline_at, routing_cycle_count,
  last_routing_entered_at, last_system_pool_entered_at,
  claim_owner_type, claim_owner_id, claim_expires_at,
  responder_stake_amount, responder_stake_status
) VALUES (
  $1, $2, $3, $4, $5, $6,
  now(), $7, $8, $9,
  $10, $11, $12,
  $13, $14,
  $15, $16, $17,
  $18, $19
)`,
		id,
		seed.sessionID,
		seed.requestMessageID,
		seed.ownerType,
		seed.ownerID,
		seed.status,
		seed.expiresAt,
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
