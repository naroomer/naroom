<script>
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { lang, t as tFn } from '$lib/i18n.js';
	import SeoHead from '$lib/SeoHead.svelte';
	// Cities loaded from API — no static registry in V2 frontend.
	let citiesData = $state([]);

	let t = $derived((key, params) => tFn($lang, key, params));

	// Terminal phases — these get cleaned up
	const TERMINAL_PHASES = new Set(['payment_expired', 'receipt_expired', 'failed']);

	/**
	 * Map backend phase to a human-readable i18n key.
	 */
	function phaseLabel(ph) {
		switch (ph) {
			case 'awaiting_payment':  return t('v2.helper.purchase_phase_awaiting');
			case 'payment_detected':  return t('v2.helper.purchase_phase_detected');
			case 'payment_confirmed': return t('v2.helper.purchase_phase_confirmed');
			case 'paid_low_balance':  return t('v2.helper.purchase_phase_balance');
			case 'contact_ready':     return t('v2.helper.purchase_phase_contact_ready');
			case 'review_pending':    return t('v2.helper.purchase_phase_review');
			default:                  return t('v2.helper.purchase_phase_done');
		}
	}

	/**
	 * @typedef {Object} PurchaseEntry
	 * @property {string} listingId
	 * @property {string} token
	 * @property {string} wallet
	 * @property {string} city
	 * @property {string} phase
	 * @property {string} clientPublicName
	 * @property {string} helperPublicName
	 * @property {number} receiptExpiresAt
	 * @property {boolean} loading
	 * @property {boolean} terminal
	 */

	/** @type {PurchaseEntry[]} */
	let purchases = $state([]);
	let pageLoading = $state(true);

	onMount(async () => {
		// Load city labels from API
		fetch('/api/v2/board/cities').then(r => r.ok ? r.json() : []).then(d => { citiesData = d; }).catch(() => {});

		// Collect all v2_hpt_* keys from localStorage
		const entries = [];
		try {
			for (let i = 0; i < localStorage.length; i++) {
				const key = localStorage.key(i);
				if (!key || !key.startsWith('v2_hpt_')) continue;
				const listingId = key.replace('v2_hpt_', '');
				try {
					const raw = JSON.parse(localStorage.getItem(key) || 'null');
					if (raw?.token && raw?.wallet) {
						entries.push({
							listingId,
							token: raw.token,
							wallet: raw.wallet,
							city: raw.city || '',
							phase: '',
							clientPublicName: '',
							helperPublicName: '',
							receiptExpiresAt: 0,
							loading: true,
							terminal: false,
						});
					}
				} catch {}
			}
		} catch {}

		purchases = entries;
		pageLoading = false;

		// Restore each purchase concurrently
		await Promise.all(purchases.map((entry, idx) => restoreEntry(idx)));
	});

	async function restoreEntry(idx) {
		const entry = purchases[idx];
		try {
			const ctrl = new AbortController();
			const tid = setTimeout(() => ctrl.abort(), 10000);
			const res = await fetch('/api/v2/helper/contact-purchases/restore', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					purchase_token: entry.token,
					wallet_address: entry.wallet,
				}),
				signal: ctrl.signal,
			});
			clearTimeout(tid);

			if (!res.ok) {
				// 404 or other — treat as stale, remove
				try { localStorage.removeItem(`v2_hpt_${entry.listingId}`); } catch {}
				purchases = purchases.map((p, i) => i === idx ? { ...p, loading: false, terminal: true } : p);
				return;
			}

			const data = await res.json();
			const isTerminal = TERMINAL_PHASES.has(data.phase);

			if (isTerminal) {
				try { localStorage.removeItem(`v2_hpt_${entry.listingId}`); } catch {}
			}

			purchases = purchases.map((p, i) => i === idx ? {
				...p,
				phase: data.phase,
				clientPublicName: data.client_public_name || '',
				helperPublicName: data.helper_public_name || '',
				receiptExpiresAt: data.receipt_expires_at || 0,
				loading: false,
				terminal: isTerminal,
			} : p);
		} catch {
			purchases = purchases.map((p, i) => i === idx ? { ...p, loading: false } : p);
		}
	}

	// Visible (non-terminal) purchases
	let visiblePurchases = $derived(purchases.filter(p => !p.terminal));

	function cityLabel(cityId) {
		const found = citiesData.find(c => c.id === cityId);
		return found ? found.label : cityId;
	}

	function purchaseHref(p) {
		// Seed sessionStorage so purchase page can restore
		try {
			sessionStorage.setItem('v2_active_purchase_token', p.token);
		} catch {}
		return '/v2/helper/purchase';
	}

	function isActionable(ph) {
		return ['awaiting_payment', 'payment_detected', 'payment_confirmed', 'paid_low_balance', 'contact_ready'].includes(ph);
	}

	function formatExpiry(unix) {
		if (!unix) return '';
		return new Date(unix * 1000).toLocaleString();
	}
</script>

<SeoHead robots="noindex, nofollow, noarchive" canonicalUrl={page.url.origin + page.url.pathname} />

<div class="page">
	<header>
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
		<nav>
			<a href="/v2/board/tbilisi" class="back-link">{t('v2.helper.back_to_board')}</a>
		</nav>
	</header>

	<div class="content">
		<h1>{t('v2.helper.purchases_title')}</h1>

		{#if pageLoading}
			<div class="status-msg">{t('v2.loading')}</div>
		{:else if visiblePurchases.length === 0}
			<div class="empty-state">
				<p class="empty-text">{t('v2.helper.purchases_empty')}</p>
				<a href="/v2/board/tbilisi" class="btn-primary">{t('v2.board.i_need_help')}</a>
			</div>
		{:else}
			<div class="purchase-list">
				{#each visiblePurchases as p}
					<div class="purchase-card">
						<div class="purchase-card-top">
							{#if p.loading}
								<div class="phase-label muted">{t('v2.loading')}</div>
							{:else}
								<div class="phase-label">{phaseLabel(p.phase)}</div>
							{/if}
							{#if p.city}
								<div class="city-label">{cityLabel(p.city)}</div>
							{/if}
						</div>

						{#if p.clientPublicName}
							<div class="nickname-row">
								<span class="nickname-label">{t('v2.rep.label')}:</span>
								<span class="nickname-value">{p.clientPublicName}</span>
							</div>
						{/if}

						{#if p.receiptExpiresAt > 0}
							<div class="expiry-row">
								{t('v2.helper.receipt_expires', { time: formatExpiry(p.receiptExpiresAt) })}
							</div>
						{/if}

						{#if !p.loading && isActionable(p.phase)}
							<a
								href={purchaseHref(p)}
								onclick={() => {
									try { sessionStorage.setItem('v2_active_purchase_token', p.token); } catch {}
								}}
								class="btn-action"
							>
								{phaseLabel(p.phase)}
							</a>
						{/if}

						<div class="listing-link-row">
							<a href="/v2/listing/{p.listingId}" class="listing-link">
								{t('v2.helper.back_to_listing')}
							</a>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>
</div>

<style>
	.page {
		max-width: 720px;
		margin: 0 auto;
		padding: 0 16px 60px;
	}

	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 20px 0 16px;
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

	nav { display: flex; gap: 16px; align-items: center; }
	.back-link { font-size: 13px; color: var(--text-dim); text-decoration: none; }
	.back-link:hover { color: var(--text); }

	.content { display: flex; flex-direction: column; gap: 20px; }

	h1 { font-size: 22px; font-weight: 700; color: var(--text); margin: 0 0 4px; }

	.status-msg {
		text-align: center;
		padding: 40px 20px;
		color: var(--text-dim);
	}

	.empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 16px;
		padding: 40px 20px;
		text-align: center;
	}
	.empty-text { color: var(--text-dim); font-size: 14px; margin: 0; }

	.btn-primary {
		background: var(--accent);
		color: var(--bg);
		border: none;
		border-radius: 8px;
		padding: 12px 24px;
		font-size: 14px;
		font-weight: 600;
		cursor: pointer;
		text-decoration: none;
		transition: opacity 0.15s;
	}
	.btn-primary:hover { opacity: 0.85; }

	.purchase-list {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.purchase-card {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.purchase-card-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.phase-label {
		font-size: 14px;
		font-weight: 600;
		color: var(--text);
	}
	.phase-label.muted { color: var(--text-dim); font-weight: 400; }

	.city-label {
		font-size: 12px;
		color: var(--text-faint);
		font-weight: 500;
	}

	.nickname-row {
		display: flex;
		gap: 4px;
		font-size: 12px;
		align-items: baseline;
	}
	.nickname-label { color: var(--text-faint); font-weight: 600; }
	.nickname-value { color: var(--text-dim); font-style: italic; }

	.expiry-row {
		font-size: 11px;
		color: var(--text-faint);
	}

	.btn-action {
		display: inline-block;
		background: var(--accent);
		color: var(--bg);
		border-radius: 8px;
		padding: 10px 20px;
		font-size: 13px;
		font-weight: 600;
		text-decoration: none;
		transition: opacity 0.15s;
		text-align: center;
		align-self: flex-start;
	}
	.btn-action:hover { opacity: 0.85; }

	.listing-link-row {
		margin-top: 4px;
	}
	.listing-link {
		font-size: 12px;
		color: var(--text-dim);
		text-decoration: none;
	}
	.listing-link:hover { color: var(--text); }
</style>
