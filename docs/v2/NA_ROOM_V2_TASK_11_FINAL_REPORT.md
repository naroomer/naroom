# NA Room V2 - Task 11 Acceptance Repair Final Report

Date: 2026-07-28
Worktree: `/private/tmp/naroom-v2`
Branch: `codex/v2-foundation`
Base HEAD: `27e287fd13d0`

## 1. Scope and repository state

Task 11 was completed on top of the existing dirty worktree without reset,
revert, commit, push, deploy, or manual production database writes.

Initial acceptance-repair state:

- 42 modified tracked files;
- 7 untracked Task 11 files;
- the same base HEAD shown above.

Final state:

- 42 modified tracked files;
- 7 untracked Task 11 files;
- no deleted tracked files;
- no generated database or binary artifacts in `git status`.

Tracked diff summary:

```text
42 files changed, 5537 insertions(+), 618 deletions(-)
```

Untracked Task 11 files:

```text
docs/v2/NA_ROOM_V2_TASK_11_FINAL_REPORT.md
frontend/src/routes/v2/helper/purchases/+page.svelte
internal/v2/city_http.go
internal/v2/city_http_test.go
internal/v2/city_registry.go
internal/v2/city_registry_test.go
internal/v2/frontend_city_registry_test.go
```

The tracked package covers:

```text
cmd/naroom-v2-dev/main.go
cmd/v2testserver/main.go
docs/v2/PRODUCT_SPEC.md
e2e/tests/v2_browser_e2e.js
frontend/src/lib/cities.js
frontend/src/lib/i18n.js
frontend/src/routes/+page.server.js
frontend/src/routes/v2/board/[city]/+page.svelte
frontend/src/routes/v2/helper/purchase/+page.svelte
frontend/src/routes/v2/how-it-works/+page.svelte
frontend/src/routes/v2/informer/+page.svelte
frontend/src/routes/v2/listing/[id]/+page.svelte
frontend/src/routes/v2/new/+page.svelte
frontend/src/routes/v2/restore/+page.svelte
internal/v2/adapters.go
internal/v2/adapters_test.go
internal/v2/db.go
internal/v2/helper_http.go
internal/v2/helper_http_test.go
internal/v2/helper_service.go
internal/v2/helper_service_test.go
internal/v2/helper_watcher.go
internal/v2/helper_watcher_test.go
internal/v2/http.go
internal/v2/http_test.go
internal/v2/informer_service.go
internal/v2/informer_transport.go
internal/v2/lifecycle_worker.go
internal/v2/lifecycle_worker_test.go
internal/v2/listing_form.go
internal/v2/listing_http.go
internal/v2/listing_http_test.go
internal/v2/listing_service.go
internal/v2/release_e2e_test.go
internal/v2/review_http.go
internal/v2/review_service.go
internal/v2/review_service_test.go
internal/v2/schema.sql
internal/v2/service.go
internal/v2/telegram_transport.go
internal/v2/telegram_transport_test.go
internal/v2/wire.go
```

## 2. Browser gate repair

The dev/test server now wires the complete controlled V2 flow: Client create and
reactivate, Helper payment and reveal, balance and chain providers, Telegram,
review delivery, time advancement, handoff expiry, and database counters.

The browser suite has 19 steps per internal run and covers the complete path
without deleting or weakening the previously failing steps. Each command runs
the suite twice, so the final two independent commands provide 76 passing step
executions.

Domain side effects are asserted:

- `wallet_already_visible`: 0 new Client flows and 0 new Client invoices;
- self-purchase: 0 Helper purchases, 0 Helper invoices, 0 balance calls, and
  0 invoice allocator calls;
- handoff: old browser token revoked and replay, wrong wallet, expiry, and
  concurrent loser all rejected safely.

## 3. Self-purchase guard

Evidence:

- `internal/v2/helper_http.go`: `handleCreate` calls `IsListingOwner` before
  balance or invoice provider calls;
- `internal/v2/helper_service.go`: `IsListingOwner` compares the requested
  listing's owner fingerprint;
- `CreatePurchase` repeats the owner check inside its transaction to close the
  race between the early check and insert;
- `internal/v2/helper_http_test.go`:
  `TestHelperHTTP_SelfPurchase_NoExternalCalls`;
- `internal/v2/release_e2e_test.go`: `TestReleaseR08b_SelfPurchaseGuard`;
- browser step `5d`.

Verified counters for the rejected request:

| Counter | Result |
|---|---:|
| balance provider calls | 0 |
| Helper invoice provider calls | 0 |
| new Helper purchases | 0 |
| new Helper invoices | 0 |
| HTTP result | 409, stable `self_purchase_not_allowed` code |

Private/wrong wallet failures do not expose the owner wallet, profile, or
fingerprint.

## 4. Cross-device handoff threat matrix

The handoff URL is built as:

```text
/v2/helper/purchase#handoff=<opaque-one-time-token>
```

It contains no wallet, currency, purchase ID, browser token, or query string.
The receiving page removes the fragment from history before the redeem request,
then asks the user to enter the wallet. Token and wallet are sent only in the
POST body.

Evidence:

- `internal/v2/helper_http.go`: create/redeem endpoints and safe error mapping;
- `internal/v2/helper_service.go`: one-active-attempt policy, atomic consume,
  browser-token rotation, and old-token revocation;
- `frontend/src/routes/v2/helper/purchase/+page.svelte`: fragment extraction,
  URL cleanup, explicit wallet form, and POST redeem;
- `internal/v2/helper_service_test.go`:
  `TestHandoff_CreateAndRedeem`,
  `TestHandoff_NewPendingRevokesPrevious`,
  `TestHandoff_ExpiredRejected`,
  `TestHandoff_ReplayRejected`,
  `TestHandoff_WrongWalletRejected`,
  `TestHandoff_WrongTokenRejected`;
- `internal/v2/helper_http_test.go`:
  `TestHelperHTTP_HandoffConcurrentRedeemHasSafeLoser`;
- browser step `6g`.

| Threat / case | Expected and verified result |
|---|---|
| wallet in URL | absent |
| token in server query | absent; fragment only |
| token left in history | removed before POST |
| wrong wallet | safe indistinguishable failure |
| unknown token | safe indistinguishable failure |
| expired token | safe indistinguishable failure |
| replay | rejected |
| two concurrent redeems | exactly one winner |
| second handoff creation | previous pending token revoked |
| successful redeem | same invoice restored |
| old browser token after success | revoked |

## 5. Owner and public mode

Owner mode is authorized by a server-verified management capability, not by
browser storage.

Evidence:

- `POST /v2/listings/{id}/owner-view` in `internal/v2/listing_http.go`;
- `GetListingOwnerView` in `internal/v2/listing_service.go`;
- route registration in `internal/v2/wire.go`;
- owner/public rendering in
  `frontend/src/routes/v2/listing/[id]/+page.svelte`;
- browser steps `4b` and `5d`.

| Context | Result |
|---|---|
| owner browser + valid management capability | `Your listing`, preview, city, state, remaining time, Telegram state, allowed owner action |
| owner mode purchase controls | absent |
| owner mode Helper progress | absent |
| owner mode navigation | `Back to board` present |
| private window without capability | ordinary public Helper view |
| forged/stale localStorage marker | does not authorize owner mode |
| owner wallet attempts public purchase | backend 409 before external calls |

The post-publish action is `Manage listing`, not an invitation to buy one's own
contact.

## 6. Create and restore separation

`/v2/new` handles only a new paid creation. It resumes only an actually
unfinished local creation/payment flow. A completed marker does not hijack a new
click on `+`.

`/v2/restore` performs its own wallet + recovery-code journey and never creates
a new $5 invoice.

Evidence:

- `frontend/src/routes/v2/new/+page.svelte`;
- `frontend/src/routes/v2/restore/+page.svelte`;
- restore and owner endpoints in `internal/v2/listing_http.go`;
- browser steps `5e` and `10`.

| Restore case | Result |
|---|---|
| wrong wallet | localized generic not-found |
| wrong code | byte-identical localized generic not-found |
| mixed wallet/code | byte-identical localized generic not-found |
| unknown pair | byte-identical localized generic not-found |
| valid expired entitlement | terminal |
| valid visible listing | owner mode |
| valid hidden listing | new balance check, new Telegram binding, reactivate |
| another listing of profile already visible | blocked without exposing it |
| any restore request | creates 0 new $5 invoices |

Russian generic response:

```text
Объявление не найдено. Проверьте кошелёк и код.
```

## 7. Single-source V2 city registry

`internal/v2/city_registry.go` is the only complete V2 registry. It defines 21
enabled cities and their countries. `/api/v2/board/cities` serves the list and
live/sample counts.

Board, create, Informer, purchase history, and How It Works use the backend
endpoint. Frontend keeps only `FALLBACK_CITY_ID`; its 9-item `CITIES` array is
the unchanged legacy V1 registry, not a V2 copy.

Evidence:

- `internal/v2/city_registry.go`;
- `internal/v2/city_http.go`;
- `internal/v2/city_registry_test.go`;
- `internal/v2/city_http_test.go`;
- `internal/v2/frontend_city_registry_test.go`;
- route wiring in `internal/v2/wire.go`.

HTTP robustness:

- limiter key strips the ephemeral port with `net.SplitHostPort` and safe
  fallback;
- `Scan` and `rows.Err()` return 500;
- failed or partial results are never cached;
- error responses use `Cache-Control: no-store`;
- Informer validates every enabled backend city.

During final review an unintended V1 expansion to 21 cities was removed, and
the legacy root redirect outcome was restored to `/v2/board/buenos_aires`.

## 8. Review lifecycle, migration, and delivery

The production path creates review entitlements only on the first successful
contact reveal.

Canonical timestamps:

```text
created_at   = first_revealed_at
available_at = first_revealed_at + 3600
expires_at   = first_revealed_at + 86400
```

The first reveal is an atomic CAS. A concurrent loser re-reads the winner's
timestamps and does not duplicate entitlements. Exact retry of an already
consumed rating is checked before availability and expiry gates, preserving
idempotency across migration backfill.

Evidence:

- `RevealHelperContact` in `internal/v2/helper_service.go`;
- canonical creation and consume order in `internal/v2/review_service.go`;
- migration in `internal/v2/db.go`;
- schema comments and constraints in `internal/v2/schema.sql`;
- delayed delivery in `internal/v2/telegram_transport.go`;
- scheduling in `internal/v2/lifecycle_worker.go`.

Key tests:

- `TestHelperReveal_EntitlementsOnlyAfterFirstReveal`;
- `TestHelperHTTP_ConcurrentFirstReveal`;
- `TestReview_FirstRevealCreatesTwoEntitlements`;
- `TestReview_FirstRevealEntitlementsIdempotent`;
- `TestReview_AvailableAtGate`;
- `TestReview_ExpiryBoundary`;
- `TestReview_MigrationPreservesConsumedIdempotency`;
- `TestReview_SnapshotLifecycle`;
- `TestReview_SnapshotSurvivesBindingDeletion`;
- `TestTelegramTransport_SendPendingReviewNotifications`;
- `TestLifecycleWorker_PendingReviewNotificationDelivery`;
- `TestLifecycleWorker_RunLoop_ReviewNoOverlap`.

Delivery behavior:

- Client receives an immediate purchase notice without review buttons;
- delayed review buttons are not sent before `available_at`;
- Client and Helper delivery states are independent and deduplicated;
- encrypted destinations are cleared after successful send, permanent failure,
  or expiry;
- Helper can register a Telegram reminder and leave the page;
- review availability remains server-enforced even if a user keeps a stale page.

## 9. Honest payment/provider observability

The API no longer invents `next_check_at = now + 5` or synthetic
confirmations. It exposes recorded domain state:

- `last_successful_chain_check_at`;
- `provider_status` derived from actual recorded checks and terminal state;
- invoice phase without claiming an exact next provider poll.

Evidence:

- persistence in `internal/v2/helper_watcher.go`;
- response mapping in `internal/v2/helper_http.go`;
- provider adapters and tests in `internal/v2/adapters.go` and
  `internal/v2/adapters_test.go`;
- `TestHelperHTTP_RestorePaymentObservabilityUsesRecordedChecks`;
- `TestHelperWatcher_ProviderOutageAndDuplicateCycles`;
- frontend state in
  `frontend/src/routes/v2/helper/purchase/+page.svelte`.

Verified UI behavior:

- active payment/confirmation step has a progress pulse;
- `prefers-reduced-motion` disables animation;
- zero-confirmation detection and confirmation are distinct states;
- outage/recovery does not lose payment state;
- completed purchase is not marked degraded;
- `Back to listing` and `Back to board` remain available.

## 10. Payment layout and navigation

The Helper purchase page fits without page scrolling or horizontal overflow at
all required viewports. Payment status, amount, USD value, address, QR,
nicknames, instructions, progress, both navigation exits, and language switcher
are visible without overlap.

Required visual sizes:

| Viewport | QR size | Scroll | Overlap |
|---|---:|---|---|
| 1440x900 | 180x180 | none | none |
| 1280x720 | 170x170 | none | none |
| 390x844 | 128x128 | none | none |
| 375x667 | 128x128 | none | none |
| 360x640 | 112x112 | none | none |

The short-mobile header uses one compact row; the logo is hidden only when
needed to preserve progress and both functional exit links.

## 11. Final gates

Final acceptance results:

| Gate | Result |
|---|---|
| `gofmt -w internal/v2 cmd/naroom-v2-dev cmd/v2testserver` | PASS |
| `git diff --check` | PASS |
| `go build ./...` | PASS |
| `go test ./... -count=1` | PASS |
| `go test -race ./internal/v2/... -count=1` | PASS, 0 races |
| `npm run check` | PASS, 0 errors, 62 pre-existing V1 warnings |
| `npm run build` | PASS |
| E2E command 1, internal run 1 | 19/19 PASS |
| E2E command 1, internal run 2 | 19/19 PASS |
| E2E command 2, internal run 1 | 19/19 PASS |
| E2E command 2, internal run 2 | 19/19 PASS |
| Final E2E total | 76/76 PASS, 0 FAIL, 0 SKIP |
| dynamic ports | released |
| required screenshots | present and visually inspected |

Screenshot paths:

```text
/private/tmp/naroom-v2/e2e/screenshots/run2_layout_1440x900.png
/private/tmp/naroom-v2/e2e/screenshots/run2_layout_1280x720.png
/private/tmp/naroom-v2/e2e/screenshots/run2_layout_390x844.png
/private/tmp/naroom-v2/e2e/screenshots/run2_layout_375x667.png
/private/tmp/naroom-v2/e2e/screenshots/run2_layout_360x640.png
```

## 12. Boundaries

- No V1 route component or V1 domain behavior was changed.
- The legacy 9-city array and legacy root redirect outcome were explicitly
  preserved during final self-review.
- No Task 12 work was started.
- No commit was created.
- No push was performed.
- No deploy was performed.
- No production service, environment, database, or Telegram bot was touched.
- No manual production database write was made.
