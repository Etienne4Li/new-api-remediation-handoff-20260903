# Local development data

This guide describes a safe data path for developing the customized interface
against the New API backend. The data created here is local development data.
It must not be presented as production usage, provider availability, pricing,
or performance.

## Start with SQLite

SQLite is the default when `SQL_DSN` is not set. For a repeatable local database,
set an explicit path in an ignored local-only directory:

```bash
mkdir -p .local-data
SQLITE_PATH="$PWD/.local-data/new-api-dev.db" \
SESSION_SECRET="replace-with-a-local-random-secret" \
go run .
```

The application runs migrations on startup. Open `http://localhost:3000` and
complete the first-run setup screen to create the Root account. Use a unique
local password with at least eight characters; do not reuse a production
credential. The setup endpoint is `GET/POST /api/setup`.

To reset a disposable local database, stop the server and remove only the
explicit development file:

```bash
rm .local-data/new-api-dev.db
```

Do not add real upstream API keys to a seed file or commit them to the
repository. Add a provider only after you have an account and key for that
provider, then configure it through the admin UI or an ignored environment file.
Until then, an empty channel/model list is the accurate state. The UI should
show an empty state rather than invented rankings, prices, or rate limits.

## Optional Docker development stack

When testing multi-service behavior, use the checked-in development compose
file. It provisions PostgreSQL and Redis with named Docker volumes:

```bash
docker compose -f docker-compose.dev.yml up -d
make dev-web
```

The backend is available at `http://localhost:3000`; the Rsbuild frontend is
available at `http://localhost:5173` and proxies API requests to the backend.
Complete `/api/setup` once, then create only the test users, channels, models,
and plans needed for the workflow under test. These compose credentials are
development defaults; replace them before sharing the stack or deploying it.

Stop the stack while preserving data with:

```bash
docker compose -f docker-compose.dev.yml down
```

Use `down -v` only when intentionally discarding the PostgreSQL/Redis volumes.

## Production data path

For a real New API deployment, use a managed PostgreSQL instance as the primary
database and managed Redis for shared cache/rate limiting. Set `SQL_DSN`,
`REDIS_CONN_STRING`, `SESSION_SECRET`, and (when running HTTPS)
`SESSION_COOKIE_SECURE=true` plus exact `SESSION_COOKIE_TRUSTED_URL` origins via
the deployment platform's secret manager. Keep database backups and rotation
outside the application container.

A production rollout should proceed in this order:

1. Provision PostgreSQL and Redis, and verify network/backup settings.
2. Start New API with the production environment variables and complete the
   first-run setup with the real administrator account.
3. Add verified upstream channels and model pricing manually (or through an
   audited import); supply provider keys through secrets, never source control.
4. Exercise login, key creation, relay, quota settlement, logs, billing, and
   admin permissions before opening access to users.

Do not connect the demo UI to public third-party endpoints merely to make the
dashboard look populated. Real metrics become meaningful only after the
deployment has its own authenticated channels and traffic.
