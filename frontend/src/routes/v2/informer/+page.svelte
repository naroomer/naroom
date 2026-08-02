<script>
	import { onMount, onDestroy } from 'svelte';
	import { lang, t as tFn } from '$lib/i18n.js';
	import { LAUNCH_CURRENCY, LAUNCH_DISPLAY_LIMITS, detectLaunchCurrency } from '$lib/v2LaunchPolicy.js';
	// Cities loaded from API — no static registry in V2 frontend.
	let citiesData = $state([]);

	// Resolve city ID → display label for the success screen.
	let cityLabel = $derived(citiesData.find(c => c.id === city)?.label ?? city);

	let t = $derived((key, params) => tFn($lang, key, params));

	// ── State ──────────────────────────────────────────────────────────────────
	let step = $state('wallet');   // wallet | waiting | connected | error
	let loading = $state(false);
	let error = $state('');
	let eligibilityVerified = $state(false);  // true after balance check passes

	let walletAddress = $state('');
	let city = $state('tbilisi');

	// Returned from /access
	let botUrl = $state('');
	let rawToken = $state('');
	let expiresAt = $state('');

	// Poll timer
	let pollTimer = null;

	// ── Currency detection ─────────────────────────────────────────────────────
	const detectCurrency = detectLaunchCurrency;

	let detectedCurrency = $derived(detectCurrency(walletAddress) || LAUNCH_CURRENCY);

	// ── Error code → i18n key ──────────────────────────────────────────────────
	const ERROR_CODE_KEY = {
		low_balance:    'v2.inf.low_balance',
		provider_error: 'v2.inf.provider_error',
		invalid_address:'v2.inf.invalid_address',
		invalid_city:   'v2.inf.invalid_city',
		token_expired:  'v2.inf.token_expired',
	};

	// ── Step 1: Call /access ───────────────────────────────────────────────────
	async function checkAccess() {
		if (detectCurrency(walletAddress) !== LAUNCH_CURRENCY || !city) return;
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/v2/informer/access', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					wallet_address: walletAddress.trim(),
					city,
				}),
			});
			const data = await res.json();
			if (!res.ok) {
				const key = ERROR_CODE_KEY[data.code] ?? '';
				error = key ? t(key, { min: '$' + LAUNCH_DISPLAY_LIMITS.informerMinUSD }) : (data.error || `HTTP ${res.status}`);
				return;
			}
			botUrl    = data.bot_url;
			rawToken  = data.raw_token;
			expiresAt = data.expires_at;
			eligibilityVerified = true;
			step = 'waiting';
			startPoll();
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	// ── Step 2: Poll /status ───────────────────────────────────────────────────
	function startPoll() {
		stopPoll();
		pollTimer = setInterval(pollStatus, 3000);
	}

	function stopPoll() {
		if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
	}

	async function pollStatus() {
		if (!rawToken) return;
		try {
			const res = await fetch('/api/v2/informer/status', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ raw_token: rawToken }),
			});
			if (res.status === 410) {
				// token_expired
				stopPoll();
				error = t('v2.inf.token_expired');
				step = 'wallet';
				return;
			}
			if (!res.ok) return;
			const data = await res.json();
			if (data.state === 'claimed') {
				stopPoll();
				step = 'connected';
			}
		} catch {}
	}

	// ── Open bot ───────────────────────────────────────────────────────────────
	function openBot() {
		if (botUrl) window.open(botUrl, '_blank', 'noopener');
	}

	async function fetchCities() {
		try {
			const r = await fetch('/api/v2/board/cities');
			if (r.ok) citiesData = await r.json();
		} catch {}
	}

	onMount(fetchCities);

	onDestroy(() => {
		stopPoll();
	});
</script>

<div class="page">
	<a href="/v2/board/{city || 'tbilisi'}" class="back">← {t('back_to_board')}</a>

	{#if step === 'wallet'}
		<!-- Step 1: Enter wallet + city -->
		<div class="section">
			<h1>{t('v2.inf.title')}</h1>
			<p class="sub">{t('v2.inf.subtitle')}</p>

			<div class="field">
				<label>{t('v2.inf.wallet_label')}</label>
				<input
					bind:value={walletAddress}
					placeholder={t('v2.inf.wallet_placeholder')}
					type="text"
					autocomplete="off"
					spellcheck="false"
					class:detected={!!detectCurrency(walletAddress)}
				/>
				{#if detectCurrency(walletAddress)}
					<div class="currency-tag">{detectedCurrency}</div>
				{/if}
				<p class="fine-print">{t('v2.inf.wallet_hint', { min: '$' + LAUNCH_DISPLAY_LIMITS.informerMinUSD })}</p>
			</div>

			<div class="field">
				<label>{t('v2.inf.city_label')}</label>
				<select bind:value={city}>
					{#each citiesData as c}
						<option value={c.id}>{c.label}</option>
					{/each}
				</select>
			</div>

			{#if error}
				<div class="err">{error}</div>
			{/if}

			<button
				class="btn-primary"
				onclick={checkAccess}
				disabled={loading || detectCurrency(walletAddress) !== LAUNCH_CURRENCY}
			>
				{loading ? t('v2.inf.checking') : t('v2.inf.check_btn')}
			</button>

			<p class="fine-print">{t('v2.inf.privacy_note')}</p>
		</div>

	{:else if step === 'waiting'}
		<!-- Step 2: Waiting for /start in bot -->
		<div class="section">
			{#if eligibilityVerified}
				<div class="eligibility-badge" data-testid="eligibility-badge">
					{t('v2.inf.eligibility_verified', { min: LAUNCH_DISPLAY_LIMITS.informerMinUSD })}
				</div>
			{/if}

			<div class="status-badge waiting">{t('v2.inf.waiting')}</div>

			<button class="tg-btn" onclick={openBot}>
				{t('v2.inf.open_bot')}
			</button>

			{#if error}
				<div class="err">{error}</div>
			{/if}

			<button class="btn-secondary" onclick={() => { stopPoll(); step = 'wallet'; error = ''; eligibilityVerified = false; }}>
				← {t('back_to_board')}
			</button>
		</div>

	{:else if step === 'connected'}
		<!-- Step 3: Connected -->
		<div class="section done">
			<div class="done-icon">✓</div>
			{#if eligibilityVerified}
				<p class="eligibility-confirm" data-testid="eligibility-confirm">
					{t('v2.inf.eligibility_verified', { min: LAUNCH_DISPLAY_LIMITS.informerMinUSD })}
				</p>
			{/if}
			<h2 data-testid="connected-msg">{t('v2.inf.connected', { city: cityLabel })}</h2>
			<a href="/v2/board/{city}" class="btn-secondary">{t('back_to_board')}</a>
		</div>
	{/if}
</div>

<style>
	.page {
		max-width: 480px;
		margin: 0 auto;
		padding: 0 16px 60px;
	}

	.back {
		display: inline-block;
		margin: 20px 0 24px;
		color: var(--text-dim);
		font-size: 13px;
	}
	.back:hover { color: var(--text); }

	.section { display: flex; flex-direction: column; gap: 16px; }

	h1 { font-size: 22px; font-weight: 700; color: var(--text); }
	h2 { font-size: 18px; font-weight: 700; color: var(--text); }
	.sub { color: var(--text-dim); font-size: 14px; line-height: 1.5; }

	.field { display: flex; flex-direction: column; gap: 6px; }
	.field label {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-dim);
		text-transform: uppercase;
		letter-spacing: 0.5px;
	}

	input, select {
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
	input:focus, select:focus { border-color: var(--accent); }
	input.detected { border-color: var(--accent); }

	.currency-tag {
		font-size: 11px;
		color: var(--accent);
		font-weight: 600;
		letter-spacing: 0.5px;
		text-transform: uppercase;
	}

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

	.tg-btn {
		display: inline-block;
		background: #2B9FD6;
		color: #fff;
		border: none;
		border-radius: 8px;
		padding: 12px 24px;
		font-size: 14px;
		font-weight: 600;
		cursor: pointer;
		text-align: center;
		transition: opacity 0.15s;
	}
	.tg-btn:hover { opacity: 0.85; }

	.status-badge {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 12px 16px;
		font-size: 13px;
		color: var(--text-dim);
	}
	.status-badge.waiting {
		animation: pulse 2s infinite;
	}
	@keyframes pulse { 0%,100% { opacity:1 } 50% { opacity:0.5 } }

	.err { color: var(--danger); font-size: 13px; }
	.fine-print { font-size: 12px; color: var(--text-faint); line-height: 1.4; }

	.eligibility-badge {
		background: rgba(123, 166, 142, 0.1);
		border: 1px solid var(--accent);
		border-radius: 8px;
		padding: 10px 14px;
		font-size: 13px;
		color: var(--accent);
		line-height: 1.4;
	}

	.eligibility-confirm {
		font-size: 13px;
		color: var(--accent);
		margin: 0;
		line-height: 1.4;
	}

	.done {
		align-items: center;
		text-align: center;
		padding-top: 40px;
	}
	.done-icon {
		width: 56px; height: 56px;
		background: rgba(123, 166, 142, 0.15);
		border: 2px solid var(--accent);
		border-radius: 50%;
		display: flex; align-items: center; justify-content: center;
		font-size: 24px; color: var(--accent);
	}
</style>
