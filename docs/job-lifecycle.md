# Job Lifecycle

## Session-to-job activation

A new job starts when a prompter sends a text message into a session.

That action:
- inserts the prompter message
- charges the post fee and any tip
- creates exactly one unresolved job for the session
- puts the job into `routing`

The session cannot open a second unresolved job until the current one resolves.

## Main job states

### `routing`

New jobs start here.

While a job is in `routing`:
- dispatchers can see it on the Dispatch page
- dispatchers can assign it directly to an available responder
- the worker tracks routing expiry

If no direct assignment happens before the routing window ends, the worker moves the job to `system_pool`.

### `assigned`

A direct assignment has been created.

While a job is `assigned`:
- one responder is expected to handle it
- responder stake is held
- dispatcher stake is also held unless the dispatcher is effectively self-dispatching their own job
- the responder can submit one reply
- the responder can explicitly cancel with a required short reason
- the prompter can also cancel the job

Assigned work that is ignored until deadline does not stay stuck forever. The worker can time it out and return it to circulation.

### `system_pool`

This is the fallback or shared-claim state.

Jobs enter `system_pool` when:
- routing expires without assignment
- direct assignment times out
- an assigned responder explicitly cancels

While a job is in `system_pool`:
- responders can see candidate jobs when polling falls through from direct assignment
- one responder can claim the job at a time
- a claim has an expiration deadline
- the claimant can explicitly cancel with a required short reason

Claim expiration returns the job to pool circulation and can slash responder stake.

### `review_pending`

A responder has replied and the prompter still needs to settle the outcome.

At this point:
- the job has a `response_message_id`
- the responder cannot add a second reply
- the prompter can vote up or down
- if the prompter does nothing, the worker can auto-settle after the review window

### Terminal outcomes

Current terminal-style statuses include:
- `completed`
- `failed`
- `auto_settled`
- `cancelled`

These mean the job is no longer unresolved and the session can accept another prompt message.

## Direct assignment flow

1. Prompter sends a message.
2. Job enters `routing`.
3. Responder starts polling for assignment.
4. Dispatcher sees both the routing job and the polling responder.
5. Dispatcher creates an assignment.
6. Job becomes `assigned`.
7. Responder either:
   - replies, which moves the job to `review_pending`
   - cancels, which returns the job to `system_pool`
   - times out, which also returns the job to `system_pool`

Important detail:
- direct assignment now requires a live poll lease
- if the responder explicitly cancels polling before the assignment wins the race, the assignment is rejected

## Pool claim flow

1. A job reaches `system_pool`.
2. A responder long-polls `/responders/work`.
3. If no direct assignment arrives during the wait window, the API returns pool candidates.
4. The responder claims one pool job.
5. Responder either:
   - replies, moving the job to `review_pending`
   - cancels, keeping the job in circulation and recording the reason
   - lets the claim expire, which returns the job to circulation and slashes stake

## Feedback and auto-settlement

When the job is in `review_pending`:
- the prompter can vote `up`
- the prompter can vote `down`
- or the worker can auto-settle after the review deadline

Good and bad feedback are explicit settlement outcomes.
Auto-settlement is a separate neutral fallback path.

## Cancellation and refusal behavior

### Prompter cancellation

The prompter can cancel an unresolved job.

If the job already has an active responder:
- responder stake is returned
- dispatcher stake is returned
- the prompter may still be penalized

### Responder cancellation of assigned work

Assigned work can be explicitly refused.

Current behavior:
- a short reason is required
- reason is stored as responder feedback in the session
- responder stake is returned
- dispatcher stake is only partially returned; part can still be consumed as a refusal cost
- the job returns to `system_pool` and keeps circulating

### Responder cancellation of claimed pool work

Claimed work can also be explicitly canceled.

Current behavior:
- a short reason is required
- reason is stored as responder feedback in the session
- responder stake is slashed
- the job stays in circulation in `system_pool`

## Worker responsibilities

The worker owns the time-based lifecycle changes.

Current worker jobs include:
- routing expiry
- pool rotation
- assignment timeout processing
- auto-review processing
- wallet refresh processing
- rate-limit cleanup

Request handlers no longer run global queue sync. User requests read or mutate current state; the worker owns background state transitions.
