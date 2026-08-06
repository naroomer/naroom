<script>
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { lang, t as tFn } from '$lib/i18n.js';
	import SeoHead from '$lib/SeoHead.svelte';
	import V2QR from '$lib/V2QR.svelte';
	import { LAUNCH_DISPLAY_LIMITS } from '$lib/v2LaunchPolicy.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	// purchase_token from sessionStorage only — never exposed in URL/history
	let purchaseToken = $state('');

	let step = $state('loading'); // loading | invoice | balance | contact | done
	let loading = $state(true);
	let error = $state('');
	// True only when restorePurchase() itself failed to complete (network
	// error, timeout, non-2xx, or a malformed response) — distinct from the
	// "no token"/"no purchase" cases, which are not retryable the same way.
	// Drives the Retry button in the step==='loading' && !loading branch.
	let restoreFailed = $state(false);
	let copyMsg = $state('');

	// Purchase data
	let purchaseId = $state('');
	let walletAddress = $state('');
	let listingId = $state('');
	let city = $state('');
	let currency = $state('BTC');
	let helperPublicName = $state('');
	let clientPublicName = $state('');
	let invoice = $state(null);
	let phase = $state('');
	let balanceRetryDeadline = $state(0);
	let contactType = $state('');
	let contactValue = $state('');
	let receiptExpiresAt = $state(0);
	let lastBalanceUSD = $state(null);

	// Provider observability (Section D)
	let providerStatus = $state(''); // 'checking' | 'healthy' | 'degraded'
	let lastSuccessfulCheckAt = $state(null);

	// Review
	let reviewToken = $state('');
	let reviewClientReputation = $state(null);
	let reviewClientName = $state('');
	let reviewSubmitted = $state(false);

	// Telegram review reminder (Section C)
	let reminderBotUrl = $state('');
	let reminderLoading = $state(false);
	let reminderError = $state('');
	let reminderRegistered = $state(false);

	// Cross-device handoff (Section G) — the QR/link carries ONLY an opaque,
	// one-time token in the URL fragment. It never carries the wallet address.
	// Fragments are never sent to the server (unlike query params), so the
	// token cannot leak via access logs, Referer headers, or proxy logs.
	let handoffQRUrl = $state('');
	let handoffExpiresAt = $state(0);
	let handoffLoading = $state(false);
	let handoffError = $state('');

	// Second-device redeem: a dedicated wallet-entry form. The redeeming device
	// never receives the first device's wallet — it must independently supply
	// its own wallet address, which the server verifies against the purchase.
	let handoffRedeemToken = $state('');
	let handoffRedeemWallet = $state('');
	let handoffRedeemLoading = $state(false);
	let handoffRedeemError = $state('');

	// Review countdown (Section G)
	let reviewAvailableAt = $state(0); // unix timestamp
	let reviewCountdownSec = $state(0);
	let reviewCountdownTimer = null;

	// Active progress step derived from current step/phase
	let activeStep = $derived(
		step === 'invoice' && phase === 'payment_confirmed' ? 3 :
		step === 'invoice' ? 2 :
		step === 'balance' ? 4 :
		step === 'contact' ? 5 :
		2
	);

	function detectCurrency(addr) {
		const a = (addr || '').trim();
		if (!a) return null;
		if (/^ltc1/i.test(a) || /^[LM]/.test(a)) return 'LTC';
		if (/^bc1/i.test(a) || /^[13]/.test(a)) return 'BTC';
		return null;
	}

	function paymentURI(inv, curr) {
		if (!inv?.payment_address) return '';
		const a = inv.amount_atomic;
		const addr = inv.payment_address;
		if (curr === 'BTC') return `bitcoin:${addr}?amount=${(a/1e8).toFixed(8)}`;
		if (curr === 'LTC') return `litecoin:${addr}?amount=${(a/1e8).toFixed(8)}`;
		return addr;
	}

	onMount(async () => {
		// Cross-device handoff: the token travels ONLY in the URL fragment
		// (#handoff=...), never as a query param, and never with the wallet.
		// Fragments are not sent to the server, so this never appears in access
		// logs or Referer headers. Strip it from the visible URL immediately;
		// the second device must type its OWN wallet in the form below —
		// nothing is auto-submitted.
		const hash = window.location.hash || '';
		const hashMatch = hash.match(/[#&]handoff=([^&]+)/);
		if (hashMatch) {
			const token = decodeURIComponent(hashMatch[1]);
			history.replaceState({}, '', window.location.pathname + window.location.search);
			handoffRedeemToken = token;
			step = 'handoff_wallet';
			loading = false;
			return;
		}

		// Load token from sessionStorage only (never from URL)
		try { purchaseToken = sessionStorage.getItem('v2_active_purchase_token') || ''; } catch {}

		// localStorage holds the matching wallet and listing continuity record.
		// Hydrate missing fields even when sessionStorage already has the token:
		// a successful cross-device redeem creates exactly that combination.
		try {
			const lhpt = JSON.parse(localStorage.getItem('v2_active_hpt') || 'null');
			if (lhpt?.token && lhpt?.wallet) {
				if (!purchaseToken) {
					purchaseToken = lhpt.token;
					try { sessionStorage.setItem('v2_active_purchase_token', lhpt.token); } catch {}
				}
				if (lhpt.token === purchaseToken) {
					walletAddress = lhpt.wallet;
					listingId = lhpt.listingId || '';
					city = lhpt.city || '';
				}
			}
		} catch {}

		if (!purchaseToken) { error = t('v2.helper.no_token'); loading = false; return; }

		// Restore purchase data from sessionStorage
		let storageKey = `v2_purchase_${purchaseToken}`;
		let saved = null;
		try { saved = JSON.parse(sessionStorage.getItem(storageKey) || 'null'); } catch {}

		if (saved) {
			purchaseId = saved.purchaseId || '';
			listingId = saved.listingId || '';
			walletAddress = walletAddress || saved.walletAddress || '';
			if (!city) city = saved.city || '';
		}

		// Try to load city from localStorage hpt entry
		if (listingId && !city) {
			try {
				const lhpt = JSON.parse(localStorage.getItem(`v2_hpt_${listingId}`) || 'null');
				if (lhpt?.city) city = lhpt.city;
			} catch {}
		}

		if (purchaseToken && walletAddress) {
			await restorePurchase();
		} else {
			error = t('v2.helper.no_purchase');
			loading = false;
		}
	});

	// Status codes treated as transient/retryable when restore itself cannot
	// be classified more specifically from the response body. 408 (request
	// timeout) and 429 (rate limited) join the 5xx range — none of these mean
	// the purchase is actually gone, unlike a genuine 404 purchase_not_found.
	function isTransientRestoreStatus(status) {
		return status === 408 || status === 429 || (status >= 500 && status < 600);
	}

	async function restorePurchase() {
		loading = true;
		error = '';
		restoreFailed = false;
		try {
			const ctrl = new AbortController();
			const tid = setTimeout(() => ctrl.abort(), 10000);
			let res;
			try {
				res = await fetch('/api/v2/helper/contact-purchases/restore', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({
						purchase_token: purchaseToken,
						wallet_address: walletAddress,
					}),
					signal: ctrl.signal,
				});
			} finally {
				clearTimeout(tid);
			}

			let data;
			try {
				data = await res.json();
			} catch {
				// Malformed/non-JSON body — never surface the raw parse error or
				// response text. Treated as transient: the purchase token/wallet
				// in storage are left untouched, so Retry resumes the same purchase.
				error = t('v2.helper.restore_failed');
				restoreFailed = true;
				providerStatus = 'degraded';
				return;
			}

			if (!res.ok) {
				if (res.status === 404 && data && data.code === 'purchase_not_found') {
					// Not transient — this purchase_token/wallet combination does
					// not (or no longer) correspond to any purchase. Retrying the
					// same request will never succeed. Use the existing
					// no_purchase message and the existing terminal cleanup
					// helper (same one routeByPhase already uses for
					// failed/receipt_expired) so a reload does not loop back into
					// the same dead end.
					error = t('v2.helper.no_purchase');
					restoreFailed = false;
					clearHelperPurchaseState(listingId, purchaseToken);
					step = 'done';
					return;
				}
				if (isTransientRestoreStatus(res.status)) {
					// Network/server-side hiccup (408/429/5xx) — never raw backend
					// text. Purchase token/wallet in storage are left untouched.
					error = t('v2.helper.restore_failed');
					restoreFailed = true;
					return;
				}
				// Any other 4xx: not retryable, but also not confirmed gone the
				// way purchase_not_found is — show a safe generic message, no
				// Retry, no cleanup, no raw backend body/error, no new invoice.
				error = t('v2.helper.restore_blocked');
				restoreFailed = false;
				return;
			}

			applyRestoreData(data);
			routeByPhase(data);
		} catch (e) {
			// Network failure or timeout (AbortError) — never surface e.message.
			// Purchase token/wallet in storage are left untouched.
			error = t('v2.helper.restore_failed');
			restoreFailed = true;
			providerStatus = 'degraded';
		} finally {
			loading = false;
		}
	}

	function applyRestoreData(data) {
		phase = data.phase;
		purchaseId = data.purchase_id || purchaseId;
		currency = data.currency || 'BTC';
		helperPublicName = data.helper_public_name || '';
		clientPublicName = data.client_public_name || '';
		invoice = data.invoice || null;
		balanceRetryDeadline = data.balance_retry_deadline_at || 0;
		lastBalanceUSD = data.last_balance_usd ?? null;
		receiptExpiresAt = data.receipt_expires_at || 0;

		// Observability fields
		providerStatus = data.provider_status || '';
		if (data.last_successful_chain_check_at) {
			lastSuccessfulCheckAt = data.last_successful_chain_check_at;
		}
	}

	// Clears all storage keys associated with a helper purchase.
	function clearHelperPurchaseState(lid, pt) {
		try {
			if (lid) localStorage.removeItem(`v2_hpt_${lid}`);
			localStorage.removeItem('v2_active_hpt');
			sessionStorage.removeItem('v2_active_purchase_token');
			if (pt) sessionStorage.removeItem(`v2_purchase_${pt}`);
			if (lid) sessionStorage.removeItem(`v2_pt_${lid}`);
		} catch {}
	}

	function routeByPhase(data) {
		const ph = data.phase;
		if (ph === 'awaiting_payment' || ph === 'payment_detected') {
			step = 'invoice';
			startInvoicePoll();
		} else if (ph === 'paid_low_balance') {
			step = 'balance';
		} else if (ph === 'contact_ready') {
			step = 'contact';
			loadContact();
		} else if (ph === 'payment_confirmed') {
			// Waiting for contact to be ready — rare transient state
			step = 'invoice';
		} else if (ph === 'failed' || ph === 'payment_expired') {
			clearHelperPurchaseState(listingId, purchaseToken);
			error = t('v2.helper.payment_expired');
			step = 'done';
		} else if (ph === 'receipt_expired') {
			clearHelperPurchaseState(listingId, purchaseToken);
			error = t('v2.helper.receipt_expired');
			step = 'done';
		} else {
			step = 'invoice';
		}
	}

	// ── Invoice polling ──────────────────────────────────────────────────────────
	let pollTimer = null;

	function startInvoicePoll() {
		stopPoll();
		pollTimer = setInterval(pollPhase, 5000);
	}

	function stopPoll() {
		if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
	}

	async function pollPhase() {
		try {
			const ctrl = new AbortController();
			const tid = setTimeout(() => ctrl.abort(), 10000);
			const res = await fetch('/api/v2/helper/contact-purchases/restore', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_token: purchaseToken,
					wallet_address: walletAddress,
				}),
				signal: ctrl.signal,
			});
			clearTimeout(tid);

			if (!res.ok) {
				providerStatus = 'degraded';
				return;
			}
			const data = await res.json();
			applyRestoreData(data);

			if (data.phase !== 'awaiting_payment' && data.phase !== 'payment_detected') {
				stopPoll();
				routeByPhase(data);
			}
		} catch (e) {
			if (e.name !== 'AbortError') {
				providerStatus = 'degraded';
			}
		}
	}

	$effect(() => {
		return () => stopPoll();
	});

	// ── Load contact (reveal) ────────────────────────────────────────────────────
	async function loadContact() {
		try {
			const ctrl = new AbortController();
			const tid = setTimeout(() => ctrl.abort(), 10000);
			const res = await fetch('/api/v2/helper/contact-purchases/reveal', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_token: purchaseToken,
					wallet_address: walletAddress,
				}),
				signal: ctrl.signal,
			});
			clearTimeout(tid);
			const data = await res.json();
			if (!res.ok) { error = data.error || `HTTP ${res.status}`; return; }
			contactType = data.contact_type;
			contactValue = data.contact;
			receiptExpiresAt = data.receipt_expires_at;
			// Try to load review capability
			await loadReviewCapability();
		} catch (e) {
			if (e.name !== 'AbortError') error = e.message;
		}
	}

	// ── Review ───────────────────────────────────────────────────────────────────
	async function loadReviewCapability() {
		try {
			const ctrl = new AbortController();
			const tid = setTimeout(() => ctrl.abort(), 10000);
			const res = await fetch('/api/v2/helper/reviews/capability', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_id: purchaseId,
					purchase_token: purchaseToken,
					wallet_address: walletAddress,
				}),
				signal: ctrl.signal,
			});
			clearTimeout(tid);
			if (res.status === 403) {
				const data = await res.json();
				if (data.available_at) {
					reviewAvailableAt = data.available_at;
					startReviewCountdown();
				}
				return;
			}
			if (!res.ok) return;
			const data = await res.json();
			reviewSubmitted = data.review_submitted === true;
			reviewToken = reviewSubmitted ? '' : (data.review_token || '');
			reviewClientReputation = data.client_reputation || null;
			reviewClientName = data.client_display_name || '';
			reviewAvailableAt = 0;
			stopReviewCountdown();
		} catch {}
	}

	function startReviewCountdown() {
		stopReviewCountdown();
		updateCountdown();
		reviewCountdownTimer = setInterval(() => {
			updateCountdown();
		}, 1000);
	}

	function stopReviewCountdown() {
		if (reviewCountdownTimer) { clearInterval(reviewCountdownTimer); reviewCountdownTimer = null; }
	}

	function updateCountdown() {
		const now = Math.floor(Date.now() / 1000);
		const remaining = reviewAvailableAt - now;
		if (remaining <= 0) {
			reviewCountdownSec = 0;
			stopReviewCountdown();
			// Re-check capability
			loadReviewCapability();
		} else {
			reviewCountdownSec = remaining;
		}
	}

	function formatCountdown(sec) {
		const h = Math.floor(sec / 3600);
		const m = Math.floor((sec % 3600) / 60);
		const s = sec % 60;
		return `${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}:${String(s).padStart(2,'0')}`;
	}

	$effect(() => {
		return () => stopReviewCountdown();
	});

	async function submitReview(rating) {
		if (!reviewToken || reviewSubmitted) return;
		try {
			const res = await fetch('/api/v2/helper/reviews', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ review_token: reviewToken, rating }),
			});
			if (res.ok) {
				reviewSubmitted = true;
			}
		} catch {}
	}

	// ── Telegram review reminder (Section C) ─────────────────────────────────────
	async function requestReminderLink() {
		reminderLoading = true;
		reminderError = '';
		reminderBotUrl = '';
		try {
			const res = await fetch('/api/v2/helper/reviews/reminder-link', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ purchase_token: purchaseToken, wallet_address: walletAddress }),
			});
			const data = await res.json();
			if (!res.ok) {
				if (res.status === 503) {
					// Reminder not configured on this server
					reminderError = '';
					return;
				}
				reminderError = data.error || `HTTP ${res.status}`;
				return;
			}
			reminderBotUrl = data.bot_url;
		} catch (e) {
			reminderError = e.message;
		} finally {
			reminderLoading = false;
		}
	}

	// ── Cross-device handoff (Section G) ────────────────────────────────────────
	async function createHandoff() {
		handoffLoading = true;
		handoffError = '';
		handoffQRUrl = '';
		try {
			const res = await fetch('/api/v2/helper/handoff/create', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ purchase_token: purchaseToken, wallet_address: walletAddress }),
			});
			const data = await res.json();
			if (!res.ok) { handoffError = data.error || `HTTP ${res.status}`; return; }
			// Token only, in the URL fragment. Never the wallet address, never a
			// query param (query params — unlike fragments — are sent to the
			// server and land in access logs / Referer headers).
			const url = `${window.location.origin}/v2/helper/purchase#handoff=${encodeURIComponent(data.token)}`;
			handoffQRUrl = url;
			handoffExpiresAt = data.expires_at;
		} catch (e) {
			handoffError = e.message;
		} finally {
			handoffLoading = false;
		}
	}

	async function redeemHandoff(token, wallet, curr) {
		loading = true;
		step = 'loading';
		error = '';
		try {
			const res = await fetch('/api/v2/helper/handoff/redeem', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ token, wallet_address: wallet }),
			});
			const data = await res.json();
			if (!res.ok) {
				// Wrong wallet, wrong/expired/replayed token, and concurrent-redeem
				// loss are all indistinguishable to the caller (safe generic message).
				error = t('v2.helper.handoff_invalid');
				loading = false;
				step = 'done';
				return;
			}
			purchaseToken = data.browser_token;
			walletAddress = wallet;
			currency = curr;
			try { sessionStorage.setItem('v2_active_purchase_token', purchaseToken); } catch {}
			try {
				localStorage.setItem('v2_active_hpt', JSON.stringify({
					token: purchaseToken,
					wallet: walletAddress,
					listingId,
					city,
				}));
			} catch {}
			await restorePurchase();
		} catch (e) {
			error = e.message;
			loading = false;
			step = 'done';
		}
	}

	// Second-device form submit: the user enters THEIR OWN wallet address (never
	// pre-filled, never received from the first device) to redeem the handoff.
	async function submitHandoffRedeem() {
		if (!handoffRedeemWallet.trim() || handoffRedeemLoading) return;
		handoffRedeemLoading = true;
		handoffRedeemError = '';
		const curr = detectCurrency(handoffRedeemWallet) || 'BTC';
		await redeemHandoff(handoffRedeemToken, handoffRedeemWallet.trim(), curr);
		handoffRedeemLoading = false;
	}

	// ── Copy ──────────────────────────────────────────────────────────────────────
	async function copyText(text, label) {
		try {
			await navigator.clipboard.writeText(text);
			copyMsg = label || t('v2.copied');
			setTimeout(() => { copyMsg = ''; }, 2000);
		} catch {}
	}

	function formatExpiry(unix) {
		if (!unix) return '';
		const d = new Date(unix * 1000);
		return d.toLocaleString();
	}

	function formatCheckTime(unix) {
		if (!unix) return '';
		const d = new Date(unix * 1000);
		return d.toLocaleTimeString();
	}

	function openContact() {
		if (!contactValue) return;
		if (contactType === 'telegram') {
			const handle = contactValue.startsWith('@') ? contactValue.slice(1) : contactValue;
			window.open(`https://t.me/${handle}`, '_blank', 'noopener,noreferrer');
		} else if (contactType === 'signal') {
			window.open(`https://signal.me/#p/${contactValue}`, '_blank', 'noopener,noreferrer');
		}
	}

	// Back nav hrefs
	let backToListingHref = $derived(listingId ? `/v2/listing/${listingId}` : '');
	let backToBoardHref   = $derived(city ? `/v2/board/${city}` : '/v2/board/tbilisi');
</script>

<SeoHead robots="noindex, nofollow, noarchive" canonicalUrl={page.url.origin + page.url.pathname} />

<div class="layout">
	<!-- ── Topbar ── -->
	<div class="topbar">
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
		<!-- Back navigation (Section E) -->
		<div class="topbar-nav">
			{#if backToListingHref}
				<a href={backToListingHref} class="back-link">{t('v2.helper.back_to_listing')}</a>
			{/if}
			<a href={backToBoardHref} class="back-link">{t('v2.helper.back_to_board')}</a>
		</div>
		{#if step !== 'loading' && step !== 'done'}
			<div class="progress">
				{#each [1,2,3,4,5] as n}
					{#if n > 1}<div class="prog-line" class:done={activeStep > n}></div>{/if}
					<div class="prog-step" class:done={activeStep > n} class:active={activeStep === n}>
						<div class="prog-dot"></div>
						<span class="prog-label">{t('v2.helper.progress.step' + n)}</span>
					</div>
				{/each}
				<span class="prog-current">{t('v2.helper.progress.step' + activeStep)} · {activeStep}/5</span>
			</div>
		{/if}
	</div>

	<!-- ── Page body ── -->
	<div class="page-body">

		{#if loading && step === 'loading'}
			<div class="center-msg">{t('v2.loading')}</div>

		{:else if step === 'loading' && !loading}
			<!-- Restore never advanced past its initial phase: either there is
			     nothing to restore (no_token/no_purchase — not retryable, go back
			     via the topbar links), or restorePurchase() itself failed
			     (network/timeout/non-2xx/malformed — retryable in place). Either
			     way this must never render as a blank page. -->
			<div class="step-center" data-testid="restore-error-state">
				<div class="step-inner">
					{#if error}<div class="err">{error}</div>{/if}
					{#if restoreFailed}
						<button
							class="btn-primary"
							data-testid="restore-retry-btn"
							onclick={restorePurchase}
							disabled={loading}
						>
							{t('v2.retry')}
						</button>
					{/if}
				</div>
			</div>

		{:else if step === 'handoff_wallet'}
			<div class="step-center" data-testid="handoff-redeem-form">
				<div class="step-inner">
					<h2>{t('v2.helper.handoff_redeem_title')}</h2>
					<p class="sub">{t('v2.helper.handoff_redeem_sub')}</p>
					<div class="field">
						<input
							bind:value={handoffRedeemWallet}
							placeholder={t('v2.listing.helper_wallet_ph')}
							type="text"
							autocomplete="off"
							spellcheck="false"
							data-testid="handoff-redeem-wallet-input"
						/>
					</div>
					{#if handoffRedeemError}<div class="err">{handoffRedeemError}</div>{/if}
					<button
						class="btn-primary"
						data-testid="handoff-redeem-submit-btn"
						onclick={submitHandoffRedeem}
						disabled={handoffRedeemLoading || !handoffRedeemWallet.trim()}
					>
						{handoffRedeemLoading ? t('v2.loading') : t('v2.helper.handoff_redeem_btn')}
					</button>
				</div>
			</div>

		{:else if step === 'invoice'}
			<div class="invoice-wrap">
				<div class="invoice-shell">

				<!-- Meta panel: aliases + instructions + observability -->
				<div class="meta-panel">
					<div class="meta-inner">
						{#if helperPublicName || clientPublicName}
							<div class="alias-block">
								{#if helperPublicName}
									<div class="alias-row">
										<span class="alias-label">{t('v2.helper.your_nickname')}</span>
										<span class="alias-value">{helperPublicName}</span>
									</div>
								{/if}
								{#if clientPublicName}
									<div class="alias-row">
										<span class="alias-label">{t('v2.helper.buying_contact_for')}</span>
										<span class="alias-value">{clientPublicName}</span>
									</div>
								{/if}
								<div class="alias-hint">{t('v2.helper.nickname_permanent')}</div>
							</div>
						{/if}

						<!-- Observability info (Section D): reflects the real domain state
						     (helper_http.go derives provider_status from actual
						     last_check_attempt_at / last_successful_chain_check_at
						     timestamps) — never a synthetic/fabricated value. -->
						{#if providerStatus === 'degraded'}
							<div class="provider-degraded">
								{t('v2.helper.provider_degraded')}
							</div>
						{:else if providerStatus === 'healthy'}
							<div class="provider-healthy">
								{t('v2.helper.provider_healthy')}
							</div>
						{:else}
							<div class="provider-checking">
								{t('v2.helper.provider_checking')}
							</div>
						{/if}

						{#if lastSuccessfulCheckAt}
							<div class="last-check-info">
								{t('v2.helper.last_check', { time: formatCheckTime(lastSuccessfulCheckAt) })}
							</div>
						{/if}

						{#if phase === 'awaiting_payment' || phase === 'payment_detected'}
							<div class="instr-block">
								<p class="instr">{t('v2.helper.instr1')}</p>
								<p class="instr">{t('v2.helper.instr2')}</p>
								<p class="instr">{t('v2.helper.instr3')}</p>
							</div>
						{/if}

						<!-- Cross-device handoff -->
						{#if !handoffQRUrl}
							<button class="btn-handoff" onclick={createHandoff} disabled={handoffLoading}>
								{handoffLoading ? t('v2.loading') : t('v2.helper.handoff_btn')}
							</button>
							{#if handoffError}<div class="err">{handoffError}</div>{/if}
						{:else}
							<div class="handoff-box">
								<p class="handoff-title">{t('v2.helper.handoff_title')}</p>
								<p class="handoff-scan">{t('v2.helper.handoff_scan')}</p>
								<div class="handoff-qr"><V2QR data={handoffQRUrl} /></div>
								<p class="handoff-expires">{t('v2.helper.handoff_expires', { time: formatExpiry(handoffExpiresAt) })}</p>
								<button class="btn-text" onclick={() => { handoffQRUrl = ''; handoffExpiresAt = 0; }}>
									{t('v2.helper.handoff_close')}
								</button>
							</div>
						{/if}
					</div>
				</div>

				<!-- Pay panel: status + amount + address + QR -->
				<div class="pay-panel">
					{#if invoice}
						<div class="pay-surface">
							<div class="inv-status"
								class:confirmed={phase === 'contact_ready' || phase === 'payment_confirmed'}
								class:detected={phase === 'payment_detected'}>
								{#if phase === 'awaiting_payment'}
									{t('v2.invoice.pending')}
								{:else if phase === 'payment_detected'}
									{t('v2.invoice.payment_detected')}
								{:else}
									{t('v2.invoice.confirmed')}
								{/if}
							</div>

							<!-- Pulse dot for current progress (Section D) -->
							{#if phase === 'awaiting_payment'}
								<div class="pulse-dot" aria-hidden="true"></div>
							{/if}

							<div class="inv-row">
								<span class="inv-label">{t('v2.invoice.amount')}</span>
								<span class="inv-val">{(invoice.amount_atomic / 1e8).toFixed(8)} {currency}</span>
								<button class="copy-btn" onclick={() => copyText((invoice.amount_atomic / 1e8).toFixed(8), t('v2.copied'))}>
									{copyMsg || t('v2.copy')}
								</button>
							</div>

							<div class="inv-row">
								<span class="inv-label">{t('v2.invoice.usd')}</span>
								<span class="inv-val">${(invoice.amount_usd_cents / 100).toFixed(2)}</span>
							</div>

							<div class="inv-row addr-row">
								<span class="inv-label">{t('v2.invoice.address')}</span>
								<span class="inv-val addr">{invoice.payment_address}</span>
								<button class="copy-btn" onclick={() => copyText(invoice.payment_address, t('v2.copied'))}>
									{copyMsg || t('v2.copy')}
								</button>
							</div>

							{#if (phase === 'awaiting_payment' || phase === 'payment_detected') && invoice.payment_address}
								<div class="qr-wrap">
									<V2QR data={paymentURI(invoice, currency)} />
								</div>
							{/if}
						</div>
					{/if}

					{#if error}<div class="err">{error}</div>{/if}
				</div>
				</div>
			</div>

		{:else if step === 'balance'}
			<div class="step-center">
				<div class="step-inner">
					<h2>{t('v2.balance.title')}</h2>
					{#if lastBalanceUSD !== null}
						<div class="err">{t('v2.balance.low', { balance: lastBalanceUSD.toFixed(0), min: '$' + LAUNCH_DISPLAY_LIMITS.helperPostPaymentMinUSD })}</div>
					{/if}
					<p class="sub">{t('v2.helper.balance_sub')}</p>
					<button class="btn-secondary" onclick={restorePurchase} disabled={loading}>
						{loading ? t('v2.loading') : t('v2.balance.recheck')}
					</button>
					{#if !handoffQRUrl}
						<button class="btn-handoff" onclick={createHandoff} disabled={handoffLoading}>
							{handoffLoading ? t('v2.loading') : t('v2.helper.handoff_btn')}
						</button>
						{#if handoffError}<div class="err">{handoffError}</div>{/if}
					{:else}
						<div class="handoff-box">
							<p class="handoff-title">{t('v2.helper.handoff_title')}</p>
							<p class="handoff-scan">{t('v2.helper.handoff_scan')}</p>
							<div class="handoff-qr"><V2QR data={handoffQRUrl} /></div>
							<p class="handoff-expires">{t('v2.helper.handoff_expires', { time: formatExpiry(handoffExpiresAt) })}</p>
							<button class="btn-text" onclick={() => { handoffQRUrl = ''; handoffExpiresAt = 0; }}>
								{t('v2.helper.handoff_close')}
							</button>
						</div>
					{/if}
					{#if error}<div class="err">{error}</div>{/if}
				</div>
			</div>

		{:else if step === 'contact'}
			<div class="step-center">
				<div class="step-inner">
					<h2>{t('v2.helper.contact_title')}</h2>

					{#if contactValue}
						<div class="contact-reveal">
							<div class="contact-type-badge">{contactType}</div>
							<div class="contact-value">
								<span class="cv-text">{contactValue}</span>
								<button class="copy-btn-lg" onclick={() => copyText(contactValue, t('v2.copied'))}>
									{copyMsg || t('v2.copy')}
								</button>
								<button class="open-btn" onclick={openContact}>
									{t('v2.helper.open_contact')}
								</button>
							</div>
							<p class="hint">{t('v2.helper.contact_hint')}</p>
							{#if receiptExpiresAt}
								<p class="expiry">{t('v2.helper.receipt_expires', { time: formatExpiry(receiptExpiresAt) })}</p>
								<p class="expiry">{t('v2.helper.same_browser_return')}</p>
							{/if}
						</div>
					{/if}

					<!-- Review section with countdown (Section G) -->
					{#if reviewAvailableAt > 0 && reviewCountdownSec > 0}
						<div class="review-countdown-box">
							<p class="review-not-yet">{t('v2.review.not_yet')}</p>
							<div class="review-countdown">
								{t('v2.review.countdown', { time: formatCountdown(reviewCountdownSec) })}
							</div>
							<!-- Optional Telegram reminder (Section C) -->
							{#if reminderRegistered}
								<p class="reminder-sent">{t('v2.helper.reminder_sent')}</p>
							{:else if reminderBotUrl}
								<a href={reminderBotUrl} target="_blank" rel="noopener noreferrer"
								   class="btn-reminder-link"
								   onclick={() => { reminderRegistered = true; }}>
									{t('v2.helper.reminder_open')}
								</a>
							{:else if !reminderLoading}
								<button class="btn-reminder" onclick={requestReminderLink} disabled={reminderLoading}>
									{t('v2.helper.reminder_btn')}
								</button>
								{#if reminderError}<p class="err">{reminderError}</p>{/if}
							{/if}
						</div>
					{:else if reviewToken && !reviewSubmitted}
						<div class="review-section">
							<h3>{t('v2.review.title')}</h3>
							{#if reviewClientName}
								<p class="sub">{t('v2.review.about', { name: reviewClientName })}</p>
							{/if}
							<div class="review-btns">
								<button class="review-btn positive" onclick={() => submitReview('positive')}>
									👍 {t('v2.review.positive')}
								</button>
								<button class="review-btn negative" onclick={() => submitReview('negative')}>
									👎 {t('v2.review.negative')}
								</button>
							</div>
						</div>
					{:else if reviewSubmitted}
						<div class="review-done">{t('v2.review.submitted')}</div>
					{/if}

					<!-- Cross-device handoff (contact step) -->
					{#if !handoffQRUrl}
						<button class="btn-handoff" onclick={createHandoff} disabled={handoffLoading}>
							{handoffLoading ? t('v2.loading') : t('v2.helper.handoff_btn')}
						</button>
						{#if handoffError}<div class="err">{handoffError}</div>{/if}
					{:else}
						<div class="handoff-box">
							<p class="handoff-title">{t('v2.helper.handoff_title')}</p>
							<p class="handoff-scan">{t('v2.helper.handoff_scan')}</p>
							<div class="handoff-qr"><V2QR data={handoffQRUrl} /></div>
							<p class="handoff-expires">{t('v2.helper.handoff_expires', { time: formatExpiry(handoffExpiresAt) })}</p>
							<button class="btn-text" onclick={() => { handoffQRUrl = ''; handoffExpiresAt = 0; }}>
								{t('v2.helper.handoff_close')}
							</button>
						</div>
					{/if}

					{#if error}<div class="err">{error}</div>{/if}
				</div>
			</div>

		{:else if step === 'done'}
			<div class="step-center">
				<div class="step-inner">
					<h2>{t('v2.helper.done_title')}</h2>
					{#if error}<p class="err">{error}</p>{/if}
					<a href={backToBoardHref} class="btn-secondary">{t('v2.listing.back')}</a>
				</div>
			</div>
		{/if}

	</div>
</div>

<style>
	/* ── Layout shell ── */
	.layout {
		min-height: 100dvh;
		height: 100dvh;
		overflow: hidden;
		display: flex;
		flex-direction: column;
		background: var(--bg);
	}

	/* ── Topbar ── */
	.topbar {
		flex-shrink: 0;
		height: 44px;
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0 20px;
		border-bottom: 1px solid var(--border);
		gap: 12px;
	}

	.logo { font-size: 16px; font-weight: 600; color: var(--text); flex-shrink: 0; }
	.v2-badge {
		font-size: 10px;
		background: var(--accent);
		color: var(--bg);
		border-radius: 4px;
		padding: 1px 5px;
		margin-left: 4px;
		font-weight: 700;
		vertical-align: middle;
	}

	/* Back nav */
	.topbar-nav {
		display: flex;
		gap: 12px;
		align-items: center;
		flex-shrink: 0;
	}
	.back-link {
		font-size: 12px;
		color: var(--text-dim);
		text-decoration: none;
		white-space: nowrap;
	}
	.back-link:hover { color: var(--text); }

	/* ── Progress bar ── */
	.progress {
		display: flex;
		align-items: center;
	}
	.prog-step {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 2px;
	}
	.prog-dot {
		width: 9px; height: 9px;
		border-radius: 50%;
		background: var(--border);
		border: 2px solid var(--border);
		flex-shrink: 0;
	}
	.prog-step.done .prog-dot { background: var(--accent); border-color: var(--accent); opacity: 0.55; }
	.prog-step.active .prog-dot {
		background: var(--accent);
		border-color: var(--accent);
		animation: pulse-dot 1.8s ease-in-out infinite;
	}
	@media (prefers-reduced-motion: reduce) {
		.prog-step.active .prog-dot { animation: none; }
	}
	@keyframes pulse-dot {
		0%, 100% { box-shadow: 0 0 0 0 rgba(100,180,255,0.4); }
		50%       { box-shadow: 0 0 0 4px rgba(100,180,255,0); }
	}
	.prog-label {
		font-size: 9px;
		color: var(--text-faint);
		white-space: nowrap;
	}
	.prog-step.done .prog-label,
	.prog-step.active .prog-label { color: var(--text-dim); }
	.prog-line {
		width: 26px; height: 2px;
		background: var(--border);
		margin-bottom: 13px;
		flex-shrink: 0;
	}
	.prog-line.done { background: var(--accent); opacity: 0.4; }

	/* Mobile current step label — hidden on desktop */
	.prog-current {
		display: none;
		font-size: 11px;
		color: var(--text-dim);
		white-space: nowrap;
		margin-left: 10px;
		font-weight: 500;
	}

	/* ── Page body ── */
	.page-body {
		flex: 1;
		min-height: 0;
		display: flex;
		overflow: hidden;
	}

	/* ── Loading ── */
	.center-msg {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--text-dim);
		font-size: 14px;
	}

	/* ── Invoice step: two-column ── */
	.invoice-wrap {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 16px;
		overflow: hidden;
	}

	.invoice-shell {
		width: min(760px, 100%);
		max-height: 100%;
		display: grid;
		grid-template-columns: minmax(210px, 250px) minmax(0, 1fr);
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: hidden;
	}

	.meta-panel {
		min-width: 0;
		border-right: 1px solid var(--border);
		display: flex;
		align-items: flex-start;
		padding: 18px;
		overflow-y: auto;
	}

	.meta-inner {
		display: flex;
		flex-direction: column;
		gap: 14px;
		width: 100%;
	}

	.alias-block {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.alias-row {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.alias-label {
		font-size: 10px;
		font-weight: 600;
		color: var(--text-faint);
		text-transform: uppercase;
		letter-spacing: 0.4px;
	}

	.alias-value {
		font-size: 14px;
		font-weight: 700;
		color: var(--text);
		word-break: break-word;
	}

	.alias-hint {
		font-size: 10px;
		color: var(--text-faint);
		margin-top: 2px;
	}

	/* Observability (Section D) */
	.provider-degraded {
		background: rgba(196, 163, 90, 0.12);
		border: 1px solid var(--warn);
		border-radius: 6px;
		padding: 8px 10px;
		font-size: 11px;
		color: var(--warn);
		line-height: 1.4;
	}

	.provider-checking {
		font-size: 10px;
		color: var(--text-faint);
		line-height: 1.4;
	}

	.provider-healthy {
		font-size: 10px;
		color: var(--accent);
		line-height: 1.4;
	}

	.last-check-info {
		font-size: 10px;
		color: var(--text-faint);
	}

	.instr-block {
		display: flex;
		flex-direction: column;
		gap: 5px;
	}

	.instr {
		font-size: 12px;
		color: var(--text-dim);
		line-height: 1.4;
		margin: 0;
	}

	/* Pulse animation dot (standalone) */
	.pulse-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--accent);
		animation: pulse-dot 1.8s ease-in-out infinite;
		margin: 2px 0;
	}
	@media (prefers-reduced-motion: reduce) {
		.pulse-dot { animation: none; }
	}

	/* ── Pay panel ── */
	.pay-panel {
		min-width: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 18px 22px;
		overflow-y: auto;
	}

	.pay-surface {
		width: 100%;
		max-width: 430px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.inv-status {
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--warn);
	}
	.inv-status.confirmed { color: var(--accent); }
	.inv-status.detected { color: var(--warn); }

	.inv-row {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.addr-row { flex-wrap: wrap; }

	.inv-label {
		font-size: 11px;
		color: var(--text-faint);
		flex: 0 0 54px;
	}

	.inv-val {
		font-size: 13px;
		color: var(--text);
		flex: 1;
		min-width: 0;
		word-break: break-all;
	}
	.inv-val.addr {
		font-family: monospace;
		font-size: 11px;
	}

	.copy-btn {
		background: none;
		border: 1px solid var(--border);
		border-radius: 4px;
		color: var(--text-dim);
		font-size: 10px;
		padding: 2px 7px;
		cursor: pointer;
		white-space: nowrap;
		transition: border-color 0.15s;
	}
	.copy-btn:hover { border-color: var(--accent); color: var(--accent); }

	.qr-wrap {
		display: flex;
		justify-content: center;
		padding-top: 6px;
	}
	/* Desktop default: 180px QR */
	.qr-wrap :global(.v2qr svg) { width: 168px; height: 168px; display: block; }

	/* ── Centered steps (balance / contact / done) ── */
	.step-center {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 24px 20px;
		overflow-y: auto;
	}

	.step-inner {
		width: 100%;
		max-width: 440px;
		display: flex;
		flex-direction: column;
		gap: 16px;
	}

	h2 { font-size: 18px; font-weight: 700; color: var(--text); margin: 0; }
	h3 { font-size: 15px; font-weight: 700; color: var(--text); margin: 0; }
	.sub { color: var(--text-dim); font-size: 14px; line-height: 1.5; margin: 0; }
	.hint { font-size: 12px; color: var(--text-faint); line-height: 1.4; margin: 0; }
	.err { color: var(--danger); font-size: 13px; }
	.expiry { font-size: 11px; color: var(--text-faint); margin: 0; }

	/* ── Contact reveal ── */
	.contact-reveal {
		background: var(--bg-card);
		border: 1px solid var(--accent);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.contact-type-badge {
		font-size: 10px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 1px;
		color: var(--accent);
	}
	.contact-value { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
	.cv-text { font-size: 18px; font-weight: 700; color: var(--text); word-break: break-all; }

	.copy-btn-lg {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-dim);
		font-size: 12px;
		padding: 6px 12px;
		cursor: pointer;
		white-space: nowrap;
		transition: border-color 0.15s;
	}
	.copy-btn-lg:hover { border-color: var(--accent); color: var(--accent); }

	.open-btn {
		background: var(--accent);
		color: var(--bg);
		border: none;
		border-radius: 6px;
		font-size: 12px;
		padding: 6px 12px;
		cursor: pointer;
		white-space: nowrap;
		transition: opacity 0.15s;
	}
	.open-btn:hover { opacity: 0.85; }

	/* ── Review countdown (Section G) ── */
	.review-countdown-box {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.review-not-yet {
		font-size: 12px;
		color: var(--text-dim);
		line-height: 1.4;
		margin: 0;
	}
	.review-countdown {
		font-size: 14px;
		font-weight: 700;
		color: var(--accent);
		font-variant-numeric: tabular-nums;
	}

	/* ── Review ── */
	.review-section {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.review-btns { display: flex; gap: 12px; }
	.review-btn {
		flex: 1;
		padding: 10px;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: var(--bg-card);
		color: var(--text);
		font-size: 14px;
		cursor: pointer;
		transition: all 0.15s;
	}
	.review-btn.positive:hover { border-color: var(--accent); color: var(--accent); }
	.review-btn.negative:hover { border-color: var(--danger); color: var(--danger); }
	.review-done {
		text-align: center;
		font-size: 13px;
		color: var(--accent);
		padding: 8px;
	}

	/* ── Handoff redeem form (second device) ── */
	.field { display: flex; flex-direction: column; gap: 6px; }
	.field input {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		color: var(--text);
		font-family: inherit;
		font-size: 14px;
		padding: 10px 12px;
		outline: none;
		transition: border-color 0.15s;
	}
	.field input:focus { border-color: var(--accent); }

	.btn-primary {
		background: var(--accent);
		color: var(--bg);
		border: none;
		border-radius: 8px;
		padding: 12px 24px;
		font-size: 14px;
		font-weight: 600;
		cursor: pointer;
		transition: opacity 0.15s;
	}
	.btn-primary:hover:not(:disabled) { opacity: 0.85; }
	.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

	/* ── Shared button ── */
	.btn-secondary {
		background: var(--bg-card);
		color: var(--text);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 10px 20px;
		font-size: 14px;
		cursor: pointer;
		transition: border-color 0.15s;
		text-decoration: none;
		display: inline-block;
		text-align: center;
	}
	.btn-secondary:hover { border-color: var(--text-dim); }

	/* ── Desktop: tight height — shrink QR slightly ── */
	@media (min-width: 601px) and (max-height: 780px) {
		.invoice-wrap { padding: 10px 16px; }
		.meta-panel { padding: 14px 16px; }
		.pay-panel { padding: 14px 18px; }
		.qr-wrap :global(.v2qr svg) { width: 154px; height: 154px; }
	}

	/* ── Mobile: single column ── */
	@media (max-width: 600px) {
		.topbar { padding: 0 14px; height: auto; min-height: 44px; flex-wrap: wrap; }
		.topbar-nav { order: 3; flex-basis: 100%; padding-bottom: 6px; }
		.prog-label { display: none; }
		.prog-current { display: inline; }
		.invoice-wrap {
			padding: 8px 10px 48px;
			align-items: center;
			overflow: hidden;
		}

		.invoice-shell {
			width: 100%;
			grid-template-columns: 1fr;
			max-height: 100%;
			overflow-y: auto;
		}

		.meta-panel {
			border-right: none;
			border-bottom: 1px solid var(--border);
			padding: 10px 12px;
			align-items: flex-start;
			overflow-y: visible;
		}

		.meta-inner {
			flex-direction: row;
			flex-wrap: wrap;
			gap: 8px 12px;
		}

		.alias-block { flex: 1 1 140px; min-width: 0; }
		.instr-block { flex: 1 1 140px; min-width: 0; }
		.alias-row { gap: 1px; }
		.alias-value { font-size: 12px; }
		.alias-hint { display: none; }
		.provider-healthy,
		.provider-checking,
		.last-check-info { display: none; }
		.instr { font-size: 11px; line-height: 1.3; }
		.btn-handoff { width: 100%; box-sizing: border-box; }

		.pay-panel {
			padding: 10px 12px 12px;
			overflow-y: visible;
			align-items: flex-start;
			justify-content: flex-start;
		}

		.pay-surface { max-width: 100%; }

		.qr-wrap :global(.v2qr svg) { width: 120px; height: 120px; }
	}

	/* ── Short mobile viewport: keep payment and language controls separate ── */
	@media (max-height: 700px) and (max-width: 600px) {
		.topbar {
			height: 38px;
			min-height: 38px;
			padding: 0 10px;
			flex-wrap: nowrap;
			gap: 8px;
		}
		.logo { display: none; }
		.topbar-nav {
			order: initial;
			flex-basis: auto;
			padding-bottom: 0;
			gap: 8px;
		}
		.progress {
			min-width: 0;
			margin-left: auto;
		}
		.prog-line { width: 14px; }
		.prog-current {
			margin-left: 6px;
			font-size: 10px;
		}
		.invoice-wrap { padding: 6px 8px 46px; }
		.meta-panel { padding: 7px 10px; }
		.pay-panel { padding: 7px 10px 9px; }
		.pay-surface { gap: 7px; }
		.qr-wrap { padding-top: 2px; }
		.qr-wrap :global(.v2qr svg) { width: 112px; height: 112px; }
	}

	/* ── Telegram review reminder (Section C) ── */
	.btn-reminder {
		background: none;
		border: 1px dashed var(--border);
		border-radius: 6px;
		color: var(--text-faint);
		font-size: 11px;
		padding: 6px 10px;
		cursor: pointer;
		transition: border-color 0.15s, color 0.15s;
		align-self: flex-start;
		margin-top: 4px;
	}
	.btn-reminder:hover:not(:disabled) { border-color: var(--accent); color: var(--accent); }
	.btn-reminder:disabled { opacity: 0.5; cursor: not-allowed; }
	.btn-reminder-link {
		font-size: 12px;
		color: var(--accent);
		text-decoration: underline;
		align-self: flex-start;
		margin-top: 4px;
	}
	.reminder-sent {
		font-size: 11px;
		color: var(--accent);
		margin: 4px 0 0;
		line-height: 1.4;
	}

	/* ── Cross-device handoff (Section G) ── */
	.btn-handoff {
		background: none;
		border: 1px dashed var(--border);
		border-radius: 8px;
		color: var(--text-faint);
		font-size: 12px;
		padding: 8px 14px;
		cursor: pointer;
		transition: border-color 0.15s, color 0.15s;
		align-self: flex-start;
	}
	.btn-handoff:hover:not(:disabled) { border-color: var(--text-dim); color: var(--text-dim); }
	.btn-handoff:disabled { opacity: 0.5; cursor: not-allowed; }

	.handoff-box {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 14px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.handoff-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}
	.handoff-scan {
		font-size: 11px;
		color: var(--text-dim);
		line-height: 1.4;
		margin: 0;
	}
	.handoff-qr { display: flex; }
	.handoff-qr :global(.v2qr svg) { width: 140px; height: 140px; display: block; }
	.handoff-expires {
		font-size: 10px;
		color: var(--text-faint);
		margin: 0;
	}
	.btn-text {
		background: none;
		border: none;
		color: var(--text-dim);
		font-size: 12px;
		padding: 0;
		cursor: pointer;
		text-decoration: underline;
		align-self: flex-start;
	}
	.btn-text:hover { color: var(--text); }

</style>
