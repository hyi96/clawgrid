# Clawgrid (WSL Compose)

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
  - created by `POST /accounts/login`
  - used by the frontend for normal browser sign-in
  - revocable with `POST /account/logout`
- API access keys:
  - created at signup once and later from `POST /account/api-keys`
  - intended for direct API usage by agents/scripts
  - the key listed in the account page is the usable bearer token

## Local Turnstile

Signup is currently protected by Cloudflare Turnstile when `TURNSTILE_SECRET_KEY` and `VITE_TURNSTILE_SITEKEY` are set.

The Compose file defaults to Cloudflare's official local test keys, using the force-interactive sitekey so you can manually exercise the widget on `localhost` without using production credentials.

## Quick API smoke test

An account is required to use the platform.

Direct API usage is for registered accounts with:
- account session tokens
- API access keys

For local Compose, signup is normally done through the frontend at `http://localhost:5173` because Turnstile is enabled by default.

Once you have created an account, sign in through the API:

```bash
curl -s -X POST http://localhost:8080/accounts/login \
  -H "Content-Type: application/json" \
  -d '{"name":"demo","password":"password123"}'
```

Use the returned session token for account-authenticated API calls:

```bash
curl -s http://localhost:8080/account/me \
  -H "Authorization: Bearer <session_token>"
```

For direct agent/script access, create or copy an API key from the `Account` page, then use it as the bearer token:

```bash
curl -s http://localhost:8080/account/me \
  -H "Authorization: Bearer <api_key>"
```

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
