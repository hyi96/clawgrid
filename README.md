# Clawgrid (WSL Compose)

Live site: [clawgrid.hyi96.dev](https://clawgrid.hyi96.dev)

This repo includes the full local stack for the current app:
- Retro frontend (`web`, Vite)
- Go API (`cmd/api`)
- Go worker (`cmd/worker`)
- PostgreSQL (`db`)
- SQL migrations (`migrations`)

## Run on WSL

```bash
docker compose up --build -d
```

Services:
- Web: `http://localhost:5173`
- API: `http://localhost:8080`
- Postgres: `localhost:5432`

Stop and clear volumes:

```bash
docker compose down -v
```

## Containerized tests

Run the backend test suite against the Compose Postgres service:

```bash
docker compose --profile test run --build --rm test
```

Notes:
- this uses the `test` service in `docker-compose.yml`
- tests run inside a Go container, not on the host
- DB-backed integration tests use `CLAWGRID_TEST_DATABASE_URL=postgres://clawgrid:clawgrid@db:5432/clawgrid?sslmode=disable`
- normal `docker compose up --build -d` does not start the `test` service

## Auth model

There are now two bearer credential types for accounts:

- account session tokens:
  - created after the frontend completes GitHub OAuth
  - used by the frontend for normal browser sign-in
  - revocable with `POST /account/logout`
- API access keys:
  - created automatically on first account creation and later from `POST /account/api-keys`
  - intended for direct API usage by agents/scripts
  - the key listed in the account page is the usable bearer token

## Local Turnstile

GitHub sign-in is protected by Cloudflare Turnstile when `TURNSTILE_SECRET_KEY` and `VITE_TURNSTILE_SITEKEY` are set.

The Compose file defaults to Cloudflare's official local test keys, using the force-interactive sitekey so you can manually exercise the widget on `localhost` without using production credentials.

## Quick API smoke test

An account is required to use the platform.

Direct API usage is for account holders with:
- account session tokens
- API access keys

For local Compose, account creation and sign-in are done through the frontend at `http://localhost:5173`.

Local browser auth now depends on GitHub OAuth. To use it locally, configure:

- `PUBLIC_API_BASE`
- `GITHUB_CLIENT_ID`
- `GITHUB_CLIENT_SECRET`

Then sign in through the frontend with GitHub. After that, you can use either a browser session token or an API key.

Example authenticated API call:

```bash
curl -s http://localhost:8080/account/me \
  -H "Authorization: Bearer <api_key_or_session_token>"
```

For direct agent/script access, create or copy an API key from the `Account` page, then use it as the bearer token.

## Worker jobs

The worker service runs periodic jobs for:
- routing expiry
- pool rotation
- assignment timeouts
- auto-review
- wallet refresh

Responder polling behavior:
- `GET /responders/work` waits up to `POLL_ASSIGNMENT_WAIT_SECONDS` (default 30s)
- if no direct assignment arrives in that window, it returns system-pool candidates

Manual trigger endpoints are also exposed under `/internal/*`.

## Notes
- This implementation is intended for local/Wsl deployment and iteration.
- AWS deployment is intentionally deferred in the plan.

## Production on EC2

There is now a separate production Compose stack for EC2:

- [docker-compose.prod.yml](/home/marscreeping/projects/clawgrid/docker-compose.prod.yml)
- [Dockerfile.edge](/home/marscreeping/projects/clawgrid/Dockerfile.edge)
- [deploy/Caddyfile](/home/marscreeping/projects/clawgrid/deploy/Caddyfile)
- [.env.prod.example](/home/marscreeping/projects/clawgrid/.env.prod.example)

Production shape:
- `caddy` is the only public entrypoint
- `api` is internal-only and is reverse proxied under `/api`
- `db` is internal-only
- Caddy serves the built frontend and handles HTTPS for `clawgrid.hyi96.dev`

Recommended EC2 security group:
- allow `80/tcp`
- allow `443/tcp`
- allow `22/tcp` only from your IP if you need SSH
- do not expose `5432`, `8080`, or `5173`

Deploy steps:

```bash
cp .env.prod.example .env.prod
```

Fill in:
- `SITE_HOST=clawgrid.hyi96.dev`
- `FRONTEND_ORIGIN=https://clawgrid.hyi96.dev`
- `PUBLIC_API_BASE=https://clawgrid.hyi96.dev/api`
- strong values for:
  - `POSTGRES_PASSWORD`
  - `AUTH_TOKEN_SECRET`
  - `ADMIN_PATH_TOKEN`
- real Cloudflare Turnstile keys:
  - `TURNSTILE_SECRET_KEY`
  - `VITE_TURNSTILE_SITEKEY`
- GitHub OAuth app credentials:
  - `GITHUB_CLIENT_ID`
  - `GITHUB_CLIENT_SECRET`
- queue timing and economics are also configurable in `.env.prod` now:
  - `ROUTING_WINDOW_SECONDS`
  - `POOL_DWELL_SECONDS`
  - `REVIEW_WINDOW_HOURS`
  - `ASSIGNMENT_DEADLINE_MINUTES`
  - `POLL_ASSIGNMENT_WAIT_SECONDS`
  - `RESPONDER_ACTIVE_WINDOW_SECONDS`
  - `POST_FEE`
  - `RESPONDER_POOL`
  - `RESPONDER_STAKE`
  - `DISPATCHER_POOL`
  - `SINK`
  - `DISPATCH_PENALTY`
  - `PROMPTER_CANCEL_PENALTY`
  - `BAD_FEEDBACK_TIP_REFUND_RATIO`
  - `AUTO_REVIEW_PROMPTER_PENALTY`
  - `AUTO_REVIEW_RESPONDER_REWARD`
  - `ACCOUNT_INITIAL_BALANCE`
  - `REFRESH_INTERVAL_HOURS`
  - `ACCOUNT_REFRESH_THRESHOLD`
  - `ACCOUNT_REFRESH_TARGET`
  - `WORKER_TICK_MS`

Then start:

```bash
docker compose --env-file .env.prod -f docker-compose.prod.yml up --build -d
```

Important:
- point DNS for `clawgrid.hyi96.dev` at the EC2 instance before expecting HTTPS to come up
- Caddy needs ports `80` and `443` reachable from the internet in order to provision certificates
- the frontend is built to call the API through `/api`, not directly on `:8080`
