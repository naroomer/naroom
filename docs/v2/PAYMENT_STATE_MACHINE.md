# NA Room V2 — Payment State Machine

This document describes the V2 client invoice state machine, guard conditions, and side effects.

It contains no real addresses, keys, or production configuration.

## Invoice States

| State | Description |
|-------|-------------|
| `pending` | Invoice created; awaiting on-chain payment detection |
| `payment_detected` | Suitable unconfirmed tx found; awaiting blockchain confirmation |
| `confirmed` | Tx confirmed; entitlement started |
| `expired` | Deadline passed without suitable detection or confirmation |

## State Transitions

| From | To | Trigger | Guard Conditions | Side Effects |
|------|-----|---------|-----------------|-------------|
| `pending` | `payment_detected` | Suitable tx found by watcher | `observedAt ≤ detection_deadline_at`; `amountAtomic ≥ invoice.amount_atomic`; entered wallet is among tx inputs | Sets `detected_txid`, `payment_detected_at`; `confirmation_deadline_at = payment_detected_at + 24h` |
| `pending` | `expired` | Detection deadline passed | `now > detection_deadline_at` | No entitlement; client must create new payment intent |
| `payment_detected` | `confirmed` | Detected tx has ≥1 confirmation | `observedAt ≤ confirmation_deadline_at` | Sets `payment_txid = detected_txid`, `payment_confirmed_at`, `entitlement_expires_at = confirmed_at + 5d`; flow state → `payment_confirmed` |
| `payment_detected` | `expired` | Confirmation grace expired | `now > confirmation_deadline_at` | No entitlement; client must create new payment intent |

## Deadlines

| Deadline | Value |
|----------|-------|
| Detection deadline | `invoice.created_at + 60 minutes` |
| Confirmation grace deadline | `invoice.payment_detected_at + 24 hours` |
| Entitlement expiry | `invoice.payment_confirmed_at + 5 × 24 hours` |

## Boundary Semantics

- Detection: `observedAt ≤ detection_deadline_at` is accepted (inclusive boundary).
- Detection: `observedAt > detection_deadline_at` is late and does not activate.
- Confirmation: `observedAt ≤ confirmation_deadline_at` is accepted (inclusive boundary).
- After confirmation grace expires: invoice transitions to `expired`, not back to `pending`.

## Key Invariants

1. A `payment_detected` invoice is locked to exactly one `detected_txid`. No other tx can replace it before confirmation deadline.
2. Amounts from separate transactions are never aggregated.
3. `entitlement_expires_at` is set exactly once when `confirmed` is reached.
4. An `expired` invoice can never transition to `confirmed`.
5. Wrong-sender tx does not close the invoice; watcher continues scanning until deadline.
6. Provider/RPC outage does not change invoice state; watcher retries with bounded backoff.
7. Duplicate watcher cycles on the same invoice are idempotent (CAS guards all updates).

## Post-Payment Balance

After `confirmed`:
- Watcher finds the tx input address matching the wallet fingerprint.
- Checks that address's current USD balance.
- Hard floor: `$120` (backend; public UI shows `$150`).
- `balance ≥ $120` → flow state `form_ready`.
- `balance < $120` → flow state `paid_low_balance`.
- Balance check may be retried by the API (recheck-balance endpoint) and by watcher restart.
- The matched sender address is resolved from the on-chain tx on each check; it is never stored in the database.

## Privacy Constraints

- Client wallet address is never stored; only its HMAC fingerprint persists.
- `detected_txid` is stored for state continuity (needed for confirmation polling and balance-check restart). It is not exposed in public API responses.
- `payment_txid` in the HTTP response is omitted from create/restore/recheck responses per Task 02 contract.
- Watcher logs do not contain wallet addresses, txids, or fingerprints.

## Watcher Backoff

The V2Watcher uses capped exponential backoff for the polling loop:

| Parameter | Value |
|-----------|-------|
| Start | 5 seconds |
| Growth factor | 2× |
| Maximum | 5 minutes |
| Reset trigger | Successful chain provider call (even if no suitable tx found) |

Backoff is managed by `Run` exactly once per cycle. `ProcessOnce` returns a `CycleResult`; if any invoice in the cycle has a provider/config/DB error, the whole cycle is treated as failed and backoff advances. A successful cycle (all invoices succeed or have no provider call) resets backoff. One invoice's success never hides another's failure.

## HD Address Derivation Contract

`HDAllocatorAdapter` wraps the V1 `crypto.HDWallet` and **shares** the V1 `invoice_index` atomic counter. This is intentionally correct: same xpub + one shared counter guarantees unique derivation indexes across V1 and V2. A separate V2 counter for the same xpub would re-derive indexes already used by V1, creating real address collisions — that pattern would be unsafe.
