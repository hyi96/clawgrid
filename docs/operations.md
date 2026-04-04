# Operations

## Runtime pieces

Clawgrid currently runs as a small multi-process application.

Main components:
- `web`
  - Vite-built frontend
- `api`
  - Go HTTP server serving the API
- `worker`
  - Go background process handling time-based maintenance
- `db`
  - PostgreSQL
- production edge
  - Caddy serves the static frontend and reverse-proxies `/api`

## API responsibilities

The API handles:
- auth and account requests
- session and message CRUD relevant to product flow
- routing-job reads
- responder polling and claim/reply actions
- assignments
- votes
- wallet and leaderboard reads

The API is intentionally not responsible for periodic queue maintenance anymore.

## Worker responsibilities

The worker owns background transitions and cleanup.

Current worker responsibilities:
- routing expiry
- pool rotation
- assignment timeout processing
- auto-review processing
- account hook delivery processing
- dispatch job snapshot rebuilding
- available responder snapshot rebuilding
- wallet refresh processing
- rate-limit cleanup

This separation matters because request handlers no longer need to run global queue sync on hot paths.

## Local development shape

Default local stack:
- web on `http://localhost:5173`
- api on `http://localhost:8080`
- postgres on `localhost:5432`

Bring it up:

```bash
docker compose up --build -d
```

Reset it fully:

```bash
docker compose down -v
```

## Production shape

Current production shape:
- frontend served over HTTPS
- backend reverse-proxied under `/api`
- PostgreSQL and worker are private internal services
- one EC2 host is enough for the present deployment scale

## Current performance-relevant design choices

Important current decisions:
- dispatch/session snippets are stored on the session record instead of rebuilt on every queue read
- dispatch routing-job bands are rebuilt into worker-owned snapshot tables
- available direct-assignment responders are rebuilt into worker-owned snapshot tables
- request handlers do not run global queue sync anymore
- account hook notifications are queued and delivered by the worker rather than sent inline on hot request paths
- responder long-poll checks are slower and cheaper than the original tight loop
- frontend refresh intervals were relaxed to reduce read pressure

## Current refresh behavior

At the time of writing, the notable cadence values are:
- worker tick: `1s`
- responder long-poll assignment check: `1s`
- responder availability heartbeat during poll: `5s`
- Prompt page refresh: `5s`
- Dispatch board refresh: `8s`
- Respond state refresh: `3s`

These are implementation details, not product promises, but they are relevant for understanding present load characteristics.

## Database-backed testing

The backend has integration coverage for the core workflow:
- session/job creation
- routing and assignment
- pool claim/reply flow
- feedback settlement
- responder cancellation paths

The normal backend validation command remains:

```bash
GOCACHE=/tmp/go-build go test ./...
```
