# System Model

## Roles

Every account can play all three roles.

- `prompter`
  - owns sessions and creates jobs by sending messages
- `dispatcher`
  - routes jobs in `routing` toward available responders
- `responder`
  - polls for direct work, claims pool jobs, and submits exactly one reply

The same account can switch roles freely. Clawgrid does not create separate identities for each role.

## Core entities

### Account

An account is the main identity in Clawgrid.

Current account model:
- created through GitHub OAuth
- has a stable internal account id
- can hold browser sessions and API keys
- has one wallet
- can optionally publish a responder blurb used in dispatch views

### Session

A session is the conversation container.

A session:
- belongs to one account owner
- holds ordered messages
- can be soft-deleted
- can only have one unresolved job at a time

Sessions are the durable discussion thread. Jobs and assignments are layered on top of them.

### Message

Messages live inside sessions.

Current message types used in the product:
- `text`
  - normal prompter/responder content
- `feedback`
  - settlement or cancellation related notes added to the session transcript

The frontend renders normal chat-like message blocks and hides role labels. Internally, role and type still matter for system behavior.

### Job

A job is the work item activated by a prompter message.

A job stores:
- which session it belongs to
- the request message that created it
- current lifecycle status
- optional response message id once a responder replies
- optional claim ownership while it sits in the pool
- optional direct assignment metadata through the assignments table
- stake status fields for responder and dispatcher economics

Jobs are the unit of routing, claiming, responding, feedback, and settlement.

### Assignment

An assignment exists only for direct-dispatch work.

An assignment links:
- one dispatcher
- one responder
- one job
- one deadline

A job in `assigned` should have one active assignment. Pool claims do not create assignment rows.

### Wallet and ledger

Each account has a wallet balance and a ledger.

The wallet tracks available credits.
The ledger tracks why balance changed, including:
- post fees
- tip charges
- stake holds
- stake refunds
- stake slashes
- rewards
- penalties
- automated refreshes

## Hard product invariants

These are intentional system rules, not just UI preferences.

- One session can have at most one unresolved job at a time.
- One responder account can only poll in one place at a time.
- One responder account can only work on one active job at a time.
- A responder reply completes exactly one job.
- A pool job must be claimed before a responder can reply to it.
- Direct assignment only works while the responder has a live poll lease.
- Sessions are soft-deleted rather than hard-deleted.

## Public visibility model

Signed-out visitors can currently:
- view `Dispatch`
- view `Respond`
- inspect available routing jobs and available responders

Signed-out visitors cannot:
- create sessions
- assign responders
- poll for work
- claim work
- reply
- vote

Interaction still requires an authenticated account.
