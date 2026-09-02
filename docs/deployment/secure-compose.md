# Hardened Docker Compose deployment

`docker-compose.secure.yml` is a production-oriented baseline. The original
`docker-compose.yml` remains a development quick-start and intentionally keeps
its compatibility defaults; do not use it on an internet-facing host.

The hardened file applies these boundaries:

- database and Redis are reachable only on an internal Docker network;
- Redis disables the default ACL user, requires a dedicated password, enables
  AOF/RDB recovery, and has a bounded memory budget (`volatile-lru` only evicts
  expiring cache entries).  The application ACL is an explicit command
  allow-list rather than broad `@read`/`@write` categories; if a future release
  adds a Redis command, review and extend the allow-list in the Compose file
  deliberately.  Lua callers use `EVALSHA` with an `EVAL` fallback, so the
  allow-list does not grant `SCRIPT LOAD`; this keeps rate limiting functional
  after a Redis restart, which clears the in-memory script cache;
- the API runs as an unprivileged UID with a read-only root filesystem,
  dropped capabilities, `no-new-privileges`, CPU/memory/PID limits, and log
  rotation; application files are capped by size/count and old database rows
  are removed by a bounded, resumable maintenance task;
- all three services have health checks and graceful shutdown periods;
- image references must be supplied as immutable `image@sha256:<digest>` values;
- credentials are mounted as Docker secrets and consumed through `*_FILE`
  variables, so they do not appear in the service environment or `docker
  inspect` output.

Run the side-effect-free validator before `docker compose up`:

```bash
bash scripts/verify-secure-compose.sh
```

It rejects mutable image tags, missing/weakly protected secret files, and
symlinked secret paths. Signature and attestation verification can be enabled
after installing a current `cosign` release with OCI Sigstore bundle support
and `jq` for signed predicate inspection:

```bash
VERIFY_SIGNATURES=1 bash scripts/verify-secure-compose.sh
```

The default certificate identity pattern trusts this repository's GitHub
Actions workflow. Set `COSIGN_IDENTITY_REGEXP` to the exact release workflow
identity used by a fork or an internal builder; do not remove issuer and
identity checks. The validator always checks the application signature and its
signed SLSA v1 provenance and SPDX 2.3 SBOM attestations. Set
`VERIFY_DEPENDENCY_SIGNATURES=1` plus
`COSIGN_DEPENDENCY_IDENTITY_REGEXP` only after confirming the Redis/PostgreSQL
publisher's signing policy; their immutable digests are still mandatory when
that optional check is not available.

## Prepare secret files

Keep the secret directory outside the source tree (for example,
`/etc/new-api/secrets`) and make it readable only by root:

```bash
umask 077
secret_dir=/etc/new-api/secrets
install -d -m 700 "$secret_dir"

pg_password=$(openssl rand -hex 32)
redis_password=$(openssl rand -hex 32)
openssl rand -hex 32 > "$secret_dir/session_secret"
openssl rand -hex 32 > "$secret_dir/crypto_secret"
printf '%s' "$pg_password" > "$secret_dir/postgres_password"
printf '%s' "$redis_password" > "$secret_dir/redis_password"
printf 'postgresql://newapi:%s@postgres:5432/new-api?sslmode=disable' "$pg_password" > "$secret_dir/sql_dsn"
printf 'redis://newapi:%s@redis:6379/0' "$redis_password" > "$secret_dir/redis_conn_string"
chmod 600 "$secret_dir"/*
```

`crypto_secret` is the durable root key for credentials encrypted at rest as
well as keyed cache/fingerprint values. Back it up separately from the live
host and keep the exact same value on every node that shares the database.
Do not regenerate or rotate it on an existing database as part of an ordinary
service restart: the current `enc:v1` envelope has no key identifier or
fallback keyring, so changing this file makes existing provider keys, user
tokens, OAuth/2FA secrets, and sensitive options unreadable. A future rotation
requires a separately tested offline re-encryption migration and a protected
copy of the previous key until verification completes.

The Redis startup rule accepts only passwords containing at least 32
characters from `[A-Za-z0-9._~-]`; the hex value above is deliberately chosen
to avoid ACL quoting and URL-encoding mistakes. If an existing credential uses
other characters, URL-encode it in the connection string and migrate it to a
safe generated value before using this file.

The API bind-mounted directories must be writable by UID/GID `10001` used by
the API container:

```bash
install -d -m 700 /var/lib/new-api/data /var/log/new-api
chown 10001:10001 /var/lib/new-api/data /var/log/new-api
```

## Pin images and start

The first upgrade from a build that stores plaintext credentials is a
maintenance-window upgrade, not a mixed-version rolling upgrade. Drain and
stop every old API instance before starting the first new instance. Startup
replaces historical key/PAT columns with non-reversible fingerprint markers;
an old binary can neither authenticate those markers correctly nor safely
write alongside the new encrypted representation. Multiple instances of the
new build may start against the same database after all old instances stop.

Resolve and review digests using a trusted registry (for example,
`docker buildx imagetools inspect`), then export the references. Do not replace
them with mutable `:latest` tags:

```bash
export NEW_API_IMAGE='calciumion/new-api@sha256:<reviewed-digest>'
export NEWAPI_POSTGRES_IMAGE='postgres:15-alpine@sha256:<reviewed-digest>'
export NEWAPI_REDIS_IMAGE='redis:7-alpine@sha256:<reviewed-digest>'
export NEWAPI_SECRET_DIR=/etc/new-api/secrets
export NEWAPI_DATA_DIR=/var/lib/new-api/data
export NEWAPI_LOG_DIR=/var/log/new-api
export RETENTION_CLEANUP_ENABLED=true
export LOG_RETENTION_DAYS=30
export SYSTEM_TASK_RETENTION_DAYS=30
export RETENTION_CLEANUP_INTERVAL_HOURS=24
export RETENTION_CLEANUP_BATCH_SIZE=100
export RETENTION_CLEANUP_MAX_BATCHES=100
export LOG_MAX_SIZE_MB=100
export LOG_MAX_FILES=30

bash scripts/verify-secure-compose.sh
docker compose -f docker-compose.secure.yml config
docker compose -f docker-compose.secure.yml up -d
docker compose -f docker-compose.secure.yml ps
```

The `config` step must succeed before any container is started. It also makes
it easy to review the rendered topology; verify that no credential value is
present in its output.

For a release built by the bundled GitHub Actions workflows, the per-platform
BuildKit provenance and SBOM records remain embedded in the image index. The
workflow also generates keyless-signed SLSA v1 provenance and separately named
amd64/arm64 SPDX 2.3 SBOM attestations for the final multi-architecture digest.
The bundled verifier checks both signed SPDX document names rather than merely
accepting whichever platform attestation happens to be returned first. Verify
the image signature plus the signed provenance and SBOM attestations against
that exact digest before exporting it:

```bash
IMAGE='calciumion/new-api@sha256:<reviewed-digest>'
cosign verify \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --certificate-identity-regexp 'https://github.com/QuantumNous/new-api/.github/workflows/.*@refs/(tags|heads)/.*' \
  "$IMAGE"
cosign verify-attestation --type https://slsa.dev/provenance/v1 \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --certificate-identity-regexp 'https://github.com/QuantumNous/new-api/.github/workflows/.*@refs/(tags|heads)/.*' \
  "$IMAGE"
cosign verify-attestation --type https://spdx.dev/Document/v2.3 \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --certificate-identity-regexp 'https://github.com/QuantumNous/new-api/.github/workflows/.*@refs/(tags|heads)/.*' \
  "$IMAGE"
```

Store the resolved digest, source commit, workflow run, SBOM, and provenance
alongside the deployment record. A tag or `latest` value by itself is not an
acceptable rollback or audit reference.

## Retention and host-level log controls

`RETENTION_CLEANUP_ENABLED` is deliberately explicit. When enabled, the master
node removes only terminal `system_tasks` rows and old application log rows,
using bounded primary-key batches that work on SQLite, MySQL, PostgreSQL, and
ClickHouse log stores. Pending/running tasks are never deleted. Choose values
that satisfy the jurisdiction's audit and privacy requirements; the 30-day
values above are examples, not a legal recommendation. Each run is capped by
`RETENTION_CLEANUP_MAX_BATCHES` and records whether more work remains, so a
large backlog cannot monopolize the database.

The application cannot rotate host journald or Docker build cache. Configure
those independently and monitor their usage. Example systemd policy (review
before applying on a shared host):

```bash
# Keep 30 days of host journal data; back up records needed for investigations first.
sudo journalctl --vacuum-time=30d

# Remove only build cache unused for 7 days. Inspect first with `docker builder du`.
docker builder prune --filter until=168h
```

The secure Compose services already cap Docker `json-file` logs at 10 MiB × 5
files. Keep `/var/log/new-api` and `/var/lib/new-api/data` on monitored
filesystems, alert before 70/85/95% utilization, and archive encrypted copies
before deleting records required for incident or billing investigations. Do
not run an unfiltered `docker system prune` on a production host.

All secure services carry `com.newapi.stack=secure` and
`com.newapi.environment=production` labels. Use those labels when inspecting
or cleaning this stack; never target containers by a broad name/glob that
could include an unrelated workload.

## Database and transport notes

The bundled PostgreSQL service is suitable for a new isolated stack and uses a
dedicated `newapi` role. For an existing production database, create separate
migration and runtime roles: run migrations during a controlled release with a
short-lived DDL-capable role, then run the API with a role limited to the DML
privileges it needs. Prefer PostgreSQL TLS (`sslmode=verify-full`) or a managed
private endpoint; the example `sslmode=disable` is only for the isolated local
Compose network.

For MySQL deployments, do not reuse `root` in `SQL_DSN`. Remove `root@%`, bind
MySQL to the private database network, require TLS where supported, and grant
the runtime account only the required schema privileges. These server-side
changes cannot be performed safely by this repository file and require an
operator-run migration with a backup and rollback plan.

The API still needs an egress-capable network to call upstream providers; only
the database/Redis `backend` network is marked `internal`. Put Nginx or another
TLS-terminating proxy on the `edge` side and enforce request-body, rate, and
connection limits there as well. A deny-by-default virtual-host example is in
[`nginx-secure.conf`](nginx-secure.conf): it rejects the default site, keeps
bootstrap/status metadata off the public internet, and rebuilds forwarding
headers. Treat the sample monitoring address and certificate paths as
placeholders, and run `nginx -t` before reloading.

Do not leave old smoke containers or temporary Compose projects running on the
host. Give test stacks a distinct project name/label (for example,
`com.newapi.environment=smoke`) and remove only that explicitly labelled
project after collecting its logs. Keep the production stack's immutable image
and deployment record; do not use an unfiltered `docker rm`, `docker system
prune`, or wildcard container name.

## Rotation and rollback

To rotate a database or Redis password, write a new mode-0600 file, update any
DSN that embeds the credential, and recreate the affected services during a
maintenance window. This procedure does not apply to `crypto_secret`; follow
the at-rest encryption warning above.

```bash
docker compose -f docker-compose.secure.yml up -d --force-recreate redis postgres new-api
docker compose -f docker-compose.secure.yml ps
```

Keep the previous secret in a separately protected recovery location until
health checks and application authentication have been verified. Back up and
test restoration of `redis_data`, `postgres_data`, and the API data/log paths
before changing image digests or database roles. Never commit the secret
directory or rendered Compose output.
