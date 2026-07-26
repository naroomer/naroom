<script>
	import { onMount } from 'svelte';
	import { lang, t as tFn } from '$lib/i18n.js';
	import V2QR from '$lib/V2QR.svelte';

	let t = $derived((key, params) => tFn($lang, key, params));

	// ── Public config ──────────────────────────────────────────────────────────────
	const DEFAULT_CONFIG = { helper_pre_invoice_min_usd: 1010, helper_post_payment_min_usd: 1000 };
	let pubConfig = $state({ ...DEFAULT_CONFIG });

	async function fetchPubConfig() {
		try {
			const r = await fetch('/api/v2/public-config');
			if (r.ok) pubConfig = { ...DEFAULT_CONFIG, ...(await r.json()) };
		} catch {}
	}

	// purchase_token from sessionStorage only — never exposed in URL/history
	let purchaseToken = $state('');

	let step = $state('loading'); // loading | invoice | balance | contact | review | done
	let loading = $state(true);
	let error = $state('');
	let copyMsg = $state('');

	// Purchase data
	let purchaseId = $state('');
	let walletAddress = $state('');
	let listingId = $state('');
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

	// Review
	let reviewToken = $state('');
	let reviewClientReputation = $state(null);
	let reviewClientName = $state('');
	let reviewSubmitted = $state(false);

	function paymentURI(inv, curr) {
		if (!inv?.payment_address) return '';
		const a = inv.amount_atomic;
		const addr = inv.payment_address;
		if (curr === 'BTC') return `bitcoin:${addr}?amount=${(a/1e8).toFixed(8)}`;
		if (curr === 'LTC') return `litecoin:${addr}?amount=${(a/1e8).toFixed(8)}`;
		return addr;
	}

	onMount(async () => {
		fetchPubConfig();
		// Load token from sessionStorage only (never from URL)
		try { purchaseToken = sessionStorage.getItem('v2_active_purchase_token') || ''; } catch {}

		// Fall back to localStorage if sessionStorage is empty
		if (!purchaseToken) {
			try {
				const lhpt = JSON.parse(localStorage.getItem('v2_active_hpt') || 'null');
				if (lhpt?.token && lhpt?.wallet) {
					purchaseToken = lhpt.token;
					walletAddress = lhpt.wallet;
					listingId = lhpt.listingId || '';
					try { sessionStorage.setItem('v2_active_purchase_token', lhpt.token); } catch {}
				}
			} catch {}
		}

		if (!purchaseToken) { error = t('v2.helper.no_token'); loading = false; return; }

		// Restore purchase data from sessionStorage
		let storageKey = `v2_purchase_${purchaseToken}`;
		let saved = null;
		try { saved = JSON.parse(sessionStorage.getItem(storageKey) || 'null'); } catch {}

		if (saved) {
			purchaseId = saved.purchaseId || '';
			listingId = saved.listingId || '';
			walletAddress = walletAddress || saved.walletAddress || '';
		}

		if (purchaseToken && walletAddress) {
			await restorePurchase();
		} else {
			error = t('v2.helper.no_purchase');
			loading = false;
		}
	});

	async function restorePurchase() {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/v2/helper/contact-purchases/restore', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_token: purchaseToken,
					wallet_address: walletAddress,
				}),
			});
			const data = await res.json();
			if (!res.ok) { error = data.error || `HTTP ${res.status}`; loading = false; return; }

			phase = data.phase;
			purchaseId = data.purchase_id || purchaseId;
			currency = data.currency || 'BTC';
			helperPublicName = data.helper_public_name || '';
			clientPublicName = data.client_public_name || '';
			invoice = data.invoice || null;
			balanceRetryDeadline = data.balance_retry_deadline_at || 0;
			lastBalanceUSD = data.last_balance_usd ?? null;
			receiptExpiresAt = data.receipt_expires_at || 0;

			routeByPhase(data);
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	// Clears all storage keys associated with a helper purchase.
	// Call before showing terminal screen so navigation away is clean.
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
			// Try to load contact
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
			const res = await fetch('/api/v2/helper/contact-purchases/restore', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_token: purchaseToken,
					wallet_address: walletAddress,
				}),
			});
			if (!res.ok) return;
			const data = await res.json();
			phase = data.phase;
			invoice = data.invoice || invoice;
			if (data.phase !== 'awaiting_payment' && data.phase !== 'payment_detected') {
				stopPoll();
				routeByPhase(data);
			}
		} catch {}
	}

	$effect(() => {
		return () => stopPoll();
	});

	// ── Load contact (reveal) ────────────────────────────────────────────────────
	async function loadContact() {
		try {
			const res = await fetch('/api/v2/helper/contact-purchases/reveal', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_token: purchaseToken,
					wallet_address: walletAddress,
				}),
			});
			const data = await res.json();
			if (!res.ok) { error = data.error || `HTTP ${res.status}`; return; }
			contactType = data.contact_type;
			contactValue = data.contact;
			receiptExpiresAt = data.receipt_expires_at;
			// Try to load review capability
			await loadReviewCapability();
		} catch (e) {
			error = e.message;
		}
	}

	// ── Review ───────────────────────────────────────────────────────────────────
	async function loadReviewCapability() {
		try {
			const res = await fetch('/api/v2/helper/reviews/capability', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_id: purchaseId,
					purchase_token: purchaseToken,
					wallet_address: walletAddress,
				}),
			});
			if (!res.ok) return; // Review not available — silently skip
			const data = await res.json();
			reviewToken = data.review_token || '';
			reviewClientReputation = data.client_reputation || null;
			reviewClientName = data.client_display_name || '';
		} catch {}
	}

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

	function openContact() {
		if (!contactValue) return;
		if (contactType === 'telegram') {
			const handle = contactValue.startsWith('@') ? contactValue.slice(1) : contactValue;
			window.open(`https://t.me/${handle}`, '_blank', 'noopener,noreferrer');
		} else if (contactType === 'signal') {
			window.open(`https://signal.me/#p/${contactValue}`, '_blank', 'noopener,noreferrer');
		}
	}
</script>

<div class="page">
	<header>
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
	</header>

	{#if loading && step === 'loading'}
		<div class="status-msg">{t('v2.loading')}</div>

	{:else if step === 'invoice'}
		<div class="section">
			<h2>{t('v2.helper.invoice_title')}</h2>

			<!-- Progress: step 2 = Payment -->
			<div class="progress-bar">
				{#each [1,2,3,4,5] as n}
					<div class="prog-step" class:done={2 > n} class:active={2 === n}>
						<div class="prog-dot"></div>
						<span class="prog-label">{t('v2.helper.progress.step' + n)}</span>
					</div>
					{#if n < 5}<div class="prog-line" class:done={2 > n}></div>{/if}
				{/each}
			</div>

			<!-- Helper's nickname -->
			{#if helperPublicName}
				<div class="nickname-section">
					<div class="nickname-label">{t('v2.helper.your_nickname')}</div>
					<div class="nickname-value">{helperPublicName}</div>
					<div class="nickname-hint">{t('v2.helper.nickname_permanent')}</div>
				</div>
			{/if}

			<!-- Who they're buying contact for -->
			{#if clientPublicName}
				<div class="nickname-section">
					<div class="nickname-label">{t('v2.helper.buying_contact_for')}</div>
					<div class="nickname-value">{clientPublicName}</div>
				</div>
			{/if}

			{#if invoice}
				<div class="invoice-box">
					<div class="inv-status" class:confirmed={phase === 'contact_ready' || phase === 'payment_confirmed'} class:detected={phase === 'payment_detected'}>
						{#if phase === 'awaiting_payment'}
							{t('v2.invoice.pending')}
						{:else if phase === 'payment_detected'}
							{t('v2.invoice.payment_detected')}
						{:else}
							{t('v2.invoice.confirmed')}
						{/if}
					</div>
					<div class="inv-row">
						<span class="inv-label">{t('v2.invoice.amount')}</span>
						<span class="inv-val">{(invoice.amount_atomic / 1e8).toFixed(8)} {currency}</span>
						<button class="copy-btn" onclick={() => copyText((invoice.amount_atomic / 1e8).toFixed(8), t('v2.copied'))}>
							{copyMsg || t('v2.copy')}
						</button>
					</div>
					<div class="inv-row">
						<span class="inv-label">{t('v2.invoice.address')}</span>
						<span class="inv-val addr">{invoice.payment_address}</span>
						<button class="copy-btn" onclick={() => copyText(invoice.payment_address, t('v2.copied'))}>
							{copyMsg || t('v2.copy')}
						</button>
					</div>
					<div class="inv-row">
						<span class="inv-label">{t('v2.invoice.usd')}</span>
						<span class="inv-val">${(invoice.amount_usd_cents / 100).toFixed(2)}</span>
					</div>
					{#if phase === 'awaiting_payment' || phase === 'payment_detected'}
						{#if invoice.payment_address}
							<div class="qr-wrap">
								<V2QR data={paymentURI(invoice, currency)} />
							</div>
						{/if}
					{/if}
				</div>

				<!-- Auto-check info box -->
				{#if phase === 'awaiting_payment' || phase === 'payment_detected'}
					<div class="info-box">
						<ul class="info-list">
							<li>{t('v2.helper.payment_auto_check')}</li>
							<li>{t('v2.helper.one_confirmation')}</li>
							<li>{t('v2.helper.balance_auto_check')}</li>
							<li>{t('v2.helper.can_restore')}</li>
							<li>{t('v2.helper.no_repay')}</li>
						</ul>
					</div>
				{/if}
			{/if}

			{#if error}<div class="err">{error}</div>{/if}
		</div>

	{:else if step === 'balance'}
		<div class="section">
			<h2>{t('v2.balance.title')}</h2>
			{#if lastBalanceUSD !== null}
				<div class="err">{t('v2.balance.low', { balance: lastBalanceUSD.toFixed(0), min: '$' + pubConfig.helper_post_payment_min_usd })}</div>
			{/if}
			<p class="sub">{t('v2.helper.balance_sub')}</p>
			<button class="btn-secondary" onclick={restorePurchase} disabled={loading}>
				{loading ? t('v2.loading') : t('v2.balance.recheck')}
			</button>
			{#if error}<div class="err">{error}</div>{/if}
		</div>

	{:else if step === 'contact'}
		<div class="section">
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
					{/if}
				</div>
			{/if}

			<!-- Review section -->
			{#if reviewToken && !reviewSubmitted}
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

			{#if error}<div class="err">{error}</div>{/if}
		</div>

	{:else if step === 'done'}
		<div class="section">
			<h2>{t('v2.helper.done_title')}</h2>
			{#if error}<p class="err">{error}</p>{/if}
			<a href="/v2/board/tbilisi" class="btn-secondary">{t('v2.listing.back')}</a>
		</div>
	{/if}
</div>

<style>
	.page {
		max-width: 540px;
		margin: 0 auto;
		padding: 0 16px 60px;
	}

	header { padding: 20px 0 32px; }
	.logo { font-size: 18px; font-weight: 600; color: var(--text); }
	.v2-badge {
		font-size: 11px;
		background: var(--accent);
		color: var(--bg);
		border-radius: 4px;
		padding: 1px 5px;
		margin-left: 4px;
		font-weight: 700;
		vertical-align: middle;
	}

	.status-msg { text-align: center; padding: 40px 20px; color: var(--text-dim); }

	/* Progress bar */
	.progress-bar {
		display: flex;
		align-items: flex-start;
		gap: 0;
		margin: 4px 0 8px;
		overflow-x: auto;
	}
	.prog-step {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 4px;
		min-width: 56px;
	}
	.prog-dot {
		width: 10px; height: 10px;
		border-radius: 50%;
		background: var(--border);
		border: 2px solid var(--border);
		flex-shrink: 0;
	}
	.prog-step.done .prog-dot { background: var(--accent); border-color: var(--accent); opacity: 0.5; }
	.prog-step.active .prog-dot { background: var(--accent); border-color: var(--accent); }
	.prog-label { font-size: 10px; color: var(--text-faint); text-align: center; white-space: nowrap; }
	.prog-step.done .prog-label, .prog-step.active .prog-label { color: var(--text-dim); }
	.prog-line {
		flex: 1;
		height: 2px;
		background: var(--border);
		margin-top: 4px;
		min-width: 12px;
	}
	.prog-line.done { background: var(--accent); opacity: 0.4; }

	/* Nickname sections */
	.nickname-section {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 10px 14px;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.nickname-label {
		font-size: 11px;
		font-weight: 600;
		color: var(--text-faint);
		text-transform: uppercase;
		letter-spacing: 0.5px;
	}
	.nickname-value {
		font-size: 15px;
		font-weight: 700;
		color: var(--text);
	}
	.nickname-hint {
		font-size: 11px;
		color: var(--text-faint);
	}

	/* Info box */
	.info-box {
		background: rgba(123, 166, 142, 0.06);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 12px 14px;
	}
	.info-list {
		margin: 0;
		padding-left: 18px;
		display: flex;
		flex-direction: column;
		gap: 5px;
	}
	.info-list li {
		font-size: 12px;
		color: var(--text-dim);
		line-height: 1.4;
	}

	.section { display: flex; flex-direction: column; gap: 16px; }
	h2 { font-size: 18px; font-weight: 700; color: var(--text); margin: 0; }
	h3 { font-size: 15px; font-weight: 700; color: var(--text); margin: 0; }
	.sub { color: var(--text-dim); font-size: 14px; line-height: 1.5; margin: 0; }
	.hint { font-size: 12px; color: var(--text-faint); line-height: 1.4; margin: 0; }
	.err { color: var(--danger); font-size: 13px; }
	.expiry { font-size: 11px; color: var(--text-faint); }

	.invoice-box {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.inv-status {
		font-size: 12px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--warn);
	}
	.inv-status.confirmed { color: var(--accent); }
	.inv-status.detected { color: var(--warn); }
	.inv-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
	.inv-label { font-size: 12px; color: var(--text-faint); flex: 0 0 70px; }
	.inv-val { font-size: 13px; color: var(--text); flex: 1; word-break: break-all; }
	.inv-val.addr { font-family: monospace; font-size: 12px; }

	.copy-btn {
		background: none;
		border: 1px solid var(--border);
		border-radius: 4px;
		color: var(--text-dim);
		font-size: 11px;
		padding: 2px 8px;
		cursor: pointer;
		white-space: nowrap;
		transition: border-color 0.15s;
	}
	.copy-btn:hover { border-color: var(--accent); color: var(--accent); }

	.qr-wrap { display: flex; justify-content: center; padding: 8px 0; }
	.qr { width: 160px; height: 160px; border-radius: 6px; }

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
	.contact-value { display: flex; align-items: center; gap: 12px; }
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
</style>
