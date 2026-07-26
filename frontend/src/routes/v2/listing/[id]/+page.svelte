<script>
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { lang, t as tFn } from '$lib/i18n.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	let id = $derived(page.params.id);
	let listing = $state(null);
	let loading = $state(true);
	let error = $state('');

	// Helper purchase initiation
	let purchaseToken = $state('');
	let helperWallet = $state('');
	let helperLoading = $state(false);
	let helperError = $state('');
	let balanceOutage = $state(false);

	// Purchase state for restore flow
	let purchaseView = $state(null); // set after create/restore
	let purchasePhase = $state(''); // 'awaiting_payment' | 'payment_detected' | etc.
	let savedPurchaseState = $state(null); // { phase, purchase_id } from localStorage restore
	let checkingRestore = $state(false);

	// Public config for threshold display
	const DEFAULT_HELPER_CONFIG = { helper_pre_invoice_min_usd: 1010, helper_post_payment_min_usd: 1000, helper_fee_usd_cents: 1000 };
	let pubConfig = $state({ ...DEFAULT_HELPER_CONFIG });

	async function fetchPubConfig() {
		try {
			const r = await fetch('/api/v2/public-config');
			if (r.ok) pubConfig = { ...DEFAULT_HELPER_CONFIG, ...(await r.json()) };
		} catch {}
	}

	// Progress steps: 1=Wallet, 2=Payment, 3=Confirmation, 4=Balance, 5=Contact
	let progressStep = $derived(() => {
		switch (purchasePhase) {
			case 'awaiting_payment':  return 2;
			case 'payment_detected':  return 3;
			case 'payment_confirmed': return 4;
			case 'paid_low_balance':  return 4;
			case 'contact_ready':     return 5;
			default:                  return purchasePhase ? 2 : 1;
		}
	});

	function urgencyColor(u) {
		if (u === 'urgent') return 'var(--urgent)';
		if (u === 'soon')   return 'var(--warn)';
		return 'var(--can-wait)';
	}

	function reputationAge(memberSinceUnix) {
		if (!memberSinceUnix) return '';
		const days = Math.floor((Date.now() / 1000 - memberSinceUnix) / 86400);
		if (days < 7) return t('v2.rep.days', { n: days });
		if (days < 30) return t('v2.rep.weeks', { n: Math.floor(days / 7) });
		if (days < 365) return t('v2.rep.months', { n: Math.floor(days / 30) });
		return t('v2.rep.years', { n: Math.floor(days / 365) });
	}

	function detectCurrency(addr) {
		const a = (addr || '').trim();
		if (!a) return null;
		if (/^ltc1/i.test(a) || /^[LM]/.test(a)) return 'LTC';
		if (/^bc1/i.test(a) || /^[13]/.test(a)) return 'BTC';
		return null;
	}

	let detectedCurrency = $derived(detectCurrency(helperWallet) || 'BTC');

	async function loadListing() {
		loading = true;
		error = '';
		try {
			const res = await fetch(`/api/v2/listings/${id}`);
			if (res.status === 404) { error = t('v2.listing.not_found'); return; }
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			listing = await res.json();
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	onMount(loadListing);

	// Generate or load purchase_token for this listing, and restore any saved purchase state.
	onMount(async () => {
		fetchPubConfig();

		const storageKey = `v2_pt_${id}`;
		let pt = '';
		try { pt = sessionStorage.getItem(storageKey) || ''; } catch {}
		if (!pt) {
			// 64 lowercase hex chars
			const arr = new Uint8Array(32);
			crypto.getRandomValues(arr);
			pt = Array.from(arr).map(b => b.toString(16).padStart(2, '0')).join('');
			try { sessionStorage.setItem(storageKey, pt); } catch {}
		}
		purchaseToken = pt;

		// Check for an existing purchase saved for this listing.
		const saved = (() => {
			try { return JSON.parse(sessionStorage.getItem(`v2_purchase_${pt}`) || 'null'); } catch { return null; }
		})();
		if (saved && saved.listingId === id) {
			purchaseView = saved;
			purchasePhase = saved.phase || 'awaiting_payment';
		}

		// Check localStorage for a saved purchase token for this listing
		const lhptRaw = (() => { try { return localStorage.getItem(`v2_hpt_${id}`); } catch { return null; } })();
		if (lhptRaw) {
			const lhpt = JSON.parse(lhptRaw);
			if (lhpt?.token && lhpt?.wallet) {
				checkingRestore = true;
				try {
					const res = await fetch('/api/v2/helper/contact-purchases/restore', {
						method: 'POST',
						headers: { 'Content-Type': 'application/json' },
						body: JSON.stringify({ purchase_token: lhpt.token, wallet_address: lhpt.wallet }),
					});
					if (res.ok) {
						const data = await res.json();
						const terminal = ['payment_expired', 'receipt_expired', 'failed'].includes(data.phase);
						if (!terminal) {
							savedPurchaseState = { phase: data.phase, purchase_id: data.purchase_id };
							// Seed sessionStorage so purchase page can restore
							try {
								sessionStorage.setItem('v2_active_purchase_token', lhpt.token);
								sessionStorage.setItem(`v2_purchase_${lhpt.token}`, JSON.stringify({
									purchaseId: data.purchase_id,
									purchaseToken: lhpt.token,
									listingId: id,
									walletAddress: lhpt.wallet,
								}));
							} catch {}
						} else {
							// Clear stale localStorage token
							try { localStorage.removeItem(`v2_hpt_${id}`); } catch {}
							// Clear stale sessionStorage so purchaseView/purchasePhase don't drive a state-box
							try {
								const stalePt = sessionStorage.getItem(`v2_pt_${id}`) || '';
								if (stalePt) {
									sessionStorage.removeItem('v2_active_purchase_token');
									sessionStorage.removeItem(`v2_purchase_${stalePt}`);
									sessionStorage.removeItem(`v2_pt_${id}`);
								}
							} catch {}
							purchaseView = null;
							purchasePhase = '';
						}
					} else {
						// 404 or other error — clear stale token
						try { localStorage.removeItem(`v2_hpt_${id}`); } catch {}
					}
				} catch {} finally {
					checkingRestore = false;
				}
			}
		}
	});

	async function startHelperPurchase() {
		if (!helperWallet.trim()) return;
		helperLoading = true;
		helperError = '';
		try {
			const res = await fetch('/api/v2/helper/contact-purchases', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_token: purchaseToken,
					listing_id: id,
					wallet_address: helperWallet.trim(),
				}),
			});
			const data = await res.json();
			if (!res.ok) {
				if (data.code === 'balance_provider_unavailable') {
					balanceOutage = true;
					helperError = t('v2.helper.balance_unavailable');
				} else {
					balanceOutage = false;
					helperError = data.error || `HTTP ${res.status}`;
				}
				return;
			}
			balanceOutage = false;
			// Save purchase data including nickname fields so helper/purchase page can restore
			try {
				sessionStorage.setItem(`v2_purchase_${purchaseToken}`, JSON.stringify({
					purchaseId: data.purchase_id,
					purchaseToken: purchaseToken,
					listingId: id,
					walletAddress: helperWallet.trim(),
					helperPublicName: data.helper_public_name,
					clientPublicName: data.client_public_name,
				}));
			} catch {}
			try { sessionStorage.setItem('v2_active_purchase_token', purchaseToken); } catch {}
			// Save to localStorage for cross-session restore
			try {
				const lhptData = JSON.stringify({ token: purchaseToken, wallet: helperWallet.trim() });
				localStorage.setItem(`v2_hpt_${id}`, lhptData);
				localStorage.setItem('v2_active_hpt', JSON.stringify({ token: purchaseToken, wallet: helperWallet.trim(), listingId: id }));
			} catch {}
			// Show inline purchase state before redirecting
			purchaseView = data;
			purchasePhase = 'awaiting_payment';
			window.location.href = '/v2/helper/purchase';
		} catch (e) {
			helperError = e.message;
		} finally {
			helperLoading = false;
		}
	}

</script>

<div class="page">
	<header>
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
		<nav>
			<a href="/v2/board/{listing?.city || 'tbilisi'}" class="back-link">
				← {t('v2.listing.back')}
			</a>
		</nav>
	</header>

	{#if loading}
		<div class="status-msg">{t('v2.loading')}</div>
	{:else if error}
		<div class="status-msg error">{error}</div>
	{:else if listing}
		<div class="listing-card">
			<div class="urgency-strip" style="background: {urgencyColor(listing.urgency)}"></div>
			<div class="listing-body">
				<div class="listing-head">
					<div class="dep">{t('dep.' + listing.dependency_type)}</div>
					<span class="urgency-tag" style="color: {urgencyColor(listing.urgency)}">
						{t('urgency.' + listing.urgency)}
					</span>
				</div>
				<div class="help">{t('help.' + listing.help_type)}</div>
				<div class="meta-row">
					<span class="langs">{(listing.languages || []).join(', ').toUpperCase()}</span>
					{#if listing.client_reputation}
						<span class="rep-age">{reputationAge(listing.client_reputation.member_since)}</span>
						{#if listing.client_reputation.positive_count > 0 || listing.client_reputation.negative_count > 0}
							<span class="rep-score">
								👍{listing.client_reputation.positive_count}
								{#if listing.client_reputation.negative_count > 0}
								 👎{listing.client_reputation.negative_count}
								{/if}
							</span>
						{/if}
					{/if}
				</div>
				{#if listing.display_name}
					<div class="client-name">{listing.display_name}</div>
				{/if}
			</div>
		</div>

		<!-- Helper purchase section -->
		<div class="helper-section">
			<h2>{t('v2.listing.helper_title')}</h2>
			<p class="sub">{t('v2.listing.helper_sub')}</p>

			<!-- 5-step progress indicator -->
			<div class="progress-bar">
				{#each [1, 2, 3, 4, 5] as step}
					<div class="progress-step" class:active={progressStep() >= step} class:current={progressStep() === step}>
						<div class="progress-dot"></div>
						<div class="progress-label">{t('v2.helper.progress.step' + step)}</div>
					</div>
					{#if step < 5}
						<div class="progress-line" class:done={progressStep() > step}></div>
					{/if}
				{/each}
			</div>

			<!-- Show nickname sections after purchase is created -->
			{#if purchaseView?.helperPublicName}
				<div class="nickname-section">
					<div class="nickname-label">{t('v2.helper.your_nickname')}</div>
					<div class="nickname-value">{purchaseView.helperPublicName}</div>
					<div class="nickname-hint">{t('v2.helper.nickname_permanent')}</div>
				</div>
			{/if}

			{#if purchaseView?.clientPublicName}
				<div class="nickname-section">
					<div class="nickname-label">{t('v2.helper.buying_contact_for')}</div>
					<div class="nickname-value">{purchaseView.clientPublicName}</div>
				</div>
			{/if}

			<!-- Restore flow states -->
			{#if purchasePhase === 'awaiting_payment' && purchaseView}
				<div class="state-box info">
					<div class="state-title">{t('v2.helper.invoice_title')}</div>
					<ul class="info-list">
						<li>{t('v2.helper.payment_auto_check')}</li>
						<li>{t('v2.helper.one_confirmation')}</li>
						<li>{t('v2.helper.balance_auto_check')}</li>
						<li>{t('v2.helper.can_restore')}</li>
						<li>{t('v2.helper.no_repay')}</li>
					</ul>
					<a href="/v2/helper/purchase" class="btn-primary btn-link">{t('v2.helper.open_contact')}</a>
				</div>
			{:else if purchasePhase === 'payment_detected' && purchaseView}
				<div class="state-box info">
					<div class="state-title">{t('waiting_confirmation')}</div>
					<ul class="info-list">
						<li>{t('v2.helper.one_confirmation')}</li>
						<li>{t('v2.helper.can_restore')}</li>
					</ul>
					<a href="/v2/helper/purchase" class="btn-primary btn-link">{t('v2.helper.open_contact')}</a>
				</div>
			{:else if purchasePhase === 'payment_confirmed' && purchaseView}
				<div class="state-box info">
					<div class="state-title">{t('checking_auto')}</div>
					<ul class="info-list">
						<li>{t('v2.helper.balance_auto_check')}</li>
						<li>{t('v2.helper.can_restore')}</li>
					</ul>
					<a href="/v2/helper/purchase" class="btn-primary btn-link">{t('v2.helper.open_contact')}</a>
				</div>
			{:else if purchasePhase === 'paid_low_balance' && purchaseView}
				<div class="state-box warn">
					<div class="state-title">{t('v2.helper.balance_sub')}</div>
					<a href="/v2/helper/purchase" class="btn-primary btn-link">{t('v2.helper.open_contact')}</a>
				</div>
			{:else if purchasePhase === 'contact_ready' && purchaseView}
				<div class="state-box ok">
					<div class="state-title">{t('v2.helper.contact_title')}</div>
					<a href="/v2/helper/purchase" class="btn-primary btn-link">{t('v2.helper.open_contact')}</a>
				</div>
			{:else if checkingRestore}
				<div class="status-msg">{t('v2.loading')}</div>
			{:else if savedPurchaseState && !purchasePhase}
				<div class="state-box info">
					<div class="state-title">{t('v2.helper.continue_hint')}</div>
					<a href="/v2/helper/purchase" class="btn-primary btn-link">{t('v2.helper.continue_purchase')}</a>
				</div>
			{:else}
				<!-- Initial purchase form -->
				<div class="notice-box">
					<div class="notice-title">{t('v2.helper.notice_title')}</div>
					<ul class="notice-list">
						<li>{t('v2.helper.notice_country')}</li>
						<li>{t('v2.helper.notice_wallet')}</li>
						<li>{t('v2.helper.notice_balance', { post_min: '$' + Math.round(pubConfig.helper_post_payment_min_usd) })}</li>
						<li>{t('v2.helper.notice_no_refund')}</li>
					</ul>
				</div>

				<div class="field">
					<label>{t('v2.listing.helper_wallet_label')}</label>
					<input
						bind:value={helperWallet}
						placeholder={t('v2.listing.helper_wallet_ph')}
						type="text"
						autocomplete="off"
						spellcheck="false"
						class:detected={!!detectCurrency(helperWallet)}
					/>
					{#if detectCurrency(helperWallet)}
						<div class="currency-tag">{detectCurrency(helperWallet)} {t('v2.client.detected')}</div>
					{/if}
					<p class="hint">{t('v2.listing.helper_wallet_hint', { min: '$' + Math.round(pubConfig.helper_pre_invoice_min_usd) })}</p>
				</div>

				{#if helperError}
					<div class="err">{helperError}</div>
				{/if}

				{#if balanceOutage}
					<button class="btn-secondary" onclick={startHelperPurchase} disabled={helperLoading}>
						{helperLoading ? t('v2.loading') : t('v2.helper.balance_retry')}
					</button>
				{/if}

				<button
					class="btn-primary"
					onclick={startHelperPurchase}
					disabled={helperLoading || !helperWallet.trim() || balanceOutage}
				>
					{helperLoading ? t('v2.loading') : t('v2.listing.helper_btn')}
				</button>

				<p class="fine-print">{t('v2.listing.helper_fine_print', { fee: '$' + Math.round(pubConfig.helper_fee_usd_cents / 100) })}</p>
			{/if}
		</div>
	{/if}
</div>

<style>
	.page {
		max-width: 600px;
		margin: 0 auto;
		padding: 0 16px 60px;
	}

	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 20px 0 24px;
		border-bottom: 1px solid var(--border);
		margin-bottom: 24px;
	}

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
	.back-link { font-size: 13px; color: var(--text-dim); }
	.back-link:hover { color: var(--text); }

	.status-msg { text-align: center; padding: 40px 20px; color: var(--text-dim); }
	.status-msg.error { color: var(--danger); }

	.listing-card {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		overflow: hidden;
		margin-bottom: 24px;
	}
	.urgency-strip { height: 4px; width: 100%; }
	.listing-body { padding: 16px; display: flex; flex-direction: column; gap: 8px; }
	.listing-head { display: flex; align-items: center; justify-content: space-between; }
	.dep { font-size: 18px; font-weight: 700; color: var(--text); }
	.urgency-tag { font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.5px; }
	.help { font-size: 13px; color: var(--text-dim); }
	.meta-row { display: flex; align-items: center; gap: 12px; margin-top: 4px; }
	.langs { font-size: 11px; color: var(--text-faint); }
	.rep-age { font-size: 10px; color: var(--text-faint); }
	.rep-score { font-size: 12px; color: var(--text-dim); }

	.helper-section { display: flex; flex-direction: column; gap: 16px; }
	h2 { font-size: 18px; font-weight: 700; color: var(--text); margin: 0; }
	.sub { color: var(--text-dim); font-size: 14px; line-height: 1.5; margin: 0; }

	.field { display: flex; flex-direction: column; gap: 6px; }
	.field label { font-size: 13px; font-weight: 600; color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.5px; }

	input {
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
	input:focus { border-color: var(--accent); }
	input.detected { border-color: var(--accent); }

	.currency-tag { font-size: 11px; color: var(--accent); font-weight: 600; letter-spacing: 0.5px; text-transform: uppercase; }
	.hint { font-size: 12px; color: var(--text-faint); line-height: 1.4; margin: 0; }
	.fine-print { font-size: 12px; color: var(--text-faint); line-height: 1.4; }
	.err { color: var(--danger); font-size: 13px; }

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

	.notice-box {
		background: rgba(196, 163, 90, 0.08);
		border: 1px solid var(--warn);
		border-radius: 8px;
		padding: 12px 14px;
	}
	.notice-title { font-size: 12px; font-weight: 700; color: var(--warn); text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 8px; }
	.notice-list { margin: 0; padding: 0 0 0 16px; display: flex; flex-direction: column; gap: 4px; }
	.notice-list li { font-size: 12px; color: var(--text-dim); line-height: 1.4; }
	.client-name { font-size: 11px; color: var(--text-faint); font-style: italic; }

	/* Progress bar */
	.progress-bar {
		display: flex;
		align-items: center;
		gap: 0;
		margin-bottom: 4px;
	}
	.progress-step {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 4px;
		flex-shrink: 0;
	}
	.progress-dot {
		width: 10px;
		height: 10px;
		border-radius: 50%;
		background: var(--border);
		transition: background 0.2s;
	}
	.progress-step.active .progress-dot { background: var(--accent); }
	.progress-step.current .progress-dot {
		box-shadow: 0 0 0 3px rgba(var(--accent-rgb, 100, 180, 255), 0.25);
	}
	.progress-label {
		font-size: 10px;
		color: var(--text-faint);
		white-space: nowrap;
	}
	.progress-step.active .progress-label { color: var(--accent); }
	.progress-line {
		flex: 1;
		height: 2px;
		background: var(--border);
		margin-bottom: 14px;
		transition: background 0.2s;
	}
	.progress-line.done { background: var(--accent); }

	/* Nickname sections */
	.nickname-section {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 10px 14px;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.nickname-label { font-size: 11px; font-weight: 600; color: var(--text-faint); text-transform: uppercase; letter-spacing: 0.5px; }
	.nickname-value { font-size: 15px; font-weight: 600; color: var(--text); }
	.nickname-hint { font-size: 11px; color: var(--text-faint); }

	/* State boxes for restore flow */
	.state-box {
		border-radius: 8px;
		padding: 14px 16px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.state-box.info { background: rgba(100, 180, 255, 0.07); border: 1px solid rgba(100, 180, 255, 0.3); }
	.state-box.warn { background: rgba(196, 163, 90, 0.08); border: 1px solid var(--warn); }
	.state-box.ok   { background: rgba(80, 200, 120, 0.08); border: 1px solid rgba(80, 200, 120, 0.4); }
	.state-title { font-size: 13px; font-weight: 600; color: var(--text); }
	.info-list { margin: 0; padding: 0 0 0 16px; display: flex; flex-direction: column; gap: 4px; }
	.info-list li { font-size: 12px; color: var(--text-dim); line-height: 1.4; }

	/* Button as link */
	.btn-link {
		display: inline-block;
		text-decoration: none;
		text-align: center;
		width: fit-content;
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
		font-family: inherit;
	}
	.btn-secondary:hover:not(:disabled) { border-color: var(--text-dim); }
	.btn-secondary:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
