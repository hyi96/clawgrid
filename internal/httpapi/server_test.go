package httpapi

import (
	"strings"
	"testing"
	"time"

	"clawgrid/internal/domain"
)

func TestAssignmentGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		dispatcherOwnerType string
		dispatcherOwnerID   string
		jobOwnerType        string
		jobOwnerID          string
		responderOwnerType  string
		responderOwnerID    string
		want                string
	}{
		{
			name:                "allows dispatcher own job with different responder",
			dispatcherOwnerType: "account",
			dispatcherOwnerID:   "acct_1",
			jobOwnerType:        "account",
			jobOwnerID:          "acct_1",
			responderOwnerType:  "account",
			responderOwnerID:    "acct_2",
			want:                "",
		},
		{
			name:                "blocks assigning self as responder",
			dispatcherOwnerType: "account",
			dispatcherOwnerID:   "acct_1",
			jobOwnerType:        "account",
			jobOwnerID:          "acct_3",
			responderOwnerType:  "account",
			responderOwnerID:    "acct_1",
			want:                "dispatcher_cannot_assign_self",
		},
		{
			name:                "blocks prompter as responder",
			dispatcherOwnerType: "account",
			dispatcherOwnerID:   "acct_9",
			jobOwnerType:        "account",
			jobOwnerID:          "acct_3",
			responderOwnerType:  "account",
			responderOwnerID:    "acct_3",
			want:                "prompter_cannot_be_responder",
		},
		{
			name:                "self guard wins when both self and prompter match",
			dispatcherOwnerType: "account",
			dispatcherOwnerID:   "acct_3",
			jobOwnerType:        "account",
			jobOwnerID:          "acct_3",
			responderOwnerType:  "account",
			responderOwnerID:    "acct_3",
			want:                "dispatcher_cannot_assign_self",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := assignmentGuard(
				tt.dispatcherOwnerType,
				tt.dispatcherOwnerID,
				tt.jobOwnerType,
				tt.jobOwnerID,
				tt.responderOwnerType,
				tt.responderOwnerID,
			)
			if got != tt.want {
				t.Fatalf("assignmentGuard() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildAccountStatsFeedbackRateUsesPrompterPerspective(t *testing.T) {
	t.Parallel()

	tom := buildAccountStats(
		15, // feedback given
		15, // replies received
		0,  // responder up
		0,  // responder down
		0,  // dispatch up
		0,  // dispatch down
		0,  // responses submitted
	)
	if got := tom["feedback_rate"]; got != "15 / 15" {
		t.Fatalf("tom feedback_rate = %v, want %q", got, "15 / 15")
	}

	noah := buildAccountStats(
		0,  // feedback given
		0,  // replies received
		15, // responder up
		0,  // responder down
		0,  // dispatch up
		0,  // dispatch down
		15, // responses submitted
	)
	if got := noah["feedback_rate"]; got != "n/a" {
		t.Fatalf("noah feedback_rate = %v, want %q", got, "n/a")
	}
	if got := noah["job_success_rate"]; got != "100.0%" {
		t.Fatalf("noah job_success_rate = %v, want %q", got, "100.0%")
	}
}

func TestSystemPoolVisibleToActor(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	actor := domain.Actor{OwnerType: domain.OwnerAccount, OwnerID: "acct_viewer"}

	tests := []struct {
		name           string
		jobOwnerType   string
		jobOwnerID     string
		claimOwnerType string
		claimOwnerID   string
		claimExpiresAt *time.Time
		want           bool
	}{
		{
			name:         "unclaimed foreign job is visible",
			jobOwnerType: "account",
			jobOwnerID:   "acct_other",
			want:         true,
		},
		{
			name:         "own job remains accessible as owner",
			jobOwnerType: "account",
			jobOwnerID:   "acct_viewer",
			want:         true,
		},
		{
			name:           "actively claimed by same actor is visible",
			jobOwnerType:   "account",
			jobOwnerID:     "acct_other",
			claimOwnerType: "account",
			claimOwnerID:   "acct_viewer",
			claimExpiresAt: ptrTime(now.Add(30 * time.Second)),
			want:           true,
		},
		{
			name:           "actively claimed by different actor is hidden",
			jobOwnerType:   "account",
			jobOwnerID:     "acct_other",
			claimOwnerType: "account",
			claimOwnerID:   "acct_someone_else",
			claimExpiresAt: ptrTime(now.Add(30 * time.Second)),
			want:           false,
		},
		{
			name:           "expired foreign claim reopens visibility",
			jobOwnerType:   "account",
			jobOwnerID:     "acct_other",
			claimOwnerType: "account",
			claimOwnerID:   "acct_someone_else",
			claimExpiresAt: ptrTime(now.Add(-30 * time.Second)),
			want:           true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := systemPoolVisibleToActor(
				tt.jobOwnerType,
				tt.jobOwnerID,
				tt.claimOwnerType,
				tt.claimOwnerID,
				tt.claimExpiresAt,
				actor,
				now,
			)
			if got != tt.want {
				t.Fatalf("systemPoolVisibleToActor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildSessionSnippetFromNewestFirst(t *testing.T) {
	t.Parallel()

	tooOld := strings.Repeat("x", dispatchSnippetSourceRuneLimit)
	got := buildSessionSnippetFromNewestFirstWithSourceTrimmed([]dispatchSnippetMessage{
		{Type: "text", Role: "prompter", Content: "latest prompt"},
		{Type: "text", Role: "responder", Content: "latest reply"},
		{Type: "feedback", Role: "responder", Content: "a responder cancelled the assigned job due to not a good fit"},
		{Type: "feedback", Role: "prompter", Content: "user rated reply as satisfactory"},
		{Type: "text", Role: "prompter", Content: tooOld},
	}, true)

	wantParts := []string{
		"prompter: latest prompt",
		"responder: latest reply",
	}
	for _, want := range wantParts {
		if !strings.Contains(got, want) {
			t.Fatalf("snippet = %q, want to contain %q", got, want)
		}
	}
	if strings.Contains(got, "user rated reply as satisfactory") {
		t.Fatalf("snippet = %q, should omit feedback lines", got)
	}
	if strings.Contains(got, "not a good fit") {
		t.Fatalf("snippet = %q, should omit responder cancel reasons", got)
	}
	if !strings.HasPrefix(got, "...") {
		t.Fatalf("snippet = %q, want older-context ellipsis prefix", got)
	}
}

func TestBuildSessionSnippetFromNewestFirstMergesConsecutivePrompterTurns(t *testing.T) {
	t.Parallel()

	got := buildSessionSnippetFromNewestFirst([]dispatchSnippetMessage{
		{Type: "text", Role: "prompter", Content: "second prompt"},
		{Type: "text", Role: "prompter", Content: "first prompt"},
	})

	if !strings.Contains(got, "prompter: first prompt / second prompt") {
		t.Fatalf("snippet = %q, want merged prompter turn", got)
	}
	if strings.Count(got, "prompter:") != 1 {
		t.Fatalf("snippet = %q, want exactly one prompter label", got)
	}
}

func TestBuildSessionSnippetFromNewestFirstKeepsOlderAnchor(t *testing.T) {
	t.Parallel()

	got := buildSessionSnippetFromNewestFirst([]dispatchSnippetMessage{
		{Type: "text", Role: "prompter", Content: "latest prompt"},
		{Type: "text", Role: "responder", Content: "latest reply"},
		{Type: "text", Role: "prompter", Content: "recent prompt"},
		{Type: "text", Role: "responder", Content: "recent reply"},
		{Type: "text", Role: "prompter", Content: "anchor prompt"},
		{Type: "text", Role: "responder", Content: "old dropped reply"},
	})

	if !strings.Contains(got, "prompter: anchor prompt") {
		t.Fatalf("snippet = %q, want older anchor prompt", got)
	}
	if strings.Contains(got, "old dropped reply") {
		t.Fatalf("snippet = %q, should omit older non-anchor turn", got)
	}
}

func TestBuildSessionSnippetFromNewestFirstFillsRecentBudgetWithManyTurns(t *testing.T) {
	t.Parallel()

	messages := make([]dispatchSnippetMessage, 0, 24)
	for i := 12; i >= 1; i-- {
		role := "responder"
		if i%2 == 1 {
			role = "prompter"
		}
		messages = append(messages, dispatchSnippetMessage{
			Type:    "text",
			Role:    role,
			Content: strings.Repeat("turn ", 10) + time.Date(2026, 3, i, 0, 0, 0, 0, time.UTC).Format("2006-01-02"),
		})
	}

	got := buildSessionSnippetFromNewestFirst(messages)

	if strings.Count(got, "| ") < 7 {
		t.Fatalf("snippet = %q, want many recent turns included", got)
	}
	if !strings.Contains(got, "2026-03-01") {
		t.Fatalf("snippet = %q, want older anchor turn included", got)
	}
}

func TestBuildSessionSnippetFromNewestFirstFallback(t *testing.T) {
	t.Parallel()

	if got := buildSessionSnippetFromNewestFirst(nil); got != "no messages yet" {
		t.Fatalf("snippet = %q, want %q", got, "no messages yet")
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}
