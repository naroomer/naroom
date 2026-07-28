<script>
	import { lang, t as tFn } from '$lib/i18n.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	let managementCode = $state('');
	let walletAddress = $state('');
	let loading = $state(false);
	let error = $state('');

	async function restore() {
		if (!managementCode.trim() || !walletAddress.trim()) return;
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/v2/client/listings/restore', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					management_code: managementCode.trim(),
					wallet_address: walletAddress.trim(),
				}),
			});
			const data = await res.json();
			if (!res.ok) {
				// Wrong wallet, wrong code, mixed pair, and unknown listing all
				// collapse to the same server code (errCodeNotFound) — show one
				// byte-identical localized message regardless of the reason,
				// so no enumeration is possible from the frontend either.
				error = data.code === 'not_found' ? t('v2.restore.not_found') : (data.error || `HTTP ${res.status}`);
				return;
			}

			const listingId = data.listing?.id || '';
			if (!listingId) {
				// Valid code+wallet, but no listing exists yet (payment/listing
				// creation never completed). This is out of scope for /v2/restore
				// — it owns the OWNER/reactivation journey only, never a $5
				// invoice or the create flow. No auto-navigation, no shared
				// storage key with /v2/new: just an honest message.
				error = t('v2.restore.no_listing_yet');
				return;
			}

			// Save the management code locally, keyed by listing ID, so the
			// listing page can offer server-authenticated owner mode. The
			// listing page independently re-verifies the code server-side.
			try {
				localStorage.setItem(`v2_mgmt_${listingId}`, JSON.stringify({
					code: managementCode.trim(),
					wallet: walletAddress.trim(),
				}));
			} catch {}

			// /v2/restore never touches /v2/new — the owner/reactivation journey
			// lives entirely on the listing page (owner mode).
			window.location.href = `/v2/listing/${listingId}`;
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}
</script>

<div class="page">
	<header>
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
	</header>

	<div class="section">
		<h1>{t('v2.restore.title')}</h1>
		<p class="sub">{t('v2.restore.sub')}</p>

		<div class="field">
			<label>{t('v2.restore.wallet_label')}</label>
			<input
				bind:value={walletAddress}
				placeholder={t('v2.restore.wallet_ph')}
				type="text"
				autocomplete="off"
				spellcheck="false"
			/>
		</div>

		<div class="field">
			<label>{t('v2.restore.code_label')}</label>
			<input
				bind:value={managementCode}
				placeholder={t('v2.restore.code_ph')}
				type="text"
				autocomplete="off"
				spellcheck="false"
				class="code-input"
			/>
			<p class="hint">{t('v2.restore.code_hint')}</p>
		</div>

		{#if error}
			<div class="err">{error}</div>
		{/if}

		<button
			class="btn-primary"
			onclick={restore}
			disabled={loading || !managementCode.trim() || !walletAddress.trim()}
		>
			{loading ? t('v2.loading') : t('v2.restore.btn')}
		</button>

		<a href="/v2/board/tbilisi" class="back-link">{t('v2.restore.back')}</a>
	</div>
</div>

<style>
	.page {
		max-width: 480px;
		margin: 0 auto;
		padding: 0 16px 60px;
	}

	header {
		padding: 20px 0 32px;
	}

	.logo {
		font-size: 18px;
		font-weight: 600;
		color: var(--text);
	}

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

	.section { display: flex; flex-direction: column; gap: 16px; }

	h1 { font-size: 22px; font-weight: 700; color: var(--text); margin: 0; }
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
	.code-input { font-family: monospace; letter-spacing: 0.5px; }

	.hint { font-size: 12px; color: var(--text-faint); line-height: 1.4; margin: 0; }
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

	.back-link {
		font-size: 13px;
		color: var(--text-dim);
		text-align: center;
	}
	.back-link:hover { color: var(--text); }
</style>
