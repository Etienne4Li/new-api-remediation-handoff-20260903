#!/usr/bin/env bash
# Validate the inputs used by docker-compose.secure.yml before any container
# is created. This script is intentionally side-effect free: it only checks
# image references, secret-file permissions, and (optionally) signatures.
set -euo pipefail

fail() {
  printf 'secure-compose validation failed: %s\n' "$1" >&2
  exit 1
}

require_image_digest() {
  local variable_name="$1"
  local reference="${!variable_name:-}"
  [[ -n "$reference" ]] || fail "$variable_name is not set"
  [[ "$reference" != *[[:space:]]* ]] || fail "$variable_name contains whitespace"
  [[ "$reference" == *@sha256:* ]] || fail "$variable_name must use image@sha256:<64-hex-digest>"

  local digest="${reference##*@sha256:}"
  local image_name="${reference%@sha256:*}"
  [[ -n "$image_name" ]] || fail "$variable_name has an empty image name"
  [[ "$digest" =~ ^[[:xdigit:]]{64}$ ]] || fail "$variable_name has an invalid sha256 digest"
}

secret_mode_is_private() {
  local path="$1"
  local raw_mode
  if raw_mode="$(stat -c '%a' "$path" 2>/dev/null)"; then
    :
  elif raw_mode="$(stat -f '%Lp' "$path" 2>/dev/null)"; then
    :
  else
    fail "cannot inspect permissions for $path"
  fi
  # Bash's base-8 arithmetic accepts a leading 0 only in some versions; the
  # explicit 8# form works for both GNU and BSD stat output.
  local mode=$((8#$raw_mode))
  (( (mode & 077) == 0 )) || fail "$path must not be group/world readable"
}

require_image_digest NEW_API_IMAGE
require_image_digest NEWAPI_REDIS_IMAGE
require_image_digest NEWAPI_POSTGRES_IMAGE

secret_dir="${NEWAPI_SECRET_DIR:-./secrets}"
[[ -d "$secret_dir" ]] || fail "secret directory does not exist: $secret_dir"
[[ ! -L "$secret_dir" ]] || fail "secret directory must not be a symlink: $secret_dir"
secret_mode_is_private "$secret_dir"

for secret_name in session_secret crypto_secret sql_dsn redis_conn_string redis_password postgres_password; do
  secret_path="$secret_dir/$secret_name"
  [[ -f "$secret_path" ]] || fail "missing secret file: $secret_path"
  [[ ! -L "$secret_path" ]] || fail "secret file must not be a symlink: $secret_path"
  secret_mode_is_private "$secret_path"
  [[ -s "$secret_path" ]] || fail "secret file is empty: $secret_path"
done

if [[ "${VERIFY_SIGNATURES:-0}" == "1" ]]; then
  command -v cosign >/dev/null 2>&1 || fail "VERIFY_SIGNATURES=1 requires cosign"
  command -v jq >/dev/null 2>&1 || fail "VERIFY_SIGNATURES=1 requires jq"
  issuer="${COSIGN_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
  identity="${COSIGN_IDENTITY_REGEXP:-https://github.com/QuantumNous/new-api/.github/workflows/.*@refs/(tags|heads)/.*}"
  cosign verify \
    --certificate-oidc-issuer "$issuer" \
    --certificate-identity-regexp "$identity" \
    "$NEW_API_IMAGE" >/dev/null || fail "signature verification failed for NEW_API_IMAGE"
  # Release workflows publish signed SLSA v1 provenance and SPDX 2.3 SBOM
  # attestations for the final multi-architecture digest. BuildKit's inline
  # per-platform attestations are not cosign signatures and are therefore not
  # accepted as substitutes here.
  cosign verify-attestation \
    --type https://slsa.dev/provenance/v1 \
    --certificate-oidc-issuer "$issuer" \
    --certificate-identity-regexp "$identity" \
    "$NEW_API_IMAGE" >/dev/null || fail "SLSA provenance verification failed for NEW_API_IMAGE"
  sbom_attestations_file="$(mktemp "${TMPDIR:-/tmp}/newapi-sbom-attestations.XXXXXX")"
  trap '[[ -z "${sbom_attestations_file:-}" || ! -f "$sbom_attestations_file" ]] || rm "$sbom_attestations_file"' EXIT
  cosign verify-attestation \
    --type https://spdx.dev/Document/v2.3 \
    --certificate-oidc-issuer "$issuer" \
    --certificate-identity-regexp "$identity" \
    "$NEW_API_IMAGE" >"$sbom_attestations_file" || fail "SBOM verification failed for NEW_API_IMAGE"
  jq -e -s \
    --arg amd64 'index.docker.io/calciumion/new-api#linux/amd64' \
    --arg arm64 'index.docker.io/calciumion/new-api#linux/arm64' '
      def envelopes: .[] | if type == "array" then .[] else . end;
      [
        envelopes
        | .payload?
        | select(type == "string")
        | @base64d
        | fromjson
        | select(.predicateType == "https://spdx.dev/Document/v2.3")
        | select(.predicate.spdxVersion == "SPDX-2.3")
        | .predicate.name
      ] as $names
      | ($names | index($amd64)) != null and ($names | index($arm64)) != null
    ' "$sbom_attestations_file" >/dev/null || fail "signed SBOMs must include linux/amd64 and linux/arm64"
  rm "$sbom_attestations_file"
  sbom_attestations_file=""

  # Third-party database images commonly use a different signing identity;
  # verify them separately when the operator has configured that trust policy
  # rather than silently treating an unrelated identity as trusted.
  if [[ "${VERIFY_DEPENDENCY_SIGNATURES:-0}" == "1" ]]; then
    dependency_identity="${COSIGN_DEPENDENCY_IDENTITY_REGEXP:-$identity}"
    for image_variable in NEWAPI_REDIS_IMAGE NEWAPI_POSTGRES_IMAGE; do
      image_reference="${!image_variable}"
      cosign verify \
        --certificate-oidc-issuer "$issuer" \
        --certificate-identity-regexp "$dependency_identity" \
        "$image_reference" >/dev/null || fail "signature verification failed for $image_variable"
    done
  fi
fi

printf 'secure-compose inputs are valid (immutable images, private secrets)\n'
