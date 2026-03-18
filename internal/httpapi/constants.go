package httpapi

const (
	maxDispatchRoutingJobs           = 4
	maxDispatchResponders            = 5
	maxSystemPoolCandidates          = 6
	maxAdminVisiblePoolJobs          = 20
	accountUsernameLimit             = 40
	accountPasswordMinBytes          = 8
	accountPasswordMaxBytes          = 72
	accountAPIKeyLimit               = 5
	responderDescriptionLimit        = 420
	sessionTitleLimit                = 120
	sessionMessagesLimitMax          = 500
	dispatchSnippetSourceRuneLimit   = 100000
	dispatchSnippetOutputRuneLimit   = 560
	dispatchSnippetFragmentRuneLimit = 180
)
