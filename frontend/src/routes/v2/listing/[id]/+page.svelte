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

	// Generate or load purchase_token for this listing
	onMount(() => {
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
				helperError = data.error || `HTTP ${res.status}`;
				return;
			}
			// Save purchase_id so helper/purchase page can restore
			try {
				sessionStorage.setItem(`v2_purchase_${purchaseToken}`, JSON.stringify({
					purchaseId: data.purchase_id,
					purchaseToken: purchaseToken,
					listingId: id,
					walletAddress: helperWallet.trim(),
				}));
			} catch {}
			try { sessionStorage.setItem('v2_active_purchase_token', purchaseToken); } catch {}
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

			<div class="notice-box">
				<div class="notice-title">{t('v2.helper.notice_title')}</div>
				<ul class="notice-list">
					<li>{t('v2.helper.notice_country')}</li>
					<li>{t('v2.helper.notice_wallet')}</li>
					<li>{t('v2.helper.notice_balance')}</li>
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
				<p class="hint">{t('v2.listing.helper_wallet_hint')}</p>
			</div>

			{#if helperError}
				<div class="err">{helperError}</div>
			{/if}

			<button
				class="btn-primary"
				onclick={startHelperPurchase}
				disabled={helperLoading || !helperWallet.trim()}
			>
				{helperLoading ? t('v2.loading') : t('v2.listing.helper_btn')}
			</button>

			<p class="fine-print">{t('v2.listing.helper_fine_print')}</p>
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
</style>
