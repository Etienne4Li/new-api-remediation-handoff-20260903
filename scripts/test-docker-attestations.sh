#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'Docker attestation contract test failed: %s\n' "$1" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local pattern="$2"
  local description="$3"
  grep -Eq -- "$pattern" "$file" || fail "$file does not $description"
}

for workflow in .github/workflows/docker-build.yml .github/workflows/docker-image-branch.yml; do
  assert_contains "$workflow" '^          provenance: mode=max$' 'enable BuildKit provenance'
  assert_contains "$workflow" '^          sbom: true$' 'enable BuildKit SBOM generation'
  assert_contains "$workflow" '^        id: manifest$' 'expose the final manifest digest'
  assert_contains "$workflow" '^          subject-name: index\.docker\.io/calciumion/new-api$' 'use a fully-qualified attestation subject'
  assert_contains "$workflow" '^          subject-digest: \$\{\{ steps\.manifest\.outputs\.digest \}\}$' 'bind attestations to the final manifest digest'
  assert_contains "$workflow" '^          push-to-registry: true$' 'publish signed attestations to the registry'
  assert_contains "$workflow" "--source-name 'index\.docker\.io/calciumion/new-api#linux/amd64'" 'name the amd64 SPDX document explicitly'
  assert_contains "$workflow" "--source-name 'index\.docker\.io/calciumion/new-api#linux/arm64'" 'name the arm64 SPDX document explicitly'
  assert_contains "$workflow" '--output spdx-json=sbom-amd64\.spdx\.json' 'generate an amd64 SPDX SBOM'
  assert_contains "$workflow" '--output spdx-json=sbom-arm64\.spdx\.json' 'generate an arm64 SPDX SBOM'
  assert_contains "$workflow" 'cosign sign --yes "index\.docker\.io/calciumion/new-api@\$\{\{ steps\.manifest\.outputs\.digest \}\}"' 'sign the final immutable digest'

  attestation_count="$(grep -Ec '^        uses: actions/attest@' "$workflow")"
  [[ "$attestation_count" == "3" ]] || fail "$workflow must publish one provenance and two platform SBOM attestations"
done

test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT
secret_dir="$test_root/secrets"
mock_bin="$test_root/bin"
cosign_log="$test_root/cosign.log"
mkdir -p "$secret_dir" "$mock_bin"
chmod 700 "$secret_dir"

for secret_name in session_secret crypto_secret sql_dsn redis_conn_string redis_password postgres_password; do
  printf 'test-secret\n' > "$secret_dir/$secret_name"
  chmod 600 "$secret_dir/$secret_name"
done

cat > "$mock_bin/cosign" <<'MOCK_COSIGN'
#!/usr/bin/env bash
set -euo pipefail
printf 'CALL' >> "$COSIGN_LOG"
printf '\t%s' "$@" >> "$COSIGN_LOG"
printf '\n' >> "$COSIGN_LOG"
if [[ -n "${COSIGN_FAIL_TYPE:-}" ]]; then
  previous=''
  for argument in "$@"; do
    if [[ "$previous" == '--type' && "$argument" == "$COSIGN_FAIL_TYPE" ]]; then
      exit 1
    fi
    previous="$argument"
  done
fi
if [[ " $* " == *' https://spdx.dev/Document/v2.3 '* ]]; then
  printf '%s\n' '{"payload":"eyJfdHlwZSI6Imh0dHBzOi8vaW4tdG90by5pby9TdGF0ZW1lbnQvdjEiLCJwcmVkaWNhdGVUeXBlIjoiaHR0cHM6Ly9zcGR4LmRldi9Eb2N1bWVudC92Mi4zIiwicHJlZGljYXRlIjp7InNwZHhWZXJzaW9uIjoiU1BEWC0yLjMiLCJuYW1lIjoiaW5kZXguZG9ja2VyLmlvL2NhbGNpdW1pb24vbmV3LWFwaSNsaW51eC9hbWQ2NCJ9fQ=="}'
  if [[ "${COSIGN_OMIT_ARM64:-0}" != "1" ]]; then
    printf '%s\n' '{"payload":"eyJfdHlwZSI6Imh0dHBzOi8vaW4tdG90by5pby9TdGF0ZW1lbnQvdjEiLCJwcmVkaWNhdGVUeXBlIjoiaHR0cHM6Ly9zcGR4LmRldi9Eb2N1bWVudC92Mi4zIiwicHJlZGljYXRlIjp7InNwZHhWZXJzaW9uIjoiU1BEWC0yLjMiLCJuYW1lIjoiaW5kZXguZG9ja2VyLmlvL2NhbGNpdW1pb24vbmV3LWFwaSNsaW51eC9hcm02NCJ9fQ=="}'
  fi
fi
MOCK_COSIGN
chmod 700 "$mock_bin/cosign"

digest='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
image="calciumion/new-api@$digest"
issuer='https://token.actions.githubusercontent.com'
identity='https://github.com/QuantumNous/new-api/.github/workflows/.*@refs/(tags|heads)/.*'

PATH="$mock_bin:$PATH" \
COSIGN_LOG="$cosign_log" \
VERIFY_SIGNATURES=1 \
NEW_API_IMAGE="$image" \
NEWAPI_REDIS_IMAGE='redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
NEWAPI_POSTGRES_IMAGE='postgres@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
NEWAPI_SECRET_DIR="$secret_dir" \
bash scripts/verify-secure-compose.sh >/dev/null

[[ "$(wc -l < "$cosign_log" | tr -d ' ')" == "3" ]] || fail 'validator must make exactly three application cosign checks'
assert_contains "$cosign_log" "^CALL[[:space:]]+verify[[:space:]]+--certificate-oidc-issuer[[:space:]]+$issuer[[:space:]]+--certificate-identity-regexp[[:space:]]+.*[[:space:]]+$image$" 'verify the image signature against the configured keyless identity'
assert_contains "$cosign_log" "^CALL[[:space:]]+verify-attestation[[:space:]]+--type[[:space:]]+https://slsa\.dev/provenance/v1[[:space:]]+--certificate-oidc-issuer[[:space:]]+$issuer[[:space:]]+--certificate-identity-regexp[[:space:]]+.*[[:space:]]+$image$" 'verify signed SLSA v1 provenance for the immutable image'
assert_contains "$cosign_log" "^CALL[[:space:]]+verify-attestation[[:space:]]+--type[[:space:]]+https://spdx\.dev/Document/v2\.3[[:space:]]+--certificate-oidc-issuer[[:space:]]+$issuer[[:space:]]+--certificate-identity-regexp[[:space:]]+.*[[:space:]]+$image$" 'verify signed SPDX 2.3 SBOMs for the immutable image'

failure_output="$test_root/failure-output.txt"
if PATH="$mock_bin:$PATH" \
  COSIGN_LOG="$cosign_log" \
  COSIGN_FAIL_TYPE='https://spdx.dev/Document/v2.3' \
  VERIFY_SIGNATURES=1 \
  NEW_API_IMAGE="$image" \
  NEWAPI_REDIS_IMAGE='redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
  NEWAPI_POSTGRES_IMAGE='postgres@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
  NEWAPI_SECRET_DIR="$secret_dir" \
  bash scripts/verify-secure-compose.sh >"$failure_output" 2>&1; then
  fail 'validator accepted a failed SPDX attestation verification'
fi
assert_contains "$failure_output" 'secure-compose validation failed: SBOM verification failed for NEW_API_IMAGE' 'report an SPDX attestation verification failure'

missing_platform_output="$test_root/missing-platform-output.txt"
if PATH="$mock_bin:$PATH" \
  COSIGN_LOG="$cosign_log" \
  COSIGN_OMIT_ARM64=1 \
  VERIFY_SIGNATURES=1 \
  NEW_API_IMAGE="$image" \
  NEWAPI_REDIS_IMAGE='redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
  NEWAPI_POSTGRES_IMAGE='postgres@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
  NEWAPI_SECRET_DIR="$secret_dir" \
  bash scripts/verify-secure-compose.sh >"$missing_platform_output" 2>&1; then
  fail 'validator accepted an SPDX attestation set without arm64'
fi
assert_contains "$missing_platform_output" 'signed SBOMs must include linux/amd64 and linux/arm64' 'reject an incomplete platform SBOM set'

printf 'Docker attestation contracts are valid\n'
