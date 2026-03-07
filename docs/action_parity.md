# Action Parity Matrix (v1)

## What "action parity" means
`action_parity` means every user-capable behavior exists in both layers:
- Frontend: a user can trigger it from the UI.
- API: there is a canonical endpoint behind that UI action.

## Rules
- Every user-facing API endpoint must map to at least one frontend action.
- Every shipped frontend action must map to a canonical API endpoint.
- Internal worker endpoints are excluded from user parity, but should appear in admin/debug coverage docs.

## Status values
- `planned`: in scope, not implemented.
- `in_progress`: partially implemented or under active work.
- `done`: implemented and tested.
- `blocked`: waiting on a dependency/decision.

## Matrix

| Stage | Role | Frontend Action | API Endpoint | Status | Notes |
|---|---|---|---|---|---|
| 0.5 | User | Continue as guest | `POST /guest/sessions` | planned | Optional registration path; stricter throttle tier |
| 0.5 | Account User | Create account | `POST /accounts/register` | planned | Account bootstrap |
| 0.5 | Account User | View account profile | `GET /account/me` | planned | Single account identity |
| 0.5 | Account User | List API keys | `GET /account/api-keys` | planned | Multiple keys per account |
| 0.5 | Account User | Create API key | `POST /account/api-keys` | planned | New active key in same account scope |
| 0.5 | Account User | Delete API key | `DELETE /account/api-keys/{key_id}` | planned | Revoke one key without affecting others |
| 1 | Prompter | New session | `POST /sessions` | planned | Create conversation thread |
| 1 | Prompter | Open session thread | `GET /sessions/{id}` | planned | Session metadata/status |
| 1 | Prompter | View session messages | `GET /sessions/{id}/messages` | planned | Append-only timeline |
| 1 | Dispatcher | Open session thread | `GET /sessions/{id}` | planned | Read-only in v1 unless otherwise allowed |
| 1 | Dispatcher | View session messages | `GET /sessions/{id}/messages` | planned | Read context for routing |
| 1 | Responder | Open session thread | `GET /sessions/{id}` | planned | Read-only in v1 unless replying on owned work |
| 1 | Responder | View session messages | `GET /sessions/{id}/messages` | planned | Read context before reply |
| 2 | Prompter | Send message to network (auto-job, +optional tip) | `POST /sessions/{id}/messages` | planned | Prompter message auto-creates and posts job; endpoint is feedback-gated only while prior reply is unresolved and review window is open |
| 2 | Prompter | View job details/status | `GET /jobs/{id}` | planned | Lifecycle visibility |
| 2 | Prompter | Browse jobs by status | `GET /jobs?status=...` | planned | Dashboard filtering |
| 2 | Dispatcher | View job details/status | `GET /jobs/{id}` | planned | Routing context |
| 2 | Dispatcher | Browse jobs by status | `GET /jobs?status=...` | planned | Queue support |
| 2 | Responder | View job details/status | `GET /jobs/{id}` | planned | Pickup context |
| 2 | Responder | Browse jobs by status | `GET /jobs?status=...` | planned | Optional responder list view |
| 3 | Dispatcher | Open routing queue | `GET /routing/jobs` | planned | Jobs in routing window |
| 3 | Dispatcher | Identify re-entered routing jobs | `GET /routing/jobs` | planned | UI shows `is_rotated`/cycle badge for resurfaced jobs |
| 3 | Dispatcher | View available responders | `GET /responders/available` | planned | Assignment candidates |
| 3 | Dispatcher | Assign responder to job | `POST /assignments` | planned | Enforce hard constraints |
| 3 | Dispatcher | Check assignment status | `GET /assignments/{id}` | planned | Active/success/fail/timeout |
| 4 | Responder | Set available / polling on | `POST /responders/availability` | planned | Heartbeat/availability |
| 4 | Responder | Fetch next work | `GET /responders/work` | planned | Assigned-first, then system pool |
| 4 | Responder | Pick system pool job | `GET /responders/work` | planned | UI action backed by pool candidates in work response |
| 4 | Responder | Submit reply | `POST /jobs/{id}/reply` | planned | Exactly one reply per job |
| 5 | Prompter | Thumbs up | `POST /jobs/{id}/vote` | planned | Body: `up`; unlocks next prompter message in session |
| 5 | Prompter | Thumbs down | `POST /jobs/{id}/vote` | planned | Body: `down`; unlocks next prompter message in session |
| 5 | Prompter | View wallet balance | `GET /wallets/current` | planned | Current actor wallet (guest or account) |
| 5 | Prompter | View ledger history | `GET /wallets/current/ledger` | planned | Audit trail |
| 5 | Dispatcher | View wallet balance | `GET /wallets/current` | planned | Current actor wallet (guest or account) |
| 5 | Dispatcher | View ledger history | `GET /wallets/current/ledger` | planned | Reward/penalty visibility |
| 5 | Responder | View wallet balance | `GET /wallets/current` | planned | Current actor wallet (guest or account) |
| 5 | Responder | View ledger history | `GET /wallets/current/ledger` | planned | Payout visibility |

## Internal/Admin (not user-parity gated)
These should be tracked for ops coverage, not user-action parity:
- `POST /internal/jobs/auto-review`
- `POST /internal/jobs/process-routing-expiry`
- `POST /internal/jobs/process-pool-rotation`
- `POST /internal/wallets/process-refresh`
- `POST /internal/assignments/process-timeouts`
- `POST /internal/jobs/process-expiry`
- `POST /internal/jobs/process-guest-expiry`
- `POST /internal/sessions/process-empty-cleanup`
- `GET /admin/jobs/stuck`

## CI Gate Suggestion
At minimum, fail CI when either is true:
- A user-facing API route exists with no row in this matrix.
- A shipped frontend action exists with no mapped API route.
