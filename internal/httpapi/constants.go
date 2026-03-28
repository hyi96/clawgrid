package httpapi

import "time"

const (
	maxDispatchRoutingJobs           = 4
	maxDispatchResponders            = 5
	maxDispatchBandItems             = 100
	dispatchJobsBandBase             = 24
	dispatchRespondersBandBase       = 25
	systemPoolBandBase               = 36
	dispatchActivityLookbackSeconds  = 120
	dispatchShuffleBucketSeconds     = 60
	maxSystemPoolCandidates          = 6
	maxAdminVisiblePoolJobs          = 20
	accountAPIKeyLimit               = 5
	responderDescriptionLimit        = 420
	sessionTitleLimit                = 120
	sessionMessagesLimitMax          = 500
	walletLedgerDefaultLimit         = 100
	walletLedgerLimitMax             = 500
	dispatchSnippetSourceRuneLimit   = 100000
	dispatchSnippetOutputRuneLimit   = 1000
	dispatchSnippetFragmentRuneLimit = 180
	rateLimitRetentionWindow         = 48 * time.Hour
	githubOAuthStateTTL              = 15 * time.Minute
	githubOAuthCompletionTTL         = 10 * time.Minute
	githubOAuthRetentionWindow       = 24 * time.Hour

	githubOAuthStartIPLimit = 20
	apiKeyCreateLimit       = 3
	claimAttemptLimit       = 20
	claimFailureLimit       = 10
	assignmentAttemptLimit  = 20
	assignmentFailureLimit  = 10
)

const (
	githubOAuthStartIPWindow = 10 * time.Minute
	apiKeyCreateWindow       = time.Hour
	claimAttemptWindow       = time.Minute
	claimFailureWindow       = time.Minute
	assignmentAttemptWindow  = time.Minute
	assignmentFailureWindow  = time.Minute
)
