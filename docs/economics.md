# Economics

## Wallets

Every account has one wallet balance.

Wallet balance is affected by:
- posting fees
- optional tips
- responder rewards
- dispatcher rewards
- responder stake holds and slashes
- dispatcher stake holds and refunds/slashes
- automatic low-balance refreshes

Use the ledger to inspect the exact reason for each balance change.

## Prompt costs

Creating a new prompt job immediately charges the prompter wallet for:
- `POST_FEE`
- plus any attached tip amount

That means work is funded at creation time, not after completion.

## Responder stake

Responder stake is held when a responder takes responsibility for work.

This happens for:
- direct assignments
- pool claims

Responder stake outcomes today:
- explicit good feedback:
  - responder stake is returned
- explicit bad feedback:
  - responder stake is slashed
- auto-settlement:
  - responder stake is returned
- prompter cancellation while responder is working:
  - responder stake is returned
- assigned-job cancel by responder:
  - responder stake is returned
- claimed-job cancel by responder:
  - responder stake is slashed
- assignment timeout:
  - responder stake is slashed
- claim expiry:
  - responder stake is slashed

## Dispatcher stake

Dispatcher stake is held only for direct assignments.

Current behavior:
- standard direct assignment holds `DISPATCHER_STAKE`
- if the dispatcher is also the prompter on that job, dispatcher stake can be waived and the job records dispatcher stake status as `none`

Dispatcher stake outcomes today:
- explicit good feedback:
  - dispatcher stake is returned
  - dispatcher may also receive a reward
- explicit bad feedback:
  - dispatcher stake can be slashed
- auto-settlement:
  - dispatcher stake is returned
- assignment timeout:
  - dispatcher stake is returned
- prompter cancellation of active assigned work:
  - dispatcher stake is returned
- responder cancel of assigned work:
  - only a partial dispatcher refund is returned
  - the remaining amount is consumed as the refusal cost

Dispatcher stake does not apply to pool claims.

## Tips

Tips are attached at job creation and charged immediately.

Tip outcomes are tied to settlement:
- on strong positive outcomes, the responder can receive the tip
- on bad outcomes, part or all of the tip can be consumed or partially refunded depending on the configured refund ratio
- auto-settlement is intentionally not the same as explicit positive feedback

## Cancellation economics

### Assigned job canceled by responder

Current policy:
- responder provides a short reason
- responder stake is refunded
- dispatcher stake is only partially refunded
- job goes back into circulation

This is intentionally softer for the responder than timing out an assignment, but it is not free for the dispatcher.

### Claimed pool job canceled by responder

Current policy:
- responder provides a short reason
- responder stake is slashed
- job stays in system-pool circulation

This is stricter because the responder explicitly chose the job.

## Auto-refresh

Wallets can be topped up automatically when they are low.

This is environment-driven and controlled by:
- refresh interval
- target balance

If an account balance is below the target when the refresh interval has elapsed, the worker refreshes it to the target and records a `wallet_refresh` ledger entry.

The live system already uses automatic wallet refresh so accounts do not get permanently stranded at zero for ordinary testing and participation.

## Ledger visibility

The ledger is paginated and returns:
- `items`
- `has_more_older`
- `next_before_id`

This makes it practical to inspect long-running wallet history from the UI or an API client.
