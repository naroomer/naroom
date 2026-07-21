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
				error = data.error || `HTTP ${res.status}`;
				return;
			}

			// Save to sessionStorage so /v2/new can auto-restore
			try {
				sessionStorage.setItem('v2_client_state', JSON.stringify({
					managementCode: managementCode.trim(),
					walletAddress: walletAddress.trim(),
					flowId: data.listing?.flow_id || '',
				}));
			} catch {}

			// Navigate to /v2/new which reads session and routes by phase
			window.location.href = '/v2/new';
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
