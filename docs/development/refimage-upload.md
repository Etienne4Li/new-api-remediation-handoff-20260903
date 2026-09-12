# Reference-image upload bridge (newapi → hosted image URLs)

Status: implemented on branch `lietio/refimage-upload`. Not built, not deployed.

## Purpose

The `lietio-video` task plugin only accepts reference assets as public HTTPS
URLs. This bridge converts multipart reference **images** uploaded to
`POST /v1/videos` into hosted HTTPS URLs before the plugin decoder runs, so an
application can send a file instead of first hosting it itself.

Reference **videos** and **audios** are unchanged: they keep their existing
URL-only contract.

## Runtime flow

```
TokenAuth → PinTaskPluginEndpoint → TaskPluginEndpointOnly
  → PrepareTaskPluginEndpoint
        buildTaskPluginRouteRequest   (multipart parsed; files stay host-owned)
        placeholder decodeRequest     ← network-free pre-validation on a COPY
        BridgeReferenceImageUpload    ← uploads images, rewrites fields
        plugin decodeRequest          (now sees URLs, not file parts)
  → Distribute → RelayTaskSubmit
        ValidateRequestAndSetAction   (bridge is idempotent here)
        price calculation
        pre-consumption               ← only after a successful upload
        upstream request
```

The bridge is **validation-first**. Before any byte reaches the image host, the
pinned plugin decoder is run against a *copy* of the protocol request in which
every planned file is represented by a placeholder URL under the reserved
segment `/__refimage_pending__/`. Only a request the decoder accepts is
uploaded. The placeholder copy is a throwaway structure: it is never installed
on the request context, its URLs never reach the image host or the upstream, and
the live field map is not mutated by the probe. When no decoder is available the
bridge refuses to upload.

The bridge runs **before** price calculation and pre-consumption. A failed
upload therefore never pre-charges the caller and never reaches the upstream.

## Field matrix

| Multipart file field | Plugin field written |
| --- | --- |
| `first_image` | `first_image` |
| `input_reference` | `first_image` |
| `image` | `first_image` |
| `last_image` | `last_image` |
| `referenceImages` | `referenceImages` |
| `referenceImages[]` | `referenceImages` |
| `images` | `referenceImages` |

`first_image` and `last_image` accept exactly one value. `referenceImages` is
capped at 30 files per request (the Seedance-2.5 allowance in the plugin model
matrix, and inside the endpoint middleware cap of 32).

A served field that already carries a hosted URL is never also given an
uploaded file, and the same target may not be reached through two aliases: the
request is rejected instead of being merged or silently overwritten.

## Validation

* Plugin key: only `lietio-video`. Every other plugin key is untouched.
* Body: `multipart/form-data` with at least one file part. File-less multipart
  and JSON requests are untouched.
* Content type: decided by **sniffing the file bytes** (`http.DetectContentType`
  plus a real header decode), never by the client-declared part type. Only
  `image/jpeg`, `image/png`, `image/webp`, and `image/gif` pass. A declared type
  that is itself an allowlisted image must agree with the bytes; a generic or
  absent declaration (`application/octet-stream`, empty) carries no information
  and is not treated as a conflict.
* Dimensions: at most 16384 px per edge and 40 million pixels total. The header
  is parsed, so a tiny file declaring a huge canvas is refused before it can be
  forwarded as a decode bomb.
* Size: 25 MB per file (the host-side per-file limit), 120 MB total.
* Response: only `files[0].url` is read, and only when it is a single-line
  absolute HTTPS URL under `https://z.lietio.com/` with no userinfo, query, or
  fragment. Any other shape is rejected.

## Failure behaviour

Every failure returns one neutral message:

```
reference image upload failed; the request was not submitted
```

Image-host and upstream response bodies are never forwarded to the caller. The
specific rejection reason is written to the server log only.

## Configuration

Startup-only, environment based (see
`setting/system_setting/refimage_upload.go`). No database option, request field,
or plugin manifest can supply the credential.

| Variable | Default | Meaning |
| --- | --- | --- |
| `REFIMAGE_UPLOAD_ENABLED` | `false` | Master switch. The bridge is off unless this is true. |
| `REFIMAGE_UPLOAD_ENDPOINT` | `http://zipline:3000/api/upload` | Upload endpoint. |
| `REFIMAGE_UPLOAD_TOKEN` | *(none)* | Upload credential (SecretRef: direct value). |
| `REFIMAGE_UPLOAD_TOKEN_FILE` | *(none)* | Upload credential (SecretRef: file path). |
| `REFIMAGE_UPLOAD_TIMEOUT_SECONDS` | `30` | Per-request upload deadline (1–120). |

### Credential injection (SecretRef convention)

The bridge never hard-codes a credential. It resolves the token from the
environment at startup, in this order:

1. `REFIMAGE_UPLOAD_TOKEN` — the literal value.
2. `REFIMAGE_UPLOAD_TOKEN_FILE` — a path whose contents are the token.

The credential must be the dedicated upload service account's token, injected by
the deployment tooling. Do **not** use the image-host administrator password, an
existing `.env` from another service, or any other secret.

If the bridge is enabled but no credential resolves, the bridge **fails
closed**: requests carrying reference images are rejected with the neutral
message instead of being uploaded anonymously.

## Request headers sent upstream

| Header | Value |
| --- | --- |
| `Authorization` | the resolved credential |
| `x-zipline-deletes-at` | `24h` |
| `Content-Type` | `multipart/form-data` (form field `file`) |

The upload uses a **dedicated** HTTP client rather than the shared relay client:

* `Proxy` is `nil`, so the credential is never sent through an ambient egress
  proxy (`HTTP_PROXY` and friends are ignored).
* Redirects are **not followed** (`http.ErrUseLastResponse`); a 3xx is treated
  as a failed upload, so the credential is never replayed against a redirect
  target.
* The response header wait is bounded, and only the first 64 KB of the response
  body is read.

## Cleanup policy

Uploaded objects are **never deleted** by this process. Before submission an
object cannot be associated with a task id, so there is no reliable key to
delete on. Retention is delegated to the host's own expiry, which the
`x-zipline-deletes-at: 24h` header requests. Reference images are expected to be
disposable after 24h; no task data references them after settlement.

## Tests

* `service/refimage_upload_test.go` — success, validation-before-upload
  (decoder rejection and missing verifier cause zero uploads), excess image
  count, alias/URL-plus-file conflicts, spoofed MIME, real-format acceptance
  (PNG/JPEG/GIF/WebP), decode-bomb dimensions, host failures (500/401/413),
  malformed responses, missing `files[0].url`, malicious response URLs,
  oversize/unsupported/unknown-field rejection, too many files, duplicate
  single-file field, alias mapping, filename sanitisation, idempotency, plugin
  gate, disabled config, credential-missing fail-closed, redirect not followed
  (and no credential replay), transport policy (no proxy, bounded header wait).
* `middleware/refimage_upload_test.go` — bridge runs before the plugin decoder;
  the decoder rejects a request before any upload; upload failure stops the
  request before distribution; JSON behaviour unchanged; other plugins are
  untouched.
* `relay/channel/task/jsplugin/refimage_upload_test.go` — adaptor-level success,
  failure with no upstream request built, plugin gate.
* `setting/system_setting/refimage_upload_test.go` — configuration defaults,
  environment and file credentials, invalid value rejection.

All tests use `httptest` servers on the loopback interface and never reach the
public network.
