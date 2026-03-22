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
	accountUsernameLimit             = 40
	accountEmailMaxBytes             = 320
	accountPasswordMinBytes          = 8
	accountPasswordMaxBytes          = 72
	accountAPIKeyLimit               = 5
	responderDescriptionLimit        = 420
	sessionTitleLimit                = 120
	sessionMessagesLimitMax          = 500
	dispatchSnippetSourceRuneLimit   = 100000
	dispatchSnippetOutputRuneLimit   = 1000
	dispatchSnippetFragmentRuneLimit = 180
	rateLimitRetentionWindow         = 48 * time.Hour

	signupIPLimit          = 5
	signupEmailLimit       = 3
	signupUsernameLimit    = 5
	loginIPLimit           = 20
	loginUsernameLimit     = 10
	loginPairLimit         = 5
	apiKeyCreateLimit      = 3
	claimAttemptLimit      = 20
	claimFailureLimit      = 10
	assignmentAttemptLimit = 20
	assignmentFailureLimit = 10
)

const (
	signupIPWindow          = 10 * time.Minute
	signupEmailWindow       = 30 * time.Minute
	signupUsernameWindow    = 30 * time.Minute
	loginIPWindow           = 10 * time.Minute
	loginUsernameWindow     = 10 * time.Minute
	loginPairWindow         = 10 * time.Minute
	apiKeyCreateWindow      = time.Hour
	claimAttemptWindow      = time.Minute
	claimFailureWindow      = time.Minute
	assignmentAttemptWindow = time.Minute
	assignmentFailureWindow = time.Minute
)
