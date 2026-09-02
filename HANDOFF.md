# NewAPI Remediation Handoff

Date: 2026-09-03

## Scope and Goal

Continue the local, source-level remediation of the NewAPI deployment audit.
The intended outcome is a reviewable, tested remediation branch. This handoff
does not authorize SSH access, production deployment, production database
access, or destructive integration tests.

The working snapshot is `work/newapi-source`. It has no `.git` metadata. The
handoff branch is reconstructed from the public `e468b739` baseline in
`work/upstream-new-api`, then overlaid with this snapshot. Do not mistake the
absence of a Git diff in `work/newapi-source` for an absence of changes.

## Handoff Branch

- Branch: `audit/remediation-handoff-20260903`
- Base: `e468b739` (`docs: update PR template and remove PR Check workflow (#7053)`)
- Snapshot commit: `71a0975a` (`chore: hand off newapi audit remediation`)
- Final commit: the metadata commit containing this update; resolve with `git log -1`
- Remote: `origin` (`Etienne4Li/new-api-remediation-handoff-20260903`, GitHub fork)
- Source repository: `upstream` (`QuantumNous/new-api`, read-only for the current account)

## Completed

- Completed broad audit remediation across authentication, payment and billing
  lifecycle handling, Token authorization, media privacy, deployment hardening,
  caching, provider routing, logging, and frontend dependency hygiene. The
  detailed inventory and remaining production actions are in the local artifact
  `outputs/newapi-full-audit.md`.
- Eliminated multiple frontend stale-async-update races in profile, affiliate,
  image-preview, and related Hooks; added deterministic regression coverage.
- Reworked settings drafts so stale parent refreshes do not overwrite local
  edits or deletes in announcements, FAQ, Uptime Kuma, and language preferences.
  Regression coverage is in
  `web/src/features/system-settings/content/__tests__/draft-props.test.tsx`.
- Replaced Cohere's hand-managed streaming goroutine/channel path with the
  shared stream scanner. It now handles standard SSE frames, multiline data,
  raw JSON fallback, V1 and V2 terminal events, authoritative billed usage,
  synthesized terminal chunks, cancellation, and bounded drain behavior.
  Tests are in `relay/channel/cohere/stream_test.go`.
- Removed the stale Cohere streaming TODO from the adaptor path.
- Preserved the project identity and organization attribution. Do not rename or
  remove protected NewAPI or QuantumNous identifiers.

## In Progress

- Frontend static-quality cleanup is still underway. The latest full lint run
  has no errors but reports 15 `react/set-state-in-effect` warnings in:
  `param-override-editor-dialog`, `upstream-conflict-dialog`, `channel-affinity`,
  `use-billing-history`, `log-settings-section`, `group-ratio-visual-editor`,
  `channel-selector-dialog`, `upstream-ratio-sync`, `notification-tab`,
  `task-logs-filter-bar`, and `model-mutate-drawer`.
- The appropriate fix is per-component state ownership, not lint suppression.
  Preserve user drafts across parent/query refreshes, handle dialog-open
  transitions deliberately, and add behavior-focused regression tests.
- The Cohere change is implemented and its package/race tests pass, but it still
  needs inclusion in the final full-repository validation pass.

## Not Started or Still Required

- Complete the remaining 15 frontend React warnings with focused tests.
- Re-run the complete frontend suite, Knip, production build, audit, and bundle
  budget after all handoff changes settle.
- Re-run the full backend ordinary tests, race tests, vet, and independent
  `relaykit` build from this exact branch.
- Re-run Python gateway tests without enabling destructive MySQL integration
  tests.
- Update `outputs/newapi-full-audit.md` after final verification: current test
  counts, the exact remaining lint warning count, Cohere TODO status, and the
  explicit distinction between local fixes and undeployed production work.
- Production work remains separate: rotate exposed credentials, reconcile
  historical billing/task incidents, retire the old gateway, test provider
  sandboxes, perform backup restore drills, and run cross-database failure
  injection. None of these actions have been performed in this workspace.

## Full Plan

- [x] Triage audit findings and implement the first high-priority local fixes.
- [x] Add regression coverage for the implemented billing, authorization,
  stream, media, lifecycle, and frontend state fixes.
- [x] Make Cohere stream parsing/cancellation use the shared lifecycle helper.
- [x] Fix the latest settings-draft stale-refresh regressions.
- [ ] Finish the remaining frontend state-effect warnings with behavior tests.
- [ ] Run final full frontend validation on this branch.
- [ ] Run final full Go validation and independent relaykit build on this branch.
- [ ] Run the non-destructive Python gateway suite.
- [ ] Update the audit report's verification summary and remaining-risk status.
- [x] Prepare this handoff document and a GitHub-pushable branch.

## Verification Status

### Re-run During This Handoff Session

From `work/newapi-source/web`:

```text
bun run format                                  PASS
bun run test -- src/features/system-settings/content/__tests__/draft-props.test.tsx --reporter=dot
                                                  PASS: 1 file, 6 tests
bun run typecheck                               PASS
bun run lint                                    PASS with 15 warnings, 0 errors
bun run format:check                            PASS
```

From `work/newapi-source`:

```text
go test ./relay/channel/cohere -count=1 -v      PASS: 3 tests
go test -race ./relay/channel/cohere -count=1   PASS
```

### Previously Reported, Not Re-run in This Final Handoff Session

```text
go test ./common ./model ./service ./controller ./relay ./router ./middleware -count=1
go test -race ./common ./model ./service ./controller ./relay ./router ./middleware -count=1
go vet ./common ./model ./service ./controller ./relay ./router ./middleware
cd relaykit && GOWORK=off go build ./...
cd work/async-image-gateway && .venv/bin/python -m unittest -v
```

The prior Python result was 44 passed and 10 skipped. The skipped tests are
explicit destructive MySQL integration tests; do not set
`RUN_DESTRUCTIVE_MYSQL_INTEGRATION_TESTS=1` unless a disposable database has
been explicitly approved.

## Exact Final Validation Commands

From the NewAPI repository root:

```bash
cd web
/Users/Etienne/.bun/bin/bun run format:check
/Users/Etienne/.bun/bin/bun run test -- --reporter=dot
/Users/Etienne/.bun/bin/bun run typecheck
/Users/Etienne/.bun/bin/bun run lint
/Users/Etienne/.bun/bin/bun run knip
/Users/Etienne/.bun/bin/bun run build
/Users/Etienne/.bun/bin/bun audit --json

cd ..
go test ./common ./model ./service ./controller ./relay ./router ./middleware -count=1
go test -race ./common ./model ./service ./controller ./relay ./router ./middleware -count=1
go vet ./common ./model ./service ./controller ./relay ./router ./middleware
cd relaykit && GOWORK=off go build ./...
```

The Python gateway is a sibling workspace at `work/async-image-gateway` in the
original audit workspace. Run its suite from that directory with
`.venv/bin/python -m unittest -v`; do not enable destructive MySQL tests.

## Known Risks and Pitfalls

- `work/newapi-source` is a source snapshot with no Git metadata. It differs
  substantially from public upstream: 883 different files and 501
  source-only entries after excluding dependency/build directories. Treat this
  handoff branch as a reconstruction of the deployed customized source, not a
  small upstream patch.
- Never run `rsync --delete` against a Git worktree root without excluding
  `.git/`. During this handoff it removed the worktree's `.git` metadata file;
  the worktree was repaired before commit and the branch was revalidated.
- Never commit `web/node_modules`, `electron/node_modules`, `web/dist`, or
  `electron/dist`. They account for most of the snapshot's size and are ignored
  build artifacts/dependencies.
- Do not connect to SSH, production databases, payment providers, or real
  upstream model providers as part of this code handoff.
- Use `common.Marshal`, `common.Unmarshal`, and related wrappers rather than
  direct `encoding/json` marshal/unmarshal calls in Go business code.
- Do not use lint disables to erase `set-state-in-effect` warnings. The risk is
  user-visible loss of an edit after stale server data arrives.
- Do not claim a local source fix is deployed. The audit report has production
  incident and operational-remediation items that require separate approval.
- The public remote may reject branch creation if the configured GitHub account
  lacks write access. The current account is read-only on `QuantumNous/new-api`,
  so the branch is pushed to the personal fork
  `Etienne4Li/new-api-remediation-handoff-20260903` instead. Do not force-push
  or target `main`.

## Suggested Skills for the Next Engineer

- `diagnose` for any failing regression or race before changing behavior.
- `tdd` for the remaining frontend state ownership fixes.
- `review` before opening a pull request, against this handoff branch.
- `playwright` only if a remaining frontend fix requires browser-level behavior
  verification.
