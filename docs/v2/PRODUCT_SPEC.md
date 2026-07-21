# NA Room V2 - Product Specification

Created: 2026-07-17
Status: DISCOVERY / PRODUCT RULES IN PROGRESS

This document is the source of truth for the new simplified product model. It is separate from NA Room V1. No rule marked `UNRESOLVED` may be silently chosen during implementation.

## 1. Why V2 Exists

NA Room V1 became too complex and fragile: internal encrypted chats, WebSocket lifecycle, reconnect behavior, room state, response acceptance, payment-to-chat transitions, balance-based capacity, recovery, and notification coordination created too many coupled states.

V2 removes the platform chat entirely. NA Room becomes a paid listing and paid contact-access marketplace. External communication happens outside NA Room.

Current product position:

- V1 is unlikely to be promoted further in its current form.
- V2 must be fully specified before code changes begin.
- V2 must not inherit V1 behavior unless that behavior is explicitly accepted here.

## 2. Status Vocabulary

- `FIXED`: explicitly stated product rule.
- `UNRESOLVED`: discussed but no final decision yet.
- `CONSTRAINT`: technical/privacy fact that the design cannot ignore.
- `OUT OF SCOPE`: explicitly absent from V2.

## 3. Core V2 Model

### 3.1 Client listing payment

`FIXED`

- Client pays $5 for a five-day publication entitlement.
- Payment comes before listing creation. Before confirmation there is no public or editable listing, only a private payment intent bound to the entered wallet.
- When the payment intent is created, the platform issues one secret code. Before payment, the code plus the original wallet address restores that payment intent and invoice status.
- After payment confirmation, the same payment intent becomes the paid listing container and the same secret becomes its listing management code. Only then does the listing form begin.
- The entitlement lasts five consecutive calendar days and starts when the $5 payment is confirmed. The calendar clock never pauses, including while publication is blocked by insufficient balance.
- Listing visibility is activated in 24-hour windows.
- Client must return and manually publish/reactivate the listing for another daily window while entitlement remains.
- The purpose of daily reactivation is to bring people back to the platform and avoid boards full of abandoned listings.
- Public UI states that the Client must maintain at least $150 on the listing wallet.
- The backend hard acceptance floor is $120. Initial publication requires a confirmed $5 payment and an authoritative post-payment wallet balance of at least $120.
- Every daily reactivation requires a fresh balance check of at least $120 on the wallet bound to the listing. Reactivation itself is free.
- The difference between the public $150 requirement and the $120 hard floor is an intentional volatility/fee buffer, not a pricing change.

### 3.2 Helper contact payment

`FIXED`

- Helper pays $10 to reveal the Client's external contact.
- There is no internal response/acceptance/chat step before contact purchase.
- A contact purchase never removes or hides the listing.
- The listing stays visible until its current daily publication window ends.
- It can be reactivated according to the remaining five-day entitlement.
- Any number of contact purchases may occur while the listing is publicly available.
- There is no refund flow.
- NA Room does not remember whether a particular Helper bought this listing before.
- The same person can accidentally buy the same contact again; the platform does not deduplicate buyers.

### 3.3 External communication

`FIXED`

- NA Room has no internal chat.
- NA Room stores no messages between Client and Helper.
- After purchase, Client and Helper communicate outside NA Room.
- Client must connect Telegram for NA Room service notifications; publication without a Client notification Telegram is not allowed. A fresh connection is required for each publication window (first publication and every daily reactivation); the previous window's binding is not reused and is deleted after the window closes. Client may connect a different Telegram account for each window.
- Client provides exactly one structured text contact: either a Telegram username/link or a Signal link. Uploaded images are not accepted. Multiple contacts in a single listing are not accepted.
- The contact is immutable for the five-day listing lifetime. To change the contact, a new paid listing is required.

### 3.4 Telegram Informer and eligibility

`FIXED`

- A Telegram bot named/positioned as the NA Room `Informer` only notifies eligible subscribers when a new listing appears on the board in their selected city.
- The Informer's responsibility ends after delivering those notifications. It is not the Helper profile, website identity, purchase authorization, contact-delivery mechanism, or review identity.
- To gain access to Informer notifications, a person supplies information about a BTC or LTC address whose current balance is at least $1,000 USD equivalent at the check time.
- Wallet ownership is not proven at Informer registration. Any person may enter a publicly discoverable rich address.
- This initial balance gate is intentionally a work/effort filter, not identity or ownership proof.
- Informer access does not create a Helper profile and does not mean that the subscriber is or will become a Helper.
- The wallet address used to unlock Informer access is not connected to any later Helper wallet, country lock, purchase, rating, or contact entitlement.
- Telegram user/chat ID is only a delivery destination for the selected-city Informer subscription.
- Exact rules for changing the selected city or subscribing to more than one city remain unresolved.

## 4. Removed V1 Functionality

`OUT OF SCOPE`

- Internal chat rooms.
- WebSocket connections.
- Message encryption keys and encrypted message history.
- Chat reconnect and multi-browser chat ownership.
- Client acceptance or rejection of Helper responses.
- Pending/accepted response queues.
- Chat invoices that open a room.
- Chat duration and half-closed room states.
- `peer_left` / `client_left` behavior.
- Active-chat slot calculation.
- Any obligation to keep users communicating inside NA Room.
- Refund processing.

## 5. Client Journey - Current Draft

1. Client opens the city board.
2. Client selects "I need help".
3. Client supplies the wallet that will fund the listing payment.
4. Platform creates a private payment intent bound to that wallet and generates one cryptographically random secret code.
5. Client must save the code before paying. The server stores only its secure hash.
6. Client receives and pays the $5 invoice. No public or editable listing exists before confirmation.
7. If the page refreshes, closes, or loses its browser session, the code plus the original wallet address restores the same payment intent and current invoice status.
8. After payment confirmation, the five-calendar-day entitlement starts and the payment intent becomes the paid listing container. The same secret code becomes its listing management code.
9. Platform checks the actual funding wallet's post-payment balance.
10. If the balance is below $120, the listing form and publication remain blocked. There is no refund. Client may top up the same wallet and request another check with the management code before the absolute deadline. The calendar clock continues while blocked.
11. If the balance is at least $120, Client starts the structured listing form.
12. Client supplies exactly one external contact — either a Telegram username/link or a Signal link — that Helpers will buy. The contact is immutable for the five-day entitlement period.
13. Client connects a Telegram account for NA Room service notifications. A fresh connection is required for each window; successful bot response at `/start` is the delivery proof that gates publication.
14. Client completes the form and activates the listing for a board window of no more than 24 hours.
15. Helpers may buy access to the contact for $10 while the listing is visible. Purchases do not affect listing visibility or remaining entitlement.
16. When the current 24-hour window ends, the listing disappears from the board.
17. Client returns with the listing management code and supplies the original listing wallet address again.
18. Platform verifies the code, checks that the supplied wallet matches the listing wallet fingerprint, confirms the five-day deadline has not passed, and checks that the current balance is at least $120. The user-facing requirement remains $150.
19. If all checks pass, Client activates the next daily window without another payment. A fresh Telegram connection is required for each new window; the previous window's binding is not reused and is deleted after the window closes. Client may connect a different Telegram account for each window.
20. Exactly five calendar days after entitlement start, the listing can no longer be published. A new paid listing is required.

Unresolved Client journey points:

- Whether the Client has a persistent pseudonymous identity across listings. (`FIXED Task 06`: Client has no public pseudonym; only aggregate review counts from Helpers are stored per wallet-bound profile — not displayed as a public name.)
- Whether Client reputation carries across listings. (`FIXED Task 06`: Yes — Client profile uses `HMAC-SHA256(serverKey, "naroom:v2:wallet:" + chain + ":" + wallet)` as fingerprint; same wallet across listings shares one profile and carries cumulative positive/negative counts.)
- Whether Client and Helper are permanently separate roles.

## 5A. Task 04C: Backend HTTP Client Journey Contract

`FIXED` — Task 04C closed the backend HTTP orchestration for the Client listing path. This section is not production routing and not final frontend; it captures the backend contract only.

### 5A.1 Routes

```
POST /v2/client/listings/restore    — composite navigation response
POST /v2/client/listings/publish    — first publication
POST /v2/client/listings/reactivate — daily reactivation
GET  /v2/board/{city}               — public board
GET  /v2/listings/{listing_id}      — public listing detail
```

Routes are test-only. Not mounted in `cmd/naroom/main.go`.

### 5A.2 Restore phase/next_action mapping

| Phase | Next action | Trigger |
|---|---|---|
| awaiting_payment | wait_for_payment | invoice pending |
| payment_detected | wait_for_payment | invoice payment_detected |
| payment_expired | start_new_listing | invoice expired before entitlement |
| paid_low_balance | recheck_balance | invoice confirmed, state paid_low_balance or payment_confirmed |
| form_ready | prepare_first_publication | form_ready, no listing, Telegram not ready |
| form_ready | publish | form_ready, no listing, Telegram ready |
| visible | view_listing | listing effectively visible (state=visible AND now < visible_until) |
| hidden | connect_telegram_for_reactivation | listing not effectively visible, entitlement alive, Telegram not ready |
| hidden | reactivate | listing not effectively visible, entitlement alive, Telegram ready |
| finished | start_new_listing | listing finished OR now >= entitlement_expires_at |

Effective visibility is determined from timestamps, not DB state label. A stale `state=visible` with `visible_until` in the past is treated as hidden without mutating the DB.

`telegram_status` is always `"needs_link" | "link_pending" | "ready" | "active"`. It is only set by `QueryLinkStatus`; a bare binding without a matching destination is never reported as `ready`.

### 5A.3 Public board and listing detail privacy contract

Board (`GET /v2/board/{city}`) and detail (`GET /v2/listings/{listing_id}`) return only:
`id`, `display_name`, `city`, `country_code`, `dependency_type`, `help_type`, `urgency`, `languages`, `visible_until` (unix seconds), `time_left_sec` (non-negative).

They never include: management_code, wallet, contact value/ciphertext, flow_id, binding_ref, token, Telegram chat_id, fingerprint, invoice data, or any internal ID.

Unknown city → 404. Invalid/unknown/malformed listing_id → identical 404. Hidden, finished, expired → identical 404. No lifecycle mutation from GET.

Ordering: newest `last_activated_at` first, then listing `id` ascending (deterministic tie-break).

### 5A.4 What Task 04C does NOT close

- Production routing (cmd/ wiring)
- Final frontend
- Helper purchase, reviews, Informer, retention
- Same-browser session token

## 6. Helper Journey - Current Draft

1. A person sees a public listing, either directly on the board or through any external source, and decides to buy its Client contact.
2. The person presses the contact-purchase/respond action.
3. The person enters their own BTC or LTC wallet address. This wallet is independent of any address that may previously have been used to unlock the Informer.
4. Platform checks whether this wallet already has a Helper profile and checks whether its current balance is sufficient for the purchase and required post-payment minimum.
5. If the wallet is already associated with a country, backend permits the purchase only when the listing has the same normalized country code.
6. If this is the wallet's first platform purchase, platform displays an informational warning that buying a contact in this country will prevent this wallet-bound Helper profile from buying contacts in other countries. Other cities in the same country remain allowed.
7. The country warning requires no checkbox or separate confirmation. Continuing to the invoice is treated as acceptance; failure to read it is the user's responsibility.
8. Platform also informs the person that the same wallet must fund the $10 invoice and must still hold at least $1,000 USD equivalent after payment, otherwise the contact will not be revealed. There is no refund.
9. Platform creates a $10 contact invoice bound to the entered wallet and this purchase attempt.
10. After payment confirmation, platform verifies that payment came from the expected entered wallet and checks that wallet's post-payment USD balance.
11. If sender verification succeeds and post-payment balance is at least $1,000, platform grants access to this one Client contact.
12. On the wallet's first successful contact purchase, platform creates the Helper profile. The country lock is committed at the first successful sender verification and post-payment balance check (≥ $1,000). Existing profiles keep their original country.
13. Platform increments the Helper aggregate purchase count and immediately displays the purchased Client contact on the current website payment/result page.
14. If payment came from another wallet or the expected wallet's post-payment balance is below $1,000, the contact is not revealed and there is no refund. If balance was low, Helper may call the recheck-balance endpoint to retry the post-payment balance check until `confirmation_deadline_at + 24 h`; after that deadline the purchase moves to the terminal `failed` state.
15. After revealing the contact, platform may issue a separate single-use review code/link. It authorizes only a later review of this purchased contact/Client and can never reveal the contact again.
16. Helper does not register Telegram as part of the purchase flow. No Helper Telegram channel is required.
17. Helper leaves NA Room and communicates with the Client through the purchased external contact.
18. The listing remains available to other buyers. Contact purchases do not change listing visibility.
19. If payment confirms after the listing's daily window or five-day entitlement ends, the contact is still revealed, because listing visibility is checked only at the time of invoice creation, not at the time of payment confirmation or reveal.

`FIXED`

- Browser generates a cryptographically random 256-bit lowercase-hex `purchase_token` before the create POST. The backend stores only an HMAC hash; the raw token is never stored or logged. Token is returned once in the `201` create response. Subsequent retries with the same token return `200` with the existing purchase, no new rows created.
- A new token does not create a parallel purchase for the same profile and listing while a non-terminal purchase already exists. After terminal states (`invoice_expired`, `failed`, `receipt_expired`) a new token may start an independent purchase on the same listing.
- Contact is shown immediately on the current website result page. The `receipt_expires_at` window is 24 h from first reveal.

Unresolved Helper journey points:

- Exact public Helper pseudonym/name generation and whether it is immutable.
- Exact pre-invoice balance formula needed to leave at least $1,000 after the $10 payment, network fees, and price movement.
- Whether Helper and Client roles are fixed or the same wallet-bound identity can perform both actions.
- Expiry and exact format of the review-only code/link.
- Same-browser behavior if the page is refreshed or closed before payment confirmation completes.

## 7. Listing Management Code

`FIXED`

- The code is issued with one private payment intent before payment.
- Before confirmation, the code plus the original wallet address restores only that payment intent and its invoice status; it does not create or expose a listing.
- After confirmation, the same code manages the one paid listing created from that payment intent.
- It is not currently approved as a permanent identity or account recovery code.
- It allows the Client to return after closing the browser both while payment is pending and while the resulting paid listing remains manageable.
- It must not expose the Client's wallet or contact.
- Server stores only a secure hash of the code.
- The code stops authorizing publication when that listing's entitlement is exhausted or expired.
- Daily reactivation uses both the listing management code and the original wallet address. The supplied address is matched against the protected wallet fingerprint bound to the listing and its live balance is checked.
- One management code can authorize only its own listing and only the wallet fingerprint bound to that listing.
- The management code is a cryptographically random secret; it is not calculated directly from the wallet address.
- Implementation requirement: `management_code_hash` and `wallet_fingerprint` are stored separately on the listing. On each activation, server verifies both.
- Wallet fingerprint is computed as a keyed value such as `HMAC-SHA256(server_secret, chain || normalized_wallet_address)`, not as a publicly reproducible plain wallet hash.
- Suggested user-facing code shape: a public listing locator plus an unguessable secret, for example `NR2-<listing-locator>.<random-secret>`. The secret itself is never stored in plaintext.

`UNRESOLVED`

- Whether the code may still provide a read-only receipt after publication rights end.
- How long encrypted contact data remains after entitlement ends.
- Whether the code allows editing the contact after a Helper has already purchased it.

## 8. Names and Reputation

### 8.1 Listing-scoped temporary name

`FIXED`

- A random display name is generated once at first publication and is stable for the five-day listing lifetime.
- Reactivation (daily window renewal) never changes the display name.
- A new paid listing receives a new random name; the old name is not reused by the same Client.
- The name is not a persistent Client identity and carries no reputation across separate listings.
- Purpose: a Helper may visually recognize that they have already encountered or purchased this listing during its active period.

### 8.2 Permanent public pseudonym

`FIXED`

- Helper keeps one random platform pseudonym under the same wallet-bound Helper profile.
- Helper account age and aggregate rating are attached to that pseudonym.
- The platform must never expose the Helper's Telegram username, chat ID, wallet address, or wallet fingerprint as the public name.
- Exact pseudonym vocabulary, generation, and whether a name can ever be regenerated remain unresolved.
- Client has a wallet-bound reputation profile (not a public pseudonym) created at first $5 payment confirmation. The profile carries aggregate positive and negative review counts submitted by Helpers after contact purchases. The profile has no public display name — it is not exposed to the board or to other users as a named identity.
- `FIXED Task 06 (was stale)`: Client does NOT have a public pseudonym. Public observers and Helpers cannot link purchases to a named Client identity. The `client_reputation` field in public listing and board DTOs exposes only `{member_since, positive_count, negative_count}` — no name, no pseudonym.
- The platform must never expose the Client's wallet address, wallet fingerprint, Telegram chat_id, or internal profile ID as the public name.

### 8.3 Wallet-derived reputation

`FIXED (Task 06)`

- Both Helper and Client profiles use a keyed wallet fingerprint: `HMAC-SHA256(serverHMACKey, "naroom:v2:helper-wallet:" + chain + ":" + normalizedAddress)` for Helpers and `HMAC-SHA256(serverHMACKey, "naroom:v2:wallet:" + chain + ":" + normalizedAddress)` for Clients. Using distinct domain prefixes prevents cross-role fingerprint collisions.
- A plain `SHA256(wallet_address)` is insufficient because a known address can be hashed and matched after a database leak. The keyed HMAC is safe under server-secret confidentiality.
- The fingerprint must never be exposed through public API, HTML, logs, analytics, or Telegram.
- A different wallet produces a different identity unless an explicit migration/link mechanism exists.
- BTC/LTC receive addresses may change, so an address is not always a durable wallet identity.
- Informer eligibility is unrelated to Helper identity and does not create this wallet fingerprint.
- A Helper profile begins from the person's own wallet entered for a specific contact purchase, and a successful purchase requires the $10 payment to come from that same expected wallet.
- A Client profile is created when the $5 payment is confirmed. Its wallet fingerprint is derived from the same wallet that funded the listing payment.
- Because the address is public, knowing or typing it cannot securely authenticate a person to the wallet-bound profile.
- A separate unguessable Helper management/recovery capability or later proof of wallet control is required if the same profile must be securely reopened; the exact mechanism is unresolved.

### 8.4 Independent persistent pseudonymous identity

`DISCUSSED, NOT FINAL`

- A random permanent identifier/recovery secret, separate from the wallet, carries name and rating.
- Wallet is then only a payment instrument.
- This allows reputation to survive wallet and session changes.
- It creates a persistent platform identity and therefore contradicts a strict "no persistent identity" rule.
- This mechanism has not been approved.

## 9. Meaning of the Five Paid Days

### Fixed model: five consecutive calendar days

`FIXED`

- Entitlement starts when the $5 payment is confirmed.
- It ends exactly five days later.
- Client must reactivate the listing for each daily publication window.
- Calendar time continues even when Client forgets to reactivate.
- Data lifecycle and expiry are simple and predictable.
- No daily publication window may continue beyond the absolute five-day deadline.

Example:

```text
payment confirmed: July 1, 12:00
absolute end:       July 6, 12:00
```

### Not selected: five separate 24-hour publication days

`NOT SELECTED`

- Payment creates five publication-day credits.
- Starting one daily publication consumes one credit.
- Skipped days do not consume credits.
- An outer validity deadline is required to avoid storing unused entitlements forever.
- The possible outer deadline has not been chosen.

Example only, not a decision:

```text
$5 = five 24-hour publication windows
windows must be used inside an unresolved maximum period
```

V2 currently uses the fixed five-consecutive-calendar-day model above.

## 10. Wallet as Identity - Explanation

`FIXED FOR HELPER, UNRESOLVED FOR CLIENT AND AUTHORIZATION`

If wallet fingerprint is the identity:

```text
wallet A -> identity A -> name/rating A
wallet B -> identity B -> name/rating B
```

Helper reputation and country lock do not automatically transfer between wallet-bound profiles. Transfer would require proof of both wallets, a separate permanent secret, or an internal linking operation; no transfer mechanism is currently approved.

Knowing a public wallet address is sufficient only for the unrelated Informer eligibility check. A real Helper profile wallet is established through the contact-purchase flow, but knowing that public address still cannot authenticate or manage the profile. Secure Helper profile return options, none approved yet:

- Sign a challenge with the wallet.
- Treat a confirmed payment sender as evidence, with known BTC/LTC limitations.
- Use a separate secret and treat wallets only as payment instruments.

## 11. Contact Storage

`CONSTRAINT`

The platform must possess the contact long enough to sell and reveal it. "Store no information" cannot literally include zero contact state while purchase is pending.

Minimum model:

- Validate the contact format.
- Encrypt contact values at rest with authenticated encryption.
- Never expose contacts on the public board, in SSR HTML, logs, URLs, analytics, Telegram notifications, or error messages.
- Reveal contact only after confirmed $10 payment.
- Define and enforce a deletion deadline.
- Treat contact as structured contact data rather than arbitrary listing text.

`FIXED` (2026-07-19)

- Exactly one structured text contact per listing: either a Telegram username/link or a Signal link.
- Uploaded images are not accepted as contacts.
- Multiple contacts in one listing are not accepted.
- The contact is immutable for the five-day listing lifetime; changing it requires a new paid listing.

`FIXED` (Task 04A — 2026-07-19)

- In the first release, the platform validates contact **format only**; it does not verify that the Client owns or controls the provided Telegram or Signal account.
- Accepted Telegram forms: `@username`, `https://t.me/username`, `https://username.t.me`. Normalized to `https://t.me/<lowercase_username>`.
- Accepted Signal form: `https://signal.me/#<opaque_payload>` copied from Signal. Fragment is preserved verbatim; inner structure is not interpreted.
- Platform UI must clearly state that the Client is responsible for the accuracy and ownership of the provided contact.
- Raw or normalized contact value is never included in error messages, logs, or public API responses.

`UNRESOLVED`

- When encrypted contact ciphertext is permanently deleted.

`POST-LAUNCH (non-blocking)`

- Whether Telegram ownership can be verified through a bot flow in a future release.
- Whether Signal ownership can be verified; Signal provides no equivalent automated bot flow.

### Contact text versus image

`DECIDED` (2026-07-19)

Structured text contact is required. An uploaded image is not accepted. Structured Telegram username / Signal link supports format validation, direct opening, exact copying, accessibility, and smaller encrypted storage.

### 11.1 Purchased contact delivery

`FIXED WORKING MODEL`

- After successful expected-wallet payment and post-payment balance verification, immediately show the purchased Client contact on the current website payment/result page.
- Give the user clear copy/open controls and instruct them to save the contact before leaving.
- Do not issue a user-facing purchase recovery code and do not promise that the purchased contact can be reopened later.
- The frontend may retain an internal short-lived same-browser purchase token solely to continue polling the same invoice across an ordinary refresh. This is implementation state, not a code the user must save.
- Do not send the raw contact or a contact-ready message to Helper through Telegram.
- Helper Telegram registration is absent from the purchase flow.
- Informer remains entirely separate and only reports new selected-city listings.
- After contact reveal, platform may issue a separate single-use review code/link. That review capability can submit one later rating but can never reveal the purchased contact.

`UNRESOLVED`

- Exact lifetime and format of the review-only code/link.
- Exact same-browser behavior if the page is closed while blockchain confirmation is still pending.
- Whether a confirmed contact remains visible after an ordinary refresh of the same browser page or is strictly shown once.

### 11.2 Telegram transport — V2 delivery path (Task 04B / Task 04B-FIX / Task 04B-FIX3)

`IMPLEMENTED`

**Overview**

The V2 Telegram transport delivers a "ready binding" notification to the Client's Telegram account before listing publication. The Client connects their Telegram by clicking a bot deep-link generated by the platform. No Telegram username, chat ID, or permanent identity is stored in plaintext.

**Webhook mode**

The bot operates in webhook mode (not long polling). The `X-Telegram-Bot-Api-Secret-Token` header is validated in constant time before the request body is parsed. Any validation failure returns 401 immediately.

**Endpoints**

- `POST /v2/client/telegram-links` — generate a deep-link (bot URL + raw token); rate-limited to 5 req/min per IP.
- `POST /v2/client/telegram-links/status` — query current link state; rate-limited to 10 req/min per IP.
- `POST /v2/telegram/client/webhook` — receive Telegram updates; validates webhook secret before body parsing.

**Token design**

- 32 crypto/rand bytes encoded as base64url without padding → exactly 43 characters.
- TTL: 15 minutes from generation.
- Only the HMAC-SHA256(tokenSecret, "naroom:v2:telegram-link:" + rawToken) is stored in the database. The raw token is never persisted.
- Token hash format: exactly 64 lowercase hex characters; enforced by a DB CHECK constraint.

**Binding-ref design**

- `binding_ref` is 16 crypto/rand bytes encoded as lowercase hex with a `bnd_` prefix (e.g. `bnd_<32 hex chars>`).
- The `v2_client_notification_bindings.binding_ref` column has a UNIQUE constraint.
- On UNIQUE collision, `HandleWebhook` generates a fresh `binding_ref`, re-encrypts `chat_id` with the new AAD, and retries the finalisation transaction. Maximum `maxBindingRefRetries = 5` attempts.
- If collision retries are exhausted **after** a successful Telegram send, the handler returns 503 and the attempt **stays processing** (not reset to pending). This prevents an immediate retry from re-sending the Telegram confirmation message. The attempt remains processing until the lease expires, at which point a new claimant may reclaim via the CAS.
- If generation or encryption fails **before** any send, the attempt is reset to pending (send has not occurred; a retry is safe).

**Attempt state machine**

```
absent → pending (CreateLink) → processing (webhook CAS) → deleted (on success)
```

- `pending`: no lease; only raw token hash stored.
- `processing`: 30-second lease; protected by CAS UPDATE requiring `lease_until IS NULL OR now >= lease_until`.
- On success: attempt row is deleted inside the commit transaction.
- On retryable failure (before send): attempt is CAS-reset to pending so the Client can generate a new link.
- On post-send failure (DB error, collision exhaustion): attempt stays `processing` until lease expiry; no re-send on immediate retry.

**Exact lease ownership**

A finalizer owns the attempt only as long as the exact `newLease` it set during the CAS is still present in the database row. Every post-send database operation — re-verification, permanent-expiry cleanup DELETE, and success DELETE — uses the three-part exact predicate:

```sql
WHERE token_hash = <claimed hash>
  AND state = 'processing'
  AND lease_until = <newLease from this handler's CAS>
```

Behaviour by re-verify result:
- Row found, `finalizeNow < newLease`: handler owns the claim; finalization proceeds.
- Row found, `finalizeNow >= newLease` (lease boundary), token/entitlement alive: 503; new claimant can reclaim.
- Row absent (`sql.ErrNoRows`): handler is **superseded** (claim lost, row replaced by `CreateLink`, or reclaimed by another handler). Return neutral 200; do **not** create a binding, delete any row, or reset any state.

A stale (superseded) handler never writes using another handler's lease, and never deletes a replacement attempt.

**Permanent-expiry cleanup algorithm**

When the token or entitlement has permanently expired at `finalizeNow`, the handler invokes `cleanupExpiredAttemptTx`:

1. `DELETE … WHERE token_hash=? AND state='processing' AND lease_until=?` inside open transaction.
2. Check `Exec` error → 503 on any error (attempt preserved).
3. Check `RowsAffected` error → 503 on any error.
4. `RowsAffected == 0` → claim was already lost or replaced; rollback + neutral 200 (do not touch foreign state).
5. `RowsAffected == 1` + commit success → confirmed cleanup; return neutral 200.
6. Commit error → 503 (attempt row may or may not be deleted; Telegram retries until lease expires).

No `//nolint:errcheck` is permitted on any security or state-transition `Exec`/`Commit` call.

**CreateLink atomicity and attempt protection**

`CreateLink` performs all reads and mutations — including deletion of any expired binding and its cascaded destination row — inside a single database transaction. There is no non-atomic window between the expiry check and the insert of the new token row.

A processing attempt is protected from replacement **only** while both conditions hold simultaneously: `lease_until > now` **AND** `expires_at > now`. An attempt whose token has permanently expired (expires_at ≤ now) may be replaced by a new `CreateLink` even if the lease is technically still active.

**Webhook secret validation**

The `X-Telegram-Bot-Api-Secret-Token` header is validated before body parsing. Both the header value and the configured secret are hashed with SHA-256 first; the two 32-byte digests are compared via `hmac.Equal` (constant-time, fixed-length comparison). This prevents timing side-channels regardless of string length.

**Bot username contract**

Configured bot username must be 5–32 ASCII alphanumeric/underscore characters and end with the suffix `bot` (case-insensitive). `@` prefix is not accepted.

**Delivery proof ordering (steps)**

1. Validate webhook secret (401 on failure). SHA-256 digest comparison (fixed-length).
2. Parse body with trailing-JSON detection (200 neutral on malformed JSON or trailing bytes after the first JSON value).
3. Reject `message.chat.id ≤ 0` (200 neutral). Private chat IDs must be strictly positive.
4. Check `message.chat.type == "private"` (200 neutral otherwise).
5. Parse `/start <token>` or `/start@<BotUsername> <token>` (case-insensitive bot name).
6. Validate token format (43-char base64url).
7. Compute token hash.
8. CAS attempt: pending or stale-processing → processing with 30s lease.
9. Re-read flow_id + window_number + entitlementExpiresAt.
10. Generate `binding_ref`; encrypt `chat_id` using AAD bound to `binding_ref`.
11. Send confirmation message to Telegram. 429 and 5xx are always retryable regardless of body size. Oversized 2xx body (> 64 KiB) is treated as permanent delivery failure.
12. Capture `finalizeNow = now()` **after** the send returns. On each collision-retry iteration, a fresh `finalizeNow = now()` is taken **after** ref generation and re-encryption and **immediately before** `db.Begin` — not at the top of the iteration — so any time elapsed during generation is correctly reflected.
13. Re-verify: if `token.expires_at ≤ finalizeNow` (token permanently expired), atomically delete exact claimed attempt and return neutral 200. If `entitlement_expires_at ≤ finalizeNow` (entitlement permanently expired), same cleanup and neutral 200. If `lease_until ≤ finalizeNow` (lease expired, but token/entitlement alive), return 503 — new claimant may reclaim.
14. Compute `validUntil = min(finalizeNow + 15 min, entitlementExpiresAt)`. This single value is written to **both** `v2_client_notification_bindings.valid_until` and `v2_telegram_destinations.expires_at`.
15. Begin transaction: re-verify attempt + flow using `finalizeNow`, attach binding + destination with `validUntil`, delete attempt, commit.
16. On `binding_ref` UNIQUE collision, generate new ref and re-encrypt; retry from step 12 (up to `maxBindingRefRetries` total attempts).

**Lease boundary**

The finalizer uses a strict half-open interval: finalization is permitted only when `finalizeNow < lease_until`. At exact equality (`finalizeNow == lease_until`), the lease is considered expired and the finalizer returns 503, allowing a new claimant to reclaim via the CAS.

**At-least-once delivery**

If the process crashes after step 11 (send succeeded) but before the commit in step 15, the binding is not created and the attempt lease eventually expires. The Client can generate a new link. This may result in the confirmation message being sent more than once if the same token is retried, but only a single binding is created. A real DB failure inside the finalization transaction rolls back both the binding and destination; the attempt remains `processing` and is recoverable after lease expiry.

**Encrypted destination**

- `v2_telegram_destinations`: stores `chat_id_ciphertext`, `chat_id_nonce`, `key_version` keyed by `binding_ref`.
- AAD: `"naroom:v2:destination-aad:" + keyVersion + ":" + bindingRef` — binds ciphertext to the specific binding; cross-binding replay is detected. When `binding_ref` changes on collision retry, chat_id is re-encrypted with the new AAD.
- `binding_ref` is a FK to `v2_client_notification_bindings(binding_ref)` with `ON DELETE CASCADE`; deleting the binding deletes the destination.
- `expires_at = validUntil` — exactly equal to `v2_client_notification_bindings.valid_until` (single shared value computed post-send; see step 14).

**Ready/active status invariant**

`QueryLinkStatus` uses an INNER JOIN between `v2_client_notification_bindings` and `v2_telegram_destinations` requiring `d.expires_at = b.valid_until`. A bare binding (no destination), or a binding+destination pair where the two expiry values differ — even if both are in the future — is **not** reported as `ready` or `active`. Only an exact-expiry-matched pair with a live destination and live binding qualifies.

**Destination required for publication**

`FirstPublish` and `Reactivate` enforce that a non-expired destination row exists for the active binding before promoting the listing. If no active binding or no destination is found, the operation returns `ErrDestinationMissing` and rolls back. The destination `expires_at` is extended to the listing's `visible_until` inside the same transaction.

**Privacy invariants**

- No permanent Client–Telegram identity. Each publication window requires a fresh link.
- Raw token, wallet address, management code, and flow_id are never stored in any transport table.
- Plaintext chat_id is never stored; only AES-256-GCM ciphertext with binding-ref AAD.
- A Client may use a different Telegram account for each activation window.
- chat_id, raw token, and bot token are never included in error messages or logs.

**Failure boundaries**

| Failure point | HTTP response | Attempt state | Effect |
|---|---|---|---|
| Webhook secret invalid | 401 | unchanged | No body parsed |
| Malformed JSON or trailing bytes | 200 | unchanged | Neutral; no body parsed past first error |
| `chat.id ≤ 0` in payload | 200 | unchanged | Neutral |
| Expired/unknown token | 200 | unchanged | Neutral |
| Encrypt chat_id error | 503 | reset to pending | No send |
| Send: retryable error (including 429/5xx, even oversized) | 503 | reset to pending | Client retries |
| Send: permanent error | 200 | reset to pending | Neutral (bot blocked etc.) |
| Send: oversized 2xx body (`> 64 KiB`) | 200 | reset to pending | Treated as permanent delivery failure |
| Token permanently expired at `finalizeNow` | 200 | **deleted** | Neutral 200; exact attempt cleaned atomically |
| Entitlement permanently expired at `finalizeNow` | 200 | **deleted** | Neutral 200; exact attempt cleaned atomically |
| Lease expired at `finalizeNow` (token/entitlement alive) | 503 | stays processing | Reclaim possible after lease; 503 for retry |
| DB fail after send (real transaction failure) | 503 | stays processing | Rolled back; recovers after lease expiry |
| `binding_ref` UNIQUE collision (≤ 5 retries) | — | retried in-process | New ref generated; re-encrypted; clock refreshed |
| `binding_ref` UNIQUE collision (> 5 retries, after send) | 503 | stays processing | Lease expires; Client retries after expiry |
| Commit success | 200 | deleted | Binding + destination created atomically |

## 12. Helper Data and Purchase History

`FIXED`

- A persistent pseudonymous `helper_id` is anchored to a protected fingerprint of the Helper profile wallet, not to Telegram.
- The Helper profile stores only the minimum persistent account and reputation state: creation time, public pseudonym, country lock, aggregate confirmed-purchase count, aggregate positive count, and aggregate negative count.
- Informer subscriptions are entirely separate from Helper profiles.
- Helper purchase flow has no Telegram registration or Telegram identity record.
- Platform does not retain a permanent list of which listings or which Clients a Helper purchased.
- Platform does not prevent the same Helper from purchasing the same listing again.
- A temporary purchase record links the current invoice, Helper, listing, and review entitlement only long enough to confirm payment, reveal/recover the contact, enforce idempotency, and accept the allowed review.
- The detailed purchase-to-listing link is deleted after the still-unresolved technical/review retention period. Aggregate counters remain.
- A person who successfully pays from another qualifying wallet can obtain another wallet-bound Helper profile. V2 currently has no stronger human-identity proof.

`CONSTRAINT`

- Country lock and reputation can be enforced only within the same wallet-bound Helper profile. They can be bypassed by funding purchases from another qualifying wallet unless stronger human-identity verification is added later.
- The unrelated Informer rich-address check neither creates nor grants control of a Helper reputation profile.
- A public wallet address cannot serve as a password. If two people know the same rich address, the platform must not silently give both control of one reputation profile. A separate secure profile-access mechanism is required and remains unresolved.
- Retaining aggregate reputation without a detailed purchase history prevents ordinary product lookup of past purchases, but temporary invoice/payment records may still need short retention for accounting, idempotency, and incident investigation.

## 13. Reviews

`FIXED (Task 06)`

**Bidirectional binary reviews — one per purchase per side.**

### Client → Helper (via Telegram)

- After a Helper's $10 payment confirms and the contact is revealed, platform sends a Telegram message to the Client's active binding with an inline keyboard (Positive / Negative).
- The Telegram callback payload is `"rv:" + hex(raw16bytes) + ":" + action` (~37 chars, fits the 64-byte Telegram callback_data limit).
- Client can rate anytime within 24 hours of the purchase `contact_ready_at`. After expiry the buttons do nothing.
- Review is not forced; Client may simply not press any button. The Telegram message is sent at most once per purchase.
- If no active, consistent Client Telegram binding+destination exists at purchase creation time, `CreatePurchase` returns `ErrReviewNoBinding` (HTTP 409 `client_notification_unavailable`). The transaction rolls back completely — zero orphan profile, purchase, invoice, entitlement, or snapshot rows.

### Helper → Client (via website)

- After contact reveal, the Helper calls `POST /v2/helper/reviews/capability` with purchase_id + purchase_token + wallet_address to obtain a `review_token` and `expires_at` (24 hours from `contact_ready_at`). The review token is NOT returned in the contact-ready HTTP response.
- Review token format: `base64url(raw16bytes) + "." + base64url(HMAC-SHA256(hmacKey, "naroom:v2:review-token:" + reviewRef))` — 66 characters.
- `POST /v2/helper/reviews/capability` validates purchase ownership and returns the review token plus Client reputation snapshot.
- `POST /v2/helper/reviews` consumes the review token and increments the Client's profile aggregate counters.
- Wrong purchase_token and wrong wallet_address return byte-identical 404 (no enumeration).
- Review tokens are never logged. Encrypted fields (Telegram chat_id) are NULLed after delivery.

### Common rules (both directions)

- A review can increment the target's positive or negative aggregate exactly once per purchase per reviewer side.
- Exact repeat (same token, same rating) is idempotent: returns 200/accepted=true without re-incrementing.
- Conflicting re-use (same token, different rating) returns 409 `review_already_consumed`.
- Free-text reviews are not part of the current model.
- Ratings become visible immediately; no minimum-count gate.
- `Cache-Control: no-store, private` on both Helper review HTTP endpoints.
- Review expiry is 24 hours from purchase `contact_ready_at` for both directions.

## 14. Payments

`FIXED`

- Client listing entitlement price: $5.
- Helper contact reveal price: $10.
- No refund flow.
- Duplicate processing of one confirmed transaction must not create duplicate entitlement or duplicate reveal effects.
- Reloading a pending payment must return to the same invoice rather than create another invoice for the same attempt.
- User-facing Client wallet requirement: at least $150.
- Backend hard acceptance floor for initial post-payment publication and every daily reactivation: at least $120.
- Values between $120 and $149.99 pass the backend check despite the public $150 warning.
- Informer access separately requires information about any publicly visible BTC/LTC address with at least $1,000 USD equivalent; this address has no Helper profile or purchase meaning.
- Before creating a Helper contact invoice, platform checks the person's entered own wallet and whether it can satisfy the $10 purchase plus the required post-payment minimum.
- The $10 invoice is bound to that entered wallet.
- Helper contact purchase succeeds only when payment is verified as coming from the same expected wallet and that wallet retains at least $1,000 USD equivalent after confirmation.
- If the post-payment Helper balance is below $1,000, contact is not revealed and there is no refund.
- If another wallet pays the invoice, contact is not revealed and there is no refund.

`FIXED` (Task 03 — V2 Invoice State Machine)

- V2 supports both BTC and LTC. Manual testing on LTC first, then BTC, is an operational preference, not a code restriction.
- Invoice detection window: 60 minutes from invoice creation (`detection_deadline_at = created_at + 3600`).
- Detection boundary: `observedAt ≤ detection_deadline_at` is accepted (inclusive). `observedAt > detection_deadline_at` is rejected as `ErrExpired`.
- Confirmation requires: invoice status `payment_detected` AND observed tx has ≥1 confirmation AND `observedAt ≤ confirmation_deadline_at`.
- `confirmation_deadline_at = payment_detected_at + 86400` (24 hours after detection).
- `payment_detected → expired` after 24 hours without a confirmed transaction. The invoice transitions to `expired` when `now > confirmation_deadline_at`.
- Late payment (after 60 min detection window) does not activate entitlement; no automatic refund.
- Amounts from separate transactions are never aggregated. Only a single transaction meeting the full amount_atomic is accepted.
- Exact atomic amount floor with no 99% tolerance. `amountAtomic < invoice.amount_atomic` returns `ErrInsufficientPayment`.
- Expected sender verification: the wallet entered at invoice creation must be among the transaction input addresses (HMAC fingerprint comparison). A wrong-sender transaction does not lock the invoice; the watcher continues scanning until the detection deadline.
- After payment confirmation: watcher matches a sender address from the confirmed tx inputs against the stored wallet fingerprint, then checks that address's current USD balance. The matched address is held in memory only (never stored in the database).
- API/RPC outage does not change invoice state. Watcher retries with bounded exponential backoff (min 5s, max 5 min, factor 2).
- HD address derivation contract: `HDAllocatorAdapter` wraps the V1 `crypto.HDWallet` and **shares** the V1 `invoice_index` atomic counter. This is intentionally correct — same xpub with one shared counter guarantees unique derivation indexes across V1 and V2. A separate V2 counter for the same xpub would collide with V1-derived addresses.

`FIXED (Task 05-FIX / Task 05-FIX2)`

- Pre-invoice balance floor: $1,010 (= $10 invoice + $1,000 post-payment minimum). Implemented as `helperPreInvoiceFloorUSD = 1010.0` in the HTTP handler; balance provider returns a value below this floor → 402, no rows created.
- Low-balance retry: a Helper who paid but holds < $1,000 post-payment may call `/recheck-balance` until `confirmation_deadline_at + 24 h`; after that the purchase moves to `failed`. Implemented via `RecheckHelperBalance` / `NormalizeHelperExpired`.
- Late listing-window payment: contact is still revealed; listing visibility is checked only at invoice creation time, not at payment confirmation or reveal.
- Late entitlement payment: same rule — entitlement expiry is checked only at invoice creation.

`CONSTRAINT`

- The Helper wallet entered at purchase is the expected payment sender and profile anchor. The unrelated rich address entered for Informer access is ignored throughout contact purchase.
- BTC/LTC transactions can have multiple inputs, change outputs, exchange withdrawals, and CoinJoin-like structures. The implementation must define an authoritative and testable funding-wallet resolution rule; it must not pretend that every transaction always has one obvious human-owned sender address.
- Warning: wallets controlled by exchanges (Coinbase, Binance, etc.) have thousands of co-owners. A sender check against an exchange hot wallet address will produce false positives (another user's withdrawal from the same exchange hot address). This is a known limitation of blockchain-based sender verification without wallet ownership proof.

## 15. Interruption and Return Requirements

`FIXED FOR CLIENT, LIMITED FOR HELPER`

Client listing/payment management must survive:

- page refresh;
- browser close and reopen;
- temporary network loss;
- payment confirmation while the browser is closed;
- token expiry;
- delayed blockchain/RPC response;
- duplicate watcher execution.

Client must be able to return to the same listing using its listing management code while that code is valid.
Before payment confirmation, the same code plus the original wallet address must return the Client to the same private payment intent and invoice status. Payment confirmation while the browser is closed must not strand the paid Client; on return, the code must continue into the paid listing flow.

Helper is not given a user-facing purchase recovery code and is not promised cross-browser or cross-device recovery of the purchased contact. An internal short-lived browser token may resume the same pending invoice after an ordinary refresh without user action. Duplicate watcher execution must remain idempotent. Exact behavior when the browser is closed before blockchain confirmation remains unresolved. The public wallet address alone is not authentication. The review-only code/link cannot reopen the paid contact.

## 16. Implementation Rule

- Do not modify V1 code from this draft.
- Do not ask an implementation agent to fill unresolved rules with recommendations.
- First complete this product specification.
- Then derive a V2 state machine, database retention model, API contract, frontend journey, and interruption test matrix.
- V2 must be implemented as a coherent model, not as incremental patches to V1 chat behavior.

## 17. Current Decision Register

### Fixed

- No internal chat.
- Client pays $5 for five consecutive calendar days of publication entitlement.
- Listing is published in manually activated 24-hour windows.
- Public UI states a minimum Client wallet balance of $150.
- Backend hard floor is $120 after the $5 payment and at every daily reactivation.
- Helper pays $10 for external contact access.
- Contact purchase does not remove the listing.
- Unlimited purchase attempts/buyers while listing is available.
- No refunds.
- Platform does not remember whether a particular Helper bought the listing before.
- One secret code is issued for the Client payment intent before payment; after confirmation, that same code manages only the resulting listing and is not an approved permanent identity.
- Telegram Informer access accepts information about any publicly visible BTC/LTC address with at least $1,000 USD equivalent; ownership is not proven and no Helper profile is created.
- Informer Telegram identity is only a selected-city notification destination and is unrelated to later Helper activity.
- Helper activity begins when a person enters their own wallet on a listing to purchase that Client's contact.
- Helper profile is anchored to a protected fingerprint of the expected purchase wallet, not to Telegram or the Informer eligibility address.
- Helper profile retains aggregate account age, purchase count, positive count, negative count, and one country lock, but not a permanent list of purchased listings.
- Helper may operate in multiple cities inside the locked country but not in another country through the same Helper account.
- The country warning is informational and requires no checkbox; proceeding to the invoice is acceptance.
- A $10 contact purchase must be paid by the entered expected wallet. At least $1,000 USD equivalent must remain after payment or the contact is withheld without refund.
- After successful verification, the purchased contact is displayed immediately on the website.
- Helper does not connect Telegram during purchase and receives no Telegram delivery of the paid contact.
- There is no user-facing purchase recovery code and no promised later reopening of the contact.
- A separate single-use review code/link may be issued after reveal and cannot reveal the contact.

### Fixed (2026-07-19)

- Exactly one structured text contact per listing: Telegram username/link or Signal link. No image. No multiple contacts. Immutable for five days.
- A fresh service Telegram connection is required for each publication window (first and every reactivation). The previous window's binding is deleted when the window closes. Client may use a different Telegram account per window. Successful bot response at `/start` is the delivery proof.
- Client display name is listing-scoped, generated randomly at first publication, stable for five days, new for each new paid listing, and is not a persistent identity.

### Fixed (Task 06 — 2026-07-21)

- Client has a persistent wallet-bound reputation profile (v2_client_profiles): created at first $5 payment confirmation, anchored to HMAC-SHA256(serverKey, "naroom:v2:wallet:" + chain + ":" + wallet). Carries positive_count and negative_count. Not a public pseudonym or login credential. Same wallet+currency shares one profile across all listings.
- Reviews are bidirectional and binary (positive/negative). One review per purchase per reviewer side. Exact repeat is idempotent; conflicting re-use is 409.
- Client → Helper review: Telegram inline button (Positive / Negative — no Skip button) sent at purchase contact_ready. Callback payload fits Telegram 64-byte limit. Expiry: 24 h from contact_ready_at. Client skips by not pressing. Missing or inconsistent Client binding at purchase creation → hard failure (ErrReviewNoBinding, HTTP 409 `client_notification_unavailable`, full rollback, zero orphan rows).
- Helper → Client review: review token (66-char HMAC-signed opaque string) issued via POST /v2/helper/reviews/capability (NOT returned automatically in the contact-ready response). POST /v2/helper/reviews/capability + POST /v2/helper/reviews. Expiry: 24 h from contact_ready_at. Wrong token and wrong wallet return byte-identical 404. Cache-Control: no-store on both endpoints.
- Review entitlements created atomically with purchase_count++ inside the contact_ready transition (one DB transaction). Both sides created at the same moment; one row per (purchase_id, reviewer_side) with UNIQUE constraint.

### Unresolved

- Whether Client reputation transfers between wallets or listings.
- Whether Client/Helper roles are fixed.
- Contact and payment-record retention periods.
- Same-browser behavior when the page is refreshed or closed while payment confirmation is pending.
- Whether Informer eligibility is rechecked periodically and whether one subscription may change or include multiple selected cities.
- Authoritative BTC/LTC funding-wallet resolution for multi-input or custodial transactions.
- Whether the contact remains visible after an ordinary refresh of the same result page or is strictly shown once.

## 18. Change Log

- 2026-07-21: Task 06-FIX — Closed 7 blockers from Task 06 review. (1) **Binding gate restored**: missing/expired/inconsistent Client binding → `ErrReviewNoBinding` (HTTP 409 `client_notification_unavailable`), full tx rollback, zero orphan rows; `snapshotClientDestinationTx` now requires `state=active`, `valid_until>now`, destination exists, `d.expires_at=b.valid_until`; soft-skip removed. (2) **Two-phase snapshot lifetime**: Phase 1 (`awaiting_contact_ready`) expires at `detection_deadline_at+86400`; Phase 2 (`pending_send`) activated at contact_ready with `expires_at=contact_ready_at+86400` via CAS touching exactly 1 row; mismatch → rollback. (3) **Client reputation in public DTO**: `PublicListingView` and `publicListingJSON` now include `client_reputation:{member_since,positive_count,negative_count}` via LEFT JOIN with `v2_client_profiles`; reads live counts directly; subsequent reads after committed review see increment immediately. (4) **Telegram callback validation hardened**: before any DB mutation: `cq.ID!=""`, message non-nil, chat non-nil, `chat.type=="private"`, `chat.id>0`, `message_id>0`; malformed → neutral 200, zero mutation, zero Bot API call. (5) **Schema `CHECK (expires_at = created_at + 86400)`**: in `v2_review_entitlements`; `createReviewEntitlementsTx` uses `created_at=contact_ready_at` so equation holds by construction. (6) **Tests rewritten/added**: `TestReview_NoBillNoOrphanRows` now asserts `ErrReviewNoBinding`+rollback; new: `TestReview_ExpiredBindingBlocksPurchase`, `TestReview_InconsistentBindingBlocksPurchase`, `TestReview_ConcurrentContactReady`, `TestReview_ConcurrentClientProfileCreation`, `TestReview_PublicReputationCounts`, `TestHelperHTTP_NoBindingReturns409`, `TestWebhookCallbackMalformedMatrix` (13 cases); all helper tests now create active binding via `mustInsertActiveBindingForFlow`. (7) **PRODUCT_SPEC.md**: removed Skip button, fixed client fingerprint domain to `naroom:v2:wallet:`, fixed review token source (capability endpoint, not contact-ready), corrected `id` vs `review_ref` pseudo-schema, described two-phase snapshot lifetime, updated binding-missing contract. All 0 FAIL 0 SKIP under `-race` and `go test ./...`.

- 2026-07-21: Task 06 — Bidirectional wallet-bound reputation and one-time post-purchase reviews. New schema tables: `v2_client_profiles` (wallet_fingerprint UNIQUE 64-hex, currency BTC|LTC, positive_count, negative_count), `v2_review_entitlements` (UNIQUE(purchase_id, reviewer_side), rating NULL until consumed, 24h window from contact_ready_at), `v2_review_delivery_snapshots` (snapshot lifecycle: awaiting_contact_ready → pending_send → sent|permanent_failure; encrypted chat_id NULLed after delivery). New files: `internal/v2/review_service.go` (ReviewService with GetHelperReviewCapability, SubmitHelperReview, createReviewEntitlementsTx, snapshotClientDestinationTx), `internal/v2/review_http.go` (HelperReviewHandler: POST /v2/helper/reviews/capability, POST /v2/helper/reviews; Cache-Control: no-store; byte-identical 404 for wrong token/wallet; idempotent repeat 200; conflicting re-use 409; rate limiter 10 req/min per IP). Modified: `schema.sql` (added 3 tables + covering indexes + v2_client_flows CHECK client_profile_id NOT NULL for non-awaiting_payment states), `service.go` (CreatePurchase creates client profile atomically with payment confirmation), `helper_service.go` (setHelperContactReady calls createReviewEntitlementsTx; snapshotClientDestinationTx soft-fails if no active Client binding — purchase proceeds without review snapshot), `telegram_transport.go` (HandleWebhook processes "rv:<hex>:<action>" review callbacks; CAS consume + profile counter increment + edit message). Key decisions: (1) Client profile created at $5 payment confirmation, not at listing publication; wallet_fingerprint domain prefix "naroom:v2:wallet:" distinct from Helper prefix. (2) Review expiry: 24 h from contact_ready_at, both directions. (3) Helper review token: base64url(raw16) + "." + base64url(HMAC) = 66 chars; Telegram review callback_data: "rv:" + hex(raw16) + ":" + action ≈ 37 chars (fits 64-byte Telegram limit). (4) Missing Client binding at purchase creation → ErrReviewNoBinding, HTTP 409, full rollback [CORRECTED in Task 06-FIX]. (5) Privacy: review tokens never logged; wallet_fingerprint and chat_id never in error responses; encrypted fields NULLed post-delivery. Tests: 21 review service tests + 16 HTTP tests (all 0 FAIL 0 SKIP under -race and go test ./...). Updated §8.2, §8.3, §13, §17, §20, §22, §24.2.

- 2026-07-21: Task 05-FIX2 — Closed four P1 blockers left open by Task 05-FIX. (1) **Exact create idempotency before external calls**: `LookupPurchaseByToken` lookup added to `handleCreate` BEFORE balance-provider and invoice-issuer calls; keyed in-process lock serializes concurrent same-token requests so only one winner calls provider/issuer; loser re-reads under lock and returns `200`. Lock key is `HMAC(token)`, never raw token. Partial UNIQUE index `uniq_v2_helper_purchases_active ON v2_helper_purchases(helper_profile_id, listing_id) WHERE state NOT IN (...)` enforces at most one non-terminal purchase per pair at DB level; INSERT collision → `ErrHelperDuplicateActivePurchase` (409). (2) **Real reveal CAS miss**: `_testRevealHook func(tx *sql.Tx, purchaseID string) error` field added to `HelperPurchaseService`; hook fires inside RevealHelperContact's transaction BEFORE the CAS UPDATE, writes `first_revealed_at` via the same tx so the UPDATE sees `RowsAffected==0` deterministically. `TestHelperReveal_DeterministicCASMiss` rewritten: hook called flag verified, CAS-miss path exercised (not the pre-injected already-revealed path), exact winner expiry returned. (3) **Strict schema positive/negative tests for all 8 purchase states**: fixed 6 existing negative subtests that were passing due to UNIQUE masking (added fresh profiles so target CHECK constraint fires); added `awaiting_payment_ok`, `awaiting_payment_with_balance_deadline_rejected`, `payment_detected_ok`, `payment_confirmed_missing_retry_deadline_rejected`, `invoice_expired_ok/with_contact_ready_at_rejected`, `failed_ok/with_contact_ready_at_rejected`, `receipt_expired_ok_unrevealed/ok_revealed/missing_contact_ready_at_rejected`, `partial_unique_terminal_does_not_block_new`, `receipt_expires_at_wrong_equation_rejected`, `invoice_confirmation_deadline_wrong_equation_rejected`, `invoice_confirmed_at_before_payment_detected_rejected` (15 new subtests). (4) **Five HTTP create idempotency/concurrency tests** in `TestHelperHTTP_ConcurrentCreate`: sequential retry (provider=1, issuer=1, rows 1/1/1, second call 200); concurrent same-token ×8 (1×201, 7×200, same purchaseID, provider=1, issuer=1); same token+different wallet (404, provider=1 total, no second provider call); concurrent different-token same pair (1×201, 3×409, 1 active purchase); terminal+new token (201, new purchaseID, same profile, provider=2, issuer=2). Synced §14 FIXED, §17 Unresolved (removed country-lock commit and low-balance retry), §23.4 (split backend capability FIXED from frontend UX Task 08). All tests 0 FAIL 0 SKIP under `-race` and `go test ./...`.

- 2026-07-20: Task 05-FIX — Closed seven P1 blockers identified in the Task 05 review. (1) **Precheck no orphan rows**: `GetOrCreateProfile` moved inside the write transaction; low-balance, issuer failure, and listing/country race leave zero new profile/purchase/invoice rows. (2) **Idempotent create**: browser-generated 64-hex `purchase_token` stored as `HMAC("naroom:v2:helper-browser-token:", token)`; same token+wallet+listing returns existing purchase (200, no token in response); different token for same profile+listing while non-terminal returns `ErrHelperDuplicateActivePurchase` (409); new token allowed after terminal. (3) **Atomic expiry**: `ExpireHelperInvoice` updates invoice and purchase in one transaction; rollback on any failure. (4) **Country CAS loser → failed**: when `setHelperContactReady` country CAS returns 0 rows, purchase transitions to `failed` in the same transaction; contact_ready_at stays NULL; purchase excluded from watcher. (5) **Reveal CAS miss single-commit path**: CAS loser re-reads winner's `receipt_expires_at` inside the tx; single commit; zero-expiry/double-commit branch removed. (6) **Strict schema CHECKs**: 64-hex IDs and HMAC fields, 2-uppercase country codes, non-negative bounded balance, state-specific required/forbidden fields (all 8 purchase states), timestamp ordering on invoices. (7) **Stable HTTP error codes**: closed set of snake_case `code` constants in `{"error":"…","code":"…"}` envelope; NaN/Inf/negative balance → 503 `balance_provider_unavailable`, no rows created. No-store headers on reveal set before capability check. Note: `_testRevealHook` used a pre-injection approach in this version that exercised the already-revealed early-exit path rather than the true CAS-miss branch; corrected in Task 05-FIX2. Added 10 new tests: `TestHelperSchema_StrictConstraints` (16 subtests), `TestHelperCreate_NoOrphanRowsOnFailure` (3 subtests), `TestHelperCreate_Idempotent`, `TestHelperExpiry_AtomicRollback`, `TestHelperCountryCASLoser_AtomicFailed`, `TestHelperReveal_DeterministicCASMiss`, `TestHelperReveal_RealCipherBothTypes` (telegram+signal), `TestHelperHTTP_NaNBalanceIs503` (3 subtests), `TestHelperHTTP_IdempotentCreate`, `TestHelperHTTP_ErrorCodes`. Synced §6, §20, §23.2, §23.4, §23.5, §24.2 with confirmed FIXED contracts. Removed stale UNRESOLVED items for country lock, recheck deadline, late payment, same-browser token, receipt window. All tests 0 FAIL 0 SKIP under `-race` and `go test ./...`.

- 2026-07-20: Task 05 — Complete V2 Helper contact purchase backend. New files: `internal/v2/helper_service.go`, `internal/v2/helper_watcher.go`, `internal/v2/helper_http.go` (production); `internal/v2/helper_service_test.go`, `internal/v2/helper_watcher_test.go`, `internal/v2/helper_http_test.go` (tests). Schema additions (appended to `schema.sql`): `v2_helper_profiles` (wallet_fingerprint UNIQUE, currency BTC|LTC, public_name UNIQUE, country_code nullable, purchase/positive/negative counts), `v2_helper_invoices` (status enum pending/detected/confirmed/expired, detected_txid_hash 64-hex nullable, all state-field consistency CHECKs), `v2_helper_purchases` (state 8-enum, browser_token_hash UNIQUE, country_code_snapshot, contact_ready_at, first_revealed_at, receipt_expires_at, balance_retry_deadline_at, timestamp-pair CHECKs); 6 covering indexes. Domain-separation HMAC prefixes: `"naroom:v2:helper-wallet:"`, `"naroom:v2:helper-browser-token:"`, `"naroom:v2:helper-txid:"` (distinct from Client prefixes). Raw txid never stored — only `HMAC-SHA256(txid)` is persisted; watcher computes hash of each candidate tx to match. Browser token returned once at create (rawToken), stored only as HMAC hash; wallet fingerprint alone does not authorize. Country CAS: `UPDATE … WHERE country_code IS NULL OR country_code = ?`; 0 rows = `ErrHelperCountryMismatch`. Contact decrypt AAD uses Client `flow_id` from `v2_listings.flow_id`, not purchase ID. First reveal: CAS `WHERE first_revealed_at IS NULL`, sets `receipt_expires_at = now + 24h`; idempotent on retry. Balance floors: $1010 pre-invoice (HTTP handler), $1000 post-payment (service). `balance_retry_deadline_at = confirmation_deadline_at + 86400s`; deadline equality is allowed; strictly after = expired. `NormalizeHelperExpired` covers 5 transitions in one tx (pending→expired, detected→expired, confirmed+low_balance+overdeadline→failed, contact_ready+unrevealed+over24h→receipt_expired, revealed+over_expiry→receipt_expired). HelperPurchaseWatcher mirrors V2Watcher: `ProcessOnce`/`Run`/`cappedBackoff`/`CycleResult`. HTTP handler (`HelperPurchaseHandler`): 4 POST endpoints with HMAC-IP rate limiters (separate buckets); strict body (application/json, 4 KiB, no unknown fields, no trailing JSON); wrong token/wallet → identical 404; unavailable listing → identical 404; provider/issuer → 503; DB/cipher → 500. Reveal sets `Cache-Control: no-store, private`, `Pragma: no-cache`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`. 35 regression tests (schema, privacy, domain separation, create/precheck, watcher payment flow, country concurrency, browser capability, receipt lifecycle, HTTP discipline, full lifecycle). All pass 0 FAIL 0 SKIP under `-race` and `go test ./...`. Does NOT implement reviews, Informer, or production routing.

- 2026-07-20: Task 04C-FIX — Closed two P1 defects in `buildRestoreNav` and tightened 11 acceptance tests. (1) **Fix 1 (terminal expiry before listing/Telegram)**: added entitlement boundary guard after invoice terminal statuses but before paid_low_balance branch and Telegram/listing lookups — now `form_ready` or `payment_confirmed`/`paid_low_balance` flows with expired entitlement return `finished/start_new_listing` (HTTP 200, `listing=null`) instead of 500. (2) **Fix 2 (visible telegram_status)**: `buildRestoreNav` now calls `QueryLinkStatus` for effectively visible listings and sets `telegram_status=active` on the composite response, matching the published destination state. Test changes: `TestListingHTTPWrongCodeWrongWallet` adds correct-code+wrong-wallet subcase; `TestListingHTTPStrictConstraints` covers unknown field for publish; `TestListingHTTPCapabilityNotInPath` covers publish and reactivate query-string bypass; `TestListingHTTPRateLimitBuckets` adds `fail_closed_at_max_entries` subtest that fills `defaultMaxLimiterEntries` unique IPs and verifies the next IP is denied; new `TestListingHTTPPublishConcurrency` — two goroutines simultaneously → exactly one 201 and one 409, one listing row, activation_count=1; new `TestListingHTTPReactivateOldWindowBinding` — consumed window-1 binding does not satisfy window-2 requirement; `TestListingHTTPPublicDetail` adds hidden/expired/malformed subtests with identical 404 body; new `TestListingHTTPRestoreFormReadyExpired` — form_ready + expired entitlement → 200 finished/start_new_listing listing=null; new `TestListingHTTPRestorePaidLowBalanceExpired` — paid_low_balance + expired → terminal override; new `TestListingHTTPRestoreVisibleTelegramActive` — visible listing → telegram_status=active; `TestClientJourneyLifecycle` step-s assertion tightened to require exactly 410 (not 409 or 410) and strict binding window_number/state/destination count after reactivation. All pass 0 FAIL 0 SKIP under -race and `go test ./...`.
- 2026-07-20: Task 04C — Backend HTTP Client journey orchestration complete. New file `internal/v2/listing_http.go` adds `ClientJourneyHandler` (Routes test-only, not wired to cmd/). Five endpoints: `POST /v2/client/listings/restore` (composite navigation response with 8 phases and 8 next_actions), `POST /v2/client/listings/publish` (first publication, 201 safe ListingView, stable 409 on retry), `POST /v2/client/listings/reactivate` (server-side balance from injected ClientBalanceReader, $120 hard floor, atomic CAS, concurrent-safe), `GET /v2/board/{city}` (only effectively visible listings, deterministic order newest-activated-first then ID ASC, empty array not null), `GET /v2/listings/{listing_id}` (visible only, identical 404 for all non-visible/unknown/malformed). Added `GetPublicListing` to ListingService and `ORDER BY last_activated_at DESC, id ASC` to `BoardQuery`. Stable error code matrix (12 codes). Rate limit: restore 10/min, publish/reactivate 5/min, board/detail 30/min, separate HMAC buckets per IP, bounded maps. Privacy: management_code/wallet/contact/binding_ref/flow_id/fingerprint/chat_id never in responses or logs. Wrong code and wrong wallet return identical 404. Browser cannot supply balance. 27 tests (constructor, strict constraints, capability-in-path rejection, rate limit buckets, all 8 phase/next_action mappings, stale-visible effective-hidden without mutation, deadline-equality finished boundary, response privacy, publish success/invalid/missing-binding/idempotency, reactivate server-balance/provider-503/floor/binding-preserved/missing-binding/success/concurrency/final-expiry, board empty/content/order, detail visibility/privacy, full E2E lifecycle). All pass 0 FAIL 0 SKIP under -race and `go test ./...`. Does NOT close Helper/reviews/Informer/retention or production routing.
- 2026-07-19: Task 04B-FIX3 — Closed final boundary cases in Telegram transport: exact lease ownership, verifiable permanent-expiry cleanup, clock placement, superseded-handler isolation, and collision-exhaustion spec. (1) All post-send DB predicates now use exact three-part ownership: `token_hash + state='processing' + lease_until=newLease`; a stale handler that finds `sql.ErrNoRows` on this predicate returns neutral 200 and writes nothing. (2) `cleanupExpiredAttemptTx` helper added: DELETE with exact predicate, checked `RowsAffected == 1`, checked `Commit`; RowsAffected=0 → neutral 200; any error → 503. No `//nolint:errcheck` on security/state-transition operations. (3) Fresh `finalizeNow = t.now()` is taken **after** ref generation/encryption and **before** `db.Begin` (not at the top of the iteration). (4) Post-send collision exhaustion leaves attempt `processing` (not reset to pending) so an immediate retry cannot re-send the Telegram message. (5) `NewHTTPBotAPISender` now also rejects non-origin paths (non-empty path other than `/`), `RawPath`, and `Opaque`. (6) §11.2 updated: exact lease ownership rules, permanent-expiry cleanup algorithm, corrected collision-exhaustion behavior, clock placement description. Added 7 new or rewritten regression tests (superseded-handler, replacement-attempt isolation, clock-boundary 503, injected DELETE failure via SQLite trigger for both expiry paths, strengthened bounded-collision assertions, URL path table cases). All 0 FAIL, 0 SKIP under -race.
- 2026-07-19: Task 04B-FIX2 — Closed remaining boundary cases in Telegram transport. (1) `CreateLink` DELETE predicate now requires `expires_at > now` in the protected condition so an expired-token processing attempt is replaceable even with a live lease; UNIQUE(flow_id) insert collision surfaces as `ErrConflict`. (2) Webhook finalizer uses strict half-open lease interval (`finalizeNow < lease_until`); `finalizeNow` is refreshed at the start of each collision-retry iteration; permanently expired token or entitlement at `finalizeNow` atomically deletes the exact claimed attempt and returns neutral 200 (instead of 503). (3) `TestWebhookDBFailureAfterSuccessfulSendRecoversAfterLease` replaced Go error hook with a real SQLite `BEFORE INSERT` trigger on `v2_telegram_destinations` — verifies binding rollback inside finalization transaction. (4) `SendMessage` checks HTTP status class before reading body: 429 and 5xx are always retryable even with oversized body; oversized check applies only to 2xx. (5) `QueryLinkStatus` JOIN requires `d.expires_at = b.valid_until` — mismatched future expiries are not reported as ready/active. (6) `TestWebhookPermanentSendFailureReturns200` uses same rawToken with different positive chat.id (not a new flow). (7) Webhook secret comparison uses SHA-256 digests (fixed-length); misleading HMAC comment removed. (8) `NewHTTPBotAPISender` validates host, no userinfo/query/fragment; `botUsernameRe` requires 5-32 chars plus `bot` suffix; private `chat.id ≤ 0` returns neutral 200. Added 12 new regression tests. All 500+ tests pass 0 FAIL, 0 SKIP under -race.
- 2026-07-19: Task 04B-FIX — Hardened Telegram transport and listing service. (1) Injectable `bindingRefGenerator` type; `defaultBindingRefGen` replaces old `makeBindingRef`; nil clock now rejected by `NewTelegramTransport`. (2) `NewHTTPBotAPISender` enforces HTTPS baseURL. (3) `SendMessage` rejects oversized responses (`> 64 KiB`) as permanent delivery failure. (4) `HandleWebhook` adds trailing-JSON detection (second decoder pass), `chat.id == 0` guard, fresh post-send clock (`finalizeNow`), and bounded `binding_ref` collision retry loop (`maxBindingRefRetries = 5`). (5) Single `validUntil = min(finalizeNow+15min, entitlementExpiresAt)` written to both `binding.valid_until` and `destination.expires_at`. (6) `attachBindingAndDestinationTx` detects UNIQUE collision on `binding_ref` and returns `errBindingRefCollision`. (7) `CreateLink` is now fully atomic (all reads and mutations inside one transaction). (8) `QueryLinkStatus` uses INNER JOIN on destinations — bare bindings without a non-expired destination row are not reported as ready/active. (9) `FirstPublish` and `Reactivate` enforce strict destination existence (return `ErrDestinationMissing` on missing active binding or missing/zero destination rows, roll back on `rowsAffected != 1`). (10) Added `_testAfterSendHook` for post-send DB failure injection in tests. Tests: 11 new tests across 4 test files; all 466 tests pass under `-race`.
- 2026-07-19: Task 04B — Implemented Telegram transport for V2 delivery path. Added `v2_telegram_link_attempts` and `v2_telegram_destinations` tables (schema.sql). New files: `telegram_transport.go` (token primitives, DestinationCipher, HTTPBotAPISender, TelegramTransport with CreateLink/QueryLinkStatus/HandleWebhook), `telegram_http.go` (TelegramLinkHandler with 3 endpoints, rate limiters). Refactored `AttachReadyBinding` to unexported `attachReadyBinding`; updated `FirstPublish` and `Reactivate` to propagate destination expires_at. Fixed `OpenMemory` to explicitly enable SQLite foreign keys via PRAGMA (cascade deletes now work). Added §11.2 to PRODUCT_SPEC.md. Tests: 35 new tests across 3 test files covering token format, schema constraints, cipher AAD binding, webhook state machine (CAS lease, concurrent safety, secret validation), HTTP endpoints (rate limits, strict body), and full-DB privacy scan.
- 2026-07-19: Task 04A-VERIFY — Added 7 direct SQLite schema regression tests for binding_ref CHECK/UNIQUE contract (wrong prefix, uppercase suffix, non-hex suffix, wrong length, duplicate ref on different flow, verified_at > created_at, active activated_at > updated_at). Updated stale comment in TestSchemaCheckConstraintsNegative. Removed "ownership verification" from §11 "Contact text versus image" DECIDED rationale (format-only is the V2 contract).
- 2026-07-19: Task 04A-FIX — Closed three P1 review blockers: (1) Strengthened SQLite binding_ref CHECK from insufficient LIKE to substr+negative GLOB; added UNIQUE(binding_ref); completed timestamp constraints (verified_at ≤ created_at ≤ updated_at; active: activated_at ≤ updated_at). (2) Deleted deprecated AttachBinding wrapper entirely. (3) Rejected percent-encoded Telegram paths (RawPath check). Removed contact ownership/format verification from §5 Unresolved, §11 UNRESOLVED, §17 Unresolved, §20 Decision Queue (moved to post-launch non-blocking note), and §21.2 completeness blockers. Removed already-resolved "Exact proof that the daily reactivation wallet" from §17 Unresolved (resolved by management_code + wallet_fingerprint contract).
- 2026-07-19: Task 04A — Fixed four decisions: (1) external Telegram/Signal contact format-only validation in first release; ownership not verified, platform UI informs Client of responsibility. (2) Service Telegram fresh-per-window: a new connection is required for first publication and every daily reactivation; previous window's binding deleted on expiry; Client may use a different account per window. (3) Successful bot response at `/start` is the delivery proof gating publication — no separate test message. (4) Contact ownership verification deferred to future release. Removed resolved entries from §3.3, §5 journey steps 13/19, §19.2, §20 Decision Queue (item 3 removed, item 4 scoped), §21.2 (item 1 removed). Added `ProductionContactValidator` (format-only, no network), new `ready/active` binding lifecycle with 15-min TTL, atomic FirstPublish/Reactivate CAS transactions, and `NormalizeExpired` binding cleanup.
- 2026-07-19: Task 04S — Expanded display-name namespace from 40 000 to 2^128 (128-bit random suffix). Removed resolved entries from §5 unresolved list and §20 Decision Queue.
- 2026-07-19: Task 04R — Fixed three decisions left open after Task 04 review: (1) exactly one structured text contact (Telegram or Signal), no image, no multiple contacts, immutable five days; (2) service Telegram binding reused automatically while active/deliverable, re-bind only on absence/expiry/invalidation/failure; (3) display name listing-scoped, stable five days, new for new paid listing, not persistent identity. Removed contradicting UNRESOLVED entries from sections 3.3, 8.1, 17, 19.2, and 21.2. Adapter tests use in-memory injected transport, not httptest.NewServer or TCP skip.
- 2026-07-17: V2 document created from the product discussion.
- 2026-07-18: Task 03A — Fixed 6 blockers: btcutil address validation (BTC+LTC, all types), atomic CAS ExpireInvoice (single SQL with inline deadline check), real bounded backoff via CycleResult (Run manages backoff once per cycle, ProcessOnce is stateless), corrected HD derivation contract (shared counter is safe; separate counter would collide), real adapter tests via in-memory injected transport (roundTripFunc), SQLite CHECK constraints for all 4 invoice status/field configurations.
- 2026-07-18: Task 03 — V2 invoice state machine implemented. Added fixed rules for detection window (60 min), confirmation grace (24 h), expiry transitions, exact-amount enforcement, sender verification, post-payment balance check, bounded watcher backoff. Removed "Whether V2 launch supports both BTC and LTC" from UNRESOLVED (both are supported).
- 2026-07-17: Fixed five consecutive calendar days and initial/daily Client balance checks; added Telegram Helper-summary/review concept and contact text-vs-image decision.
- 2026-07-17: Split Client balance policy into public $150 requirement and backend $120 hard acceptance floor.
- 2026-07-17: Added Helper Telegram Informer, $1,000 public-address registration gate, wallet-bound Helper profile, country lock, and post-payment $1,000 funding-wallet rule.
- 2026-07-17: Corrected Informer scope: Telegram is notification delivery only and is not Helper identity, authorization, purchase, contact, or review infrastructure.
- 2026-07-17: Separated Helper onboarding from Informer completely; Helper now begins at contact purchase with an entered own wallet, same-wallet payment enforcement, and informational country notice.
- 2026-07-17: Added immediate secure website contact result; raw paid contact is not sent through Telegram.
- 2026-07-17: Removed Helper Telegram and user-facing purchase recovery code; contact is shown immediately on the website, while a separate code/link may authorize only a later review.
- 2026-07-17: Fixed Client payment continuity: wallet-bound payment intent and secret code are created before the $5 payment; the same code restores the invoice and becomes the listing management code after confirmation.
- No code, database, test, deployment, or V1 behavior changed.

## 19. V2 Listing State Machine - Working Draft

This state machine contains only agreed transitions plus explicitly marked unresolved branches.

### 19.1 Creation and payment

```text
PAYMENT_INTENT_CREATED
  -> PAYMENT_AND_LISTING_CODE_ISSUED
  -> AWAITING_5_USD_PAYMENT
  -> PAYMENT_CONFIRMED
  -> PAID_LISTING_CONTAINER_CREATED
  -> POST_PAYMENT_BALANCE_CHECK
```

Before `PAYMENT_CONFIRMED`, no public or editable listing exists. The secret code plus the original wallet restores only the private payment intent and invoice state. After confirmation, the same code becomes the management code for the resulting paid listing, and listing creation begins.

Balance result:

```text
balance >= $120
  -> LISTING_FORM
  -> ENTITLEMENT_READY

balance < $120
  -> PAID_LOW_BALANCE
```

In `PAID_LOW_BALANCE`, Client may top up the same wallet and retry any number of times before the absolute five-day deadline. The five-calendar-day clock already started at payment confirmation and does not pause. There is no refund.

### 19.2 Telegram and first publication

```text
ENTITLEMENT_READY
  -> TELEGRAM_CONNECTION_STEP
  -> DAILY_VISIBLE
```

A new Telegram connection is mandatory for first publication and for every daily reactivation. Each 24-hour window requires its own fresh short-lived binding; the previous window's binding is not reused and is deleted when the window expires. Successful bot response at `/start` (delivery proof) gates the publication transaction. Client may connect a different Telegram account for each window. The service Telegram connection is independent of the external Telegram/Signal contact that Helpers purchase.

When first publication starts:

```text
visible_until = min(now + 24 hours, entitlement_expires_at)
```

### 19.3 Daily expiry and reactivation

```text
DAILY_VISIBLE
  -> DAILY_HIDDEN             when visible_until is reached
  -> ENTITLEMENT_FINISHED     when entitlement_expires_at is reached
```

From `DAILY_HIDDEN` or `PAID_LOW_BALANCE`, Client supplies:

```text
listing management code
original listing wallet address
```

Server checks:

```text
management code hash matches
wallet fingerprint matches listing
current time < entitlement_expires_at
current balance >= $120
```

Result:

```text
all checks pass
  -> DAILY_VISIBLE

balance < $120
  -> DAILY_BLOCKED_LOW_BALANCE

five-day deadline reached
  -> ENTITLEMENT_FINISHED
```

From `DAILY_BLOCKED_LOW_BALANCE`, Client may top up the same wallet and retry before the absolute five-day deadline. This does not add or restore calendar time.

### 19.4 Contact purchases

While state is `DAILY_VISIBLE`:

```text
Helper opens listing
  -> OWN_WALLET_ENTERED
  -> PROFILE_COUNTRY_AND_PRE_INVOICE_BALANCE_CHECKED
  -> INFORMATIONAL_COUNTRY_AND_BALANCE_NOTICE_SHOWN
  -> CONTACT_INVOICE_PENDING
  -> CONTACT_PAYMENT_CONFIRMED
  -> EXPECTED_WALLET_AND_POST_PAYMENT_BALANCE_VERIFIED
  -> CONTACT_RECEIPT_READY
```

Each purchase is independent. `CONTACT_RECEIPT_READY` does not change the listing state, daily timer, or five-day deadline. Wrong-sender or post-payment low-balance results withhold the contact without refund. Full Helper transitions are defined in section 23.

`FIXED (Task 05-FIX)`: If the $10 invoice is created while the listing is visible but confirms after the daily window or five-day entitlement expires, the contact is still revealed. Listing visibility state is checked only at invoice creation time, not at payment confirmation or reveal.

### 19.5 Final expiry

```text
now >= entitlement_expires_at
  -> ENTITLEMENT_FINISHED
```

In `ENTITLEMENT_FINISHED`:

- management code cannot publish the listing again;
- no new $10 contact invoice can be created;
- pending $10 invoice behavior remains unresolved;
- contact ciphertext deletion deadline remains unresolved;
- a new $5 listing is required.

## 20. Decision Queue - Required Before V2 Implementation

Priority order is based on dependency, not recommendation.

`FIXED (Task 05-FIX)`

- **Helper country lock:** committed at the first successful sender verification and post-payment balance check (≥ $1,000). Enforced by CAS update: if the profile already holds a different country code the purchase transitions atomically to `failed`. Section 23.5.
- **Helper payment authority (retry):** a Helper who paid but holds < $1,000 post-payment may call `/recheck-balance` until `confirmation_deadline_at + 24 h`; after that deadline the purchase moves to `failed`. The authoritative BTC/LTC funding-wallet resolution rule remains unresolved (see §23.2 CONSTRAINT).
- **Late payment and reveal:** if payment confirms after the listing's daily window or five-day entitlement expires, the contact is still revealed. Listing visibility is checked only at the time of invoice creation, not at confirmation or reveal.
- **Same-browser Helper continuity:** browser-generated `purchase_token` (64 lowercase hex) is returned once in the `201` create response and stored only as an HMAC hash. Restore/recheck/reveal use token + original wallet. Same token + same wallet = idempotent 200.
- **Receipt window:** `receipt_expires_at = first_revealed_at + 24 h`.

`FIXED (Task 06)`

- **Client identity and reputation (was item 1):** Client has a persistent wallet-bound profile (v2_client_profiles) created at $5 payment confirmation. Carries positive_count and negative_count. HMAC-keyed fingerprint. Not a public pseudonym.
- **Reviews (was item 4):** Both directions implemented. Client → Helper via Telegram inline button (24 h window). Helper → Client via HMAC review token on website (24 h window). One review per purchase per side. See §13 and §22.

`UNRESOLVED`

1. **Role model:** fixed Client/Helper identities or both actions available to one wallet-bound profile.
2. **Authoritative BTC/LTC funding-wallet resolution:** multi-input, custodial (exchange), and CoinJoin-like transactions.
3. **Retention:** contact ciphertext, invoices, management-code hash, temporary purchase links, aggregate reputation, and expired listings.
4. **Informer operation:** selected-city changes, multiple-city subscriptions, and periodic balance recheck policy.

No coding brief for the role model is valid until item 1 above is fixed. Retention and Informer additionally require items 3-4.

`POST-LAUNCH (non-blocking)`

- **Contact ownership verification (future):** Telegram ownership may be verifiable via a bot flow in a future release. Signal has no equivalent automated flow. Deferred; format-only validation is the V2 contract.

## 21. Client Journey Completeness Audit

### 21.1 Parts that are already coherent

- Client can create one structured listing.
- Client pays $5 once.
- Five-calendar-day clock starts at confirmed payment and never pauses.
- Public UI states $150; backend hard floor is $120.
- A low balance hides/blocks publication but does not cancel payment or stop the calendar clock.
- Client can top up and request another check before the final deadline.
- Listing management code and original wallet address jointly authorize each daily activation.
- Every successful activation lasts at most 24 hours and never beyond the final deadline.
- Contact purchases do not remove the listing.
- Any number of purchase attempts may reveal the contact for $10.
- No internal conversation or message history exists.

### 21.2 Client path still not fully closed

The Client journey is not implementation-complete until these points are fixed:

1. Exact Telegram notification generated by a $10 purchase.
2. Whether persistent Helper reputation data is allowed in that notification.
3. Client-to-Helper review lifetime and interaction.
4. What happens to a pending $10 invoice when daily/final listing visibility expires.
5. Deletion deadlines for encrypted contact, invoice records, and review tokens.

Until the remaining open items are fixed, the Client flow is understandable but not yet a complete coding contract.

## 22. Telegram Purchase Notification and Client Review

`FIXED (Task 06)`

### 22.1 Purchase event

After a Helper's $10 payment is confirmed and `contact_ready` state is reached:

1. Contact is revealed to that purchase attempt (website).
2. Listing remains visible and unchanged.
3. Server atomically creates both review entitlements (client-side and helper-side) and increments the Helper's purchase_count in the same transaction.
4. Client Telegram bot receives a message (active binding required; `FIXED Task 06-FIX`: missing/expired/inconsistent binding → `ErrReviewNoBinding` → HTTP 409 `client_notification_unavailable`, full transaction rollback, zero orphan rows — the soft-skip was removed).

Message shape (implemented):

```text
NA Room: ваш контакт куплен.

Helper: <public pseudonym>
На платформе: <N days>
Подтверждённых покупок: <N>
Положительных отзывов: <N>
Отрицательных отзывов: <N>

Оцените Helper после общения:
[Положительно] [Отрицательно]
```

This message uses only the Helper's public pseudonym and aggregate counters. It must not contain Telegram identity, wallet address/fingerprint, listing management code, Client contact, transaction inputs, or internal principal identifiers.

### 22.2 Rating timing

The Telegram message is sent at purchase `contact_ready` time. Client communicates externally first and may return to the existing Telegram message later to rate.

Review window: 24 hours from purchase `contact_ready_at` for both directions.

### 22.3 Telegram callback security

`FIXED (Task 06)`

Buttons contain only an opaque single-use callback token. They never contain wallet hash, contact, listing management code, `helper_id`, or plaintext purchase data.

Telegram callback_data format: `"rv:" + hex(raw16bytes) + ":" + action` — approximately 37 characters, within Telegram's 64-byte limit.

Server stores in `v2_review_entitlements`:

```text
id            — internal row ID (64-char hex)
review_ref    — opaque token key ("rev_" + 32 lowercase hex chars); stored hashed
purchase_id
reviewer_side   — "client" | "helper"
target_profile_id — the profile whose counters are incremented
rating          — NULL until consumed
expires_at
consumed_at     — NULL until consumed
```

Callback validation (before any DB mutation): non-empty callback query ID; `rv:<32 lowercase hex>:p|n` data format; non-nil message and chat; `message_id > 0`; `chat.type == "private"`; `chat.id > 0`. Malformed/missing → neutral 200, zero mutation, zero Bot API call.

Delivery snapshot two-phase lifetime:
- **Phase 1 (`awaiting_contact_ready`)**: created atomically with the purchase inside `CreatePurchase`. `expires_at = detection_deadline_at + 86400`. Survives Client binding deletion.
- **Phase 2 (`pending_send`)**: activated at `contact_ready` inside the same transaction as review entitlements and `purchase_count++`. `expires_at` reset to `contact_ready_at + 86400` (matching review entitlement window). CAS must touch exactly 1 row; 0 or >1 → rollback.

On button press:

1. Bot parses `rv:<hex>:<action>` from callback_data.
2. Looks up entitlement by review_ref, checks expiry.
3. Atomically CAS: `UPDATE … SET rating=?, consumed_at=? WHERE id=? AND rating IS NULL`. 0 rows affected = already consumed.
4. Increments the target profile's positive_count or negative_count.
5. Telegram message is edited to confirm the rating.
6. Second button press with same action returns idempotent success; different action returns conflict.

### 22.4 Rating shape

Binary rating:

```text
positive
negative
```

Free-text reviews, star ratings, editing, dispute handling, and public review text are not part of the current agreed model and must not be added silently.

### 22.5 Helper-to-Client review

`FIXED (Task 06)`

`FIXED Task 06-FIX (was stale)`: The review token is NOT returned in the contact-ready or reveal HTTP response. After successful contact reveal, the Helper calls `POST /v2/helper/reviews/capability` separately to obtain the `review_token` (66-char opaque string) and `expires_at` (unix seconds, 24 h from `contact_ready_at`).

Review token format: `base64url(raw16bytes) + "." + base64url(HMAC-SHA256(hmacKey, "naroom:v2:review-token:" + reviewRef))`.

Endpoints:
- `POST /v2/helper/reviews/capability` — validates purchase_id + purchase_token + wallet_address; returns review_token + expires_at + client_reputation snapshot + client_display_name.
- `POST /v2/helper/reviews` — consumes the review token; increments Client's profile counter.

The Informer bot is not involved because its only function is selected-city new-listing notification.
No Helper Telegram connection is required for the Helper review flow.
The review token cannot reopen or reveal the purchased contact.

## 23. Helper State Machine - Working Draft

### 23.1 Wallet qualification

```text
LISTING_OPEN
  -> BUY_CONTACT_SELECTED
  -> OWN_WALLET_ENTERED
  -> HELPER_PROFILE_LOOKUP_BY_PROTECTED_WALLET_FINGERPRINT
  -> PRE_INVOICE_BALANCE_CHECK
```

Result:

```text
balance insufficient for $10 payment plus required post-payment minimum
  -> PURCHASE_BLOCKED_LOW_BALANCE

existing profile country differs from listing country
  -> PURCHASE_BLOCKED_COUNTRY

new wallet or existing profile in the same country
  -> COUNTRY_AND_BALANCE_NOTICE
  -> PURCHASE_ACCESS_CAPABILITY_ISSUED
  -> CONTACT_INVOICE_PENDING
```

The notice is informational. There is no checkbox or separate acknowledgement action. Displaying it before the invoice is mandatory; continuing to the invoice is acceptance.

### 23.2 Payment and sender decision

```text
CONTACT_INVOICE_PENDING
  -> PAYMENT_DETECTED
  -> PAYMENT_CONFIRMED
  -> EXPECTED_WALLET_SENDER_VERIFICATION
```

Result:

```text
payment not attributable to the entered expected wallet
  -> PAID_CONTACT_WITHHELD_WRONG_SENDER

payment attributable to expected wallet
  -> POST_PAYMENT_BALANCE_CHECK
```

Balance result:

```text
expected wallet balance >= $1,000
  -> HELPER_PROFILE_CREATED_OR_LOADED
  -> COUNTRY_LOCK_COMMITTED_AT_APPROVED_TRANSITION
  -> CONTACT_RECEIPT_READY
  -> PURCHASE_AGGREGATE_INCREMENTED
  -> CLIENT_TELEGRAM_NOTIFIED
  -> CLIENT_REVIEW_TOKEN_AVAILABLE

expected wallet balance < $1,000
  -> PAID_CONTACT_WITHHELD_LOW_BALANCE
```

There is no refund for wrong-sender or low-balance results. A Helper who paid but holds < $1,000 post-payment may call `/recheck-balance` to retry the balance check until `balance_retry_deadline_at` (`confirmation_deadline_at + 24 h`). After that deadline the purchase moves to the terminal `failed` state.

### 23.3 Immediate contact result and review code

Delivery flow:

```text
CONTACT_RECEIPT_READY
  -> CONTACT_SHOWN_IMMEDIATELY_ON_WEBSITE
  -> REVIEW_ONLY_CODE_OR_LINK_ISSUED
```

The review capability authorizes one later rating only. It cannot reveal the contact. No Telegram registration, Telegram delivery, or Informer interaction exists in this flow.

### 23.4 Return and interruption requirements

`FIXED (Task 05-FIX / Task 05-FIX2)` — backend capability contract:

The purchase token (64 lowercase hex, browser-generated, returned once in the `201` create response) is the sole short-lived capability for this purchase. It is stored only as `HMAC-SHA256("naroom:v2:helper-browser-token:", token)` and never re-issued. Using the same token + same wallet:

- `/contact-purchases` (create) → idempotent `200` with existing purchase, no new provider/issuer call;
- `/contact-purchases/restore` → restores purchase phase/state at any point within the allowed lifetime;
- `/recheck-balance` → retries the balance check until `confirmation_deadline_at + 24 h`;
- `/contact-purchases/reveal` → reveals the contact within `receipt_expires_at` (24 h from first reveal).

Different token or different wallet for the same purchase → generic `404` (no enumeration). There is no user-facing purchase recovery code and no cross-browser promise. The public wallet address alone does not grant access.

`UNRESOLVED (Task 08)` — frontend UX after browser interruption:

- Exact frontend behavior when the Helper closes the browser tab or refreshes the page during payment confirmation.
- Whether the contact remains visible after an ordinary refresh of the reveal page or is strictly shown once.

These are frontend presentation decisions, not backend contract changes.

### 23.5 Country enforcement

Listings and Helper locks must use a normalized `country_code`, not city-name string comparison.

```text
helper.country_code is empty
  -> purchase may proceed subject to warnings

helper.country_code == listing.country_code
  -> purchase may proceed in any supported city in that country

helper.country_code != listing.country_code
  -> purchase action is blocked before invoice creation
```

The same rule must be enforced by the backend even if a user bypasses the frontend notice or opens a direct listing URL. The country lock is committed at the first successful sender verification and post-payment balance check (≥ $1,000) via atomic CAS update; a concurrent purchase for a different country causes the new purchase to transition to `failed` in the same transaction, leaving the existing lock intact.

## 24. Helper Journey Completeness Audit

### 24.1 Parts that are already coherent

- Helper can begin directly from any public listing without prior Informer registration.
- Helper enters their own wallet before invoice creation.
- Platform checks whether the wallet is new, whether an existing country lock permits this listing, and whether balance appears sufficient.
- Helper sees an informational country-lock, expected-wallet, post-payment balance, contact-withholding, and no-refund notice before the invoice. No acknowledgement control is required.
- The $10 invoice is bound to the entered wallet and payment must be attributable to that wallet.
- The same expected wallet is checked for at least $1,000 after payment confirmation.
- Successful payment and balance verification reveal the external contact without changing listing visibility.
- Helper account can retain age, aggregate purchases, aggregate ratings, and one country lock without retaining a permanent per-listing purchase history.
- Client can receive a Telegram purchase notification and later rate that Helper through a single-use action.
- Helper needs no Telegram connection. A separate review-only code/link may support a later Helper review without reopening the contact.

### 24.2 Helper path still not fully closed

`FIXED (Task 05-FIX)`

- **Country lock commit point** (was item 1): first successful sender verification and post-payment balance check ≥ $1,000. Atomic CAS; concurrent conflict → purchase `failed`.
- **Low-balance retry** (was item 3): allowed via `/recheck-balance` until `confirmation_deadline_at + 24 h`; after that the purchase is `failed`.
- **Late payment** (was item 9): contact is still revealed; listing visibility is checked only at invoice create time.
- **Same-browser continuity**: browser-generated `purchase_token` returned once at `201`; same token + wallet = idempotent `200`.
- **Receipt window**: `receipt_expires_at = first_revealed_at + 24 h`.

`FIXED (Task 06)`

- **Helper review-code/link lifetime and target (was item 6):** review token expires 24 h from `contact_ready_at`. Review targets the Client's wallet-bound profile (v2_client_profiles). Token is HMAC-signed and single-use. Format and endpoints defined in §22.5.

`UNRESOLVED`

1. Exact BTC/LTC funding-wallet resolution rule for multi-input and custodial transactions.
2. Same-browser behavior when the page is refreshed or closed before payment confirmation completes.
3. Whether the contact remains visible after refreshing the same result page or is strictly shown once.
4. How long pending invoices and temporary purchase-to-review links remain stored.
5. Whether the same wallet-bound profile may also act as a Client.

These points remain open. Informer decisions are tracked separately. No implementation agent may choose them silently.
