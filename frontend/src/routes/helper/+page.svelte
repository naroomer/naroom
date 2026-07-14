<script>
	import { lang, t as tFn } from '$lib/i18n.js';
	import { CITIES } from '$lib/cities.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	const DEP_VALUES    = ['alcohol','opioids','stimulants','cannabis','cocaine','mephedrone','benzodiazepines','polysubstance','gambling'];
	const HELP_VALUES   = ['crisis','relapse_prevention','motivation','just_talk','recovery_plan'];
	const URGENCY_VALUES = ['urgent','soon','can_wait'];

	const CITY_OPTIONS = CITIES.map(c => ({ value: c.id, label: c.label }));

	const LANGUAGE_OPTIONS = [
		{ value: 'en', label: 'EN' },
		{ value: 'ru', label: 'RU' },
		{ value: 'ka', label: 'ქარ' },
		{ value: 'es', label: 'ES' },
	];

	let DEP_OPTIONS     = $derived(DEP_VALUES.map(v => ({ value: v, label: t('dep.' + v) })));
	let HELP_OPTIONS    = $derived(HELP_VALUES.map(v => ({ value: v, label: t('help.' + v) })));
	let URGENCY_OPTIONS = $derived(URGENCY_VALUES.map(v => ({ value: v, label: t('urgency.' + v) })));

	// Form state
	let walletAddress = $state('');
	let currency      = $state('BTC');
	let city          = $state('');
	let language      = $state('');
	let problem       = $state('');
	let helpType      = $state('');
	let urgency       = $state('');

	// UI state
	let step           = $state(1); // 1=form 2=telegram 3=done
	let loading        = $state(false);
	let error          = $state('');
	let telegramBotUrl = $state('');
	let telegramToken  = $state('');
	// Recovery code display (shown once after /session/init)
	let recoveryCodeNew  = $state('');
	let showRecoveryStep = $state(false);
	let pendingAfterRecovery = $state(null);

	// Step 1 → ensure principal session → show recovery code → check balance → get Telegram token → go to step 2
	async function handleSubscribe() {
		if (!walletAddress) return;
		loading = true;
		error = '';
		try {
			let sessionToken = sessionStorage.getItem('naroom_session_peer') || '';

			// Validate existing session via /session/status
			if (sessionToken) {
				try {
					const testRes = await fetch('/api/session/status', { headers: { 'Authorization': `Bearer ${sessionToken}` } });
					if (!testRes.ok) { sessionToken = ''; }
				} catch { sessionToken = ''; }
			}

			if (!sessionToken) {
				// New session — show recovery code BEFORE calling /wallet/register
				const initRes = await fetch('/api/session/init', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ role: 'peer' }),
				});
				if (!initRes.ok) throw new Error('Failed to initialize session');
				const initData = await initRes.json();
				sessionToken = initData.session_token;
				sessionStorage.setItem('naroom_session_peer', sessionToken);

				recoveryCodeNew = initData.recovery_code;
				pendingAfterRecovery = async () => {
					showRecoveryStep = false;
					await registerPeerWalletAndContinue(sessionToken);
				};
				showRecoveryStep = true;
				loading = false;
				return;
			}

			// Existing valid session — go straight to wallet registration
			await registerPeerWalletAndContinue(sessionToken);
		} catch (e) {
			error = e.message ?? 'Error';
		} finally {
			loading = false;
		}
	}

	async function registerPeerWalletAndContinue(sessionToken) {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/wallet/register', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${sessionToken}`,
				},
				body: JSON.stringify({ wallet_address: walletAddress, currency, role: 'peer' }),
			});
			const data = await res.json();
			if (!res.ok) {
				if (res.status === 402) {
					throw new Error(t('helper.low_balance', { balance: Math.round(data.balance_usd ?? 0), required: data.required_usd ?? 1000 }));
				}
				throw new Error(data.error ?? 'Failed to verify balance');
			}
			await continueHelperSubscribe(sessionToken);
		} catch (e) {
			error = e.message ?? 'Error';
		} finally {
			loading = false;
		}
	}

	async function continueHelperSubscribe(sessionToken) {
		loading = true;
		try {
			// Get Telegram token
			const filters = {};
			if (city)     filters.city = city;
			if (language) filters.language = language;
			if (problem)  filters.problem = problem;
			if (helpType) filters.help_type = helpType;
			if (urgency)  filters.urgency = urgency;

			const tgRes = await fetch('/api/telegram/helper/token', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${sessionToken}`,
				},
				body: JSON.stringify(filters),
			});
			if (!tgRes.ok) {
				const e = await tgRes.json();
				throw new Error(e.error ?? 'Failed to connect Telegram');
			}
			const tgData = await tgRes.json();
			telegramBotUrl = tgData.bot_url;
			telegramToken  = tgData.token;
			step = 2;
		} catch (e) {
			error = e.message ?? 'Error';
		} finally {
			loading = false;
		}
	}

	// Poll for Telegram confirmation (step 3) — 5 min timeout
	$effect(() => {
		if (step !== 2 || !telegramToken) return;
		const tok = telegramToken;
		const deadline = Date.now() + 5 * 60 * 1000;
		const timer = setInterval(async () => {
			if (Date.now() > deadline) {
				clearInterval(timer);
				error = t('helper.tg_timeout');
				step = 1;
				return;
			}
			try {
				const res = await fetch(`/api/telegram/helper/confirm?token=${tok}`);
				if (!res.ok) return;
				const data = await res.json();
				if (data.confirmed) {
					clearInterval(timer);
					step = 3;
				}
			} catch {}
		}, 3000);
		return () => clearInterval(timer);
	});
</script>

<svelte:head>
	<meta name="robots" content="noindex, nofollow" />
</svelte:head>

<div class="page">
	<header>
		<a class="back" href="/board/tbilisi">{t('helper.back')}</a>
	</header>

	<!-- Step 1: Form -->
	{#if step === 1 && !showRecoveryStep}
	<div class="form-wrap">
		<div>
			<h1>{t('helper.title')}</h1>
			<p class="subtitle">{t('helper.subtitle')}</p>
		</div>

		<!-- Wallet -->
		<div class="field">
			<label class="label">{t('helper.wallet_label', { currency })}</label>
			<div class="currency-row">
				<button
					class="cur-btn"
					class:active={currency === 'BTC'}
					onclick={() => { currency = 'BTC'; walletAddress = ''; }}
					type="button"
				>BTC</button>
				<button
					class="cur-btn"
					class:active={currency === 'LTC'}
					onclick={() => { currency = 'LTC'; walletAddress = ''; }}
					type="button"
				>LTC</button>
			</div>
			<input
				type="text"
				class="input"
				placeholder={currency === 'BTC' ? '1A1zP1...' : 'LYzU9...'}
				bind:value={walletAddress}
			/>
			<p class="hint">{t('helper.wallet_hint')}</p>
		</div>

		<!-- Filters -->
		<div class="filters-section">
			<p class="filters-label">{t('helper.filters')}</p>
			<p class="hint">{t('helper.filters_hint')}</p>

			<div class="filters-grid">
				<div class="filter-field">
					<label class="label">{t('helper.city')}</label>
					<select class="select" bind:value={city}>
						<option value="">{t('helper.any')}</option>
						{#each CITY_OPTIONS as opt}
							<option value={opt.value}>{opt.label}</option>
						{/each}
					</select>
				</div>

				<div class="filter-field">
					<label class="label">{t('helper.language')}</label>
					<select class="select" bind:value={language}>
						<option value="">{t('helper.any')}</option>
						{#each LANGUAGE_OPTIONS as opt}
							<option value={opt.value}>{opt.label}</option>
						{/each}
					</select>
				</div>

				<div class="filter-field">
					<label class="label">{t('helper.problem')}</label>
					<select class="select" bind:value={problem}>
						<option value="">{t('helper.any')}</option>
						{#each DEP_OPTIONS as opt}
							<option value={opt.value}>{opt.label}</option>
						{/each}
					</select>
				</div>

				<div class="filter-field">
					<label class="label">{t('helper.help_type')}</label>
					<select class="select" bind:value={helpType}>
						<option value="">{t('helper.any')}</option>
						{#each HELP_OPTIONS as opt}
							<option value={opt.value}>{opt.label}</option>
						{/each}
					</select>
				</div>

				<div class="filter-field">
					<label class="label">{t('helper.urgency')}</label>
					<select class="select" bind:value={urgency}>
						<option value="">{t('helper.any')}</option>
						{#each URGENCY_OPTIONS as opt}
							<option value={opt.value}>{opt.label}</option>
						{/each}
					</select>
				</div>
			</div>
		</div>

		{#if error}
			<div class="error">{error}</div>
		{/if}

		<button
			class="submit"
			disabled={!walletAddress || loading}
			onclick={handleSubscribe}
			type="button"
		>
			{loading ? t('helper.processing') : t('helper.subscribe')}
		</button>
	</div>

	<!-- Recovery code display (shown once after /session/init) -->
	{:else if showRecoveryStep}
	<div class="form-wrap">
		<h2>Save your recovery code</h2>
		<p>This is the only way to restore access if you lose your session. It will not be shown again.</p>
		<div class="message-box" style="font-size: 12px;">{recoveryCodeNew}</div>
		<button class="submit" onclick={() => pendingAfterRecovery && pendingAfterRecovery()}>
			I saved it — continue →
		</button>
	</div>

	<!-- Step 2: Telegram linking -->
	{:else if step === 2}
	<div class="tg-wrap">
		<div class="tg-icon">
			<svg width="48" height="48" viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg">
				<circle cx="24" cy="24" r="24" fill="#29A9EB"/>
				<path d="M10.7 23.3l26.4-10.2c1.2-.4 2.2.3 1.8 1.9l-4.5 21.2c-.3 1.4-1.2 1.7-2.4 1.1l-6.6-4.9-3.2 3.1c-.4.4-.7.7-1.4.7l.5-6.8 12.6-11.4c.5-.5-.1-.7-.8-.2L15.3 27.4l-6.5-2c-1.4-.4-1.4-1.4.9-2.1z" fill="white"/>
			</svg>
		</div>
		<h2>{t('helper.tg_title')}</h2>
		<p>{t('helper.tg_body')}</p>

		{#if telegramBotUrl}
			<a class="submit tg-btn" href={telegramBotUrl} target="_blank" rel="noopener noreferrer">
				{t('helper.tg_open_bot')}
			</a>
		{/if}

		<div class="status-row">
			<span class="dot"></span>
			{t('helper.tg_waiting')}
		</div>
	</div>

	<!-- Step 3: Done -->
	{:else if step === 3}
	<div class="tg-wrap">
		<div class="done-icon">✓</div>
		<h2>{t('helper.tg_done')}</h2>
		<p>{t('helper.tg_done_body')}</p>
		<a class="submit" href="/board/tbilisi">{t('helper.back')}</a>
	</div>
	{/if}
</div>

<style>
	.page {
		max-width: 560px;
		margin: 0 auto;
		padding: 0 16px 60px;
	}

	header {
		padding: 20px 0 24px;
	}

	.back {
		color: var(--text-dim);
		font-size: 13px;
	}

	.back:hover { color: var(--text); }

	.form-wrap, .tg-wrap {
		display: flex;
		flex-direction: column;
		gap: 28px;
	}

	h1 {
		font-size: 24px;
		font-weight: 600;
		color: var(--text);
	}

	h2 {
		font-size: 20px;
		font-weight: 600;
		color: var(--text);
	}

	.subtitle {
		color: var(--text-dim);
		font-size: 13px;
		margin-top: 4px;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.label {
		font-size: 13px;
		color: var(--text-dim);
		font-weight: 500;
	}

	.hint {
		font-size: 12px;
		color: var(--text-dim);
		margin: 0;
	}

	.currency-row {
		display: flex;
		gap: 8px;
	}

	.cur-btn {
		padding: 6px 16px;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: transparent;
		color: var(--text-dim);
		font-size: 13px;
		cursor: pointer;
		transition: all 0.15s;
	}

	.cur-btn.active {
		border-color: var(--accent);
		color: var(--accent);
		background: color-mix(in srgb, var(--accent) 10%, transparent);
	}

	.input {
		width: 100%;
		padding: 12px 14px;
		border-radius: 10px;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
		font-size: 14px;
		font-family: monospace;
		box-sizing: border-box;
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.sig-input {
		resize: vertical;
		font-size: 13px;
	}

	.message-box {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 14px;
		font-family: monospace;
		font-size: 12px;
		color: var(--text-dim);
		white-space: pre-wrap;
		word-break: break-all;
		line-height: 1.6;
	}

	.copy-btn {
		align-self: flex-start;
		padding: 6px 16px;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: transparent;
		color: var(--accent);
		font-size: 13px;
		cursor: pointer;
		transition: all 0.15s;
	}

	.copy-btn:hover {
		background: color-mix(in srgb, var(--accent) 10%, transparent);
	}

	.connect-box {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.connect-btn {
		width: 100%;
		padding: 13px;
		border-radius: 12px;
		border: 1.5px solid var(--accent);
		background: transparent;
		color: var(--accent);
		font-size: 15px;
		font-weight: 600;
		cursor: pointer;
		transition: all 0.15s;
	}

	.connect-btn:hover:not(:disabled) {
		background: color-mix(in srgb, var(--accent) 10%, transparent);
	}

	.connect-btn:disabled { opacity: 0.4; cursor: not-allowed; }

	.connect-hint {
		font-size: 11px;
		color: var(--text-dim);
		text-align: center;
	}

	.divider {
		display: flex;
		align-items: center;
		gap: 12px;
		color: var(--text-dim);
		font-size: 12px;
	}

	.divider::before, .divider::after {
		content: '';
		flex: 1;
		height: 1px;
		background: var(--border);
	}

	.how-box {
		background: color-mix(in srgb, var(--accent) 6%, transparent);
		border: 1px solid color-mix(in srgb, var(--accent) 20%, transparent);
		border-radius: 10px;
		padding: 14px 16px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.how-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}

	.link-btn {
		background: none;
		border: none;
		color: var(--text-dim);
		font-size: 13px;
		cursor: pointer;
		padding: 0;
		text-align: left;
	}

	.link-btn:hover { color: var(--text); }

	.filters-section {
		display: flex;
		flex-direction: column;
		gap: 16px;
	}

	.filters-label {
		font-size: 14px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}

	.filters-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 12px;
	}

	.filter-field {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.select {
		padding: 10px 12px;
		border-radius: 10px;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
		font-size: 14px;
		cursor: pointer;
	}

	.select:focus {
		outline: none;
		border-color: var(--accent);
	}

	.error {
		color: var(--urgent);
		font-size: 13px;
		padding: 10px 14px;
		background: color-mix(in srgb, var(--urgent) 10%, transparent);
		border-radius: 8px;
	}

	.submit {
		display: block;
		width: 100%;
		padding: 14px;
		border-radius: 12px;
		background: var(--accent);
		color: #fff;
		font-size: 15px;
		font-weight: 600;
		text-align: center;
		border: none;
		cursor: pointer;
		text-decoration: none;
		transition: opacity 0.15s;
	}

	.submit:disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.submit:hover:not(:disabled) { opacity: 0.88; }

	.tg-btn {
		background: #29A9EB;
	}

	.tg-icon {
		display: flex;
		justify-content: center;
		padding-top: 24px;
	}

	.done-icon {
		display: flex;
		justify-content: center;
		align-items: center;
		width: 48px;
		height: 48px;
		border-radius: 50%;
		background: var(--accent);
		color: #fff;
		font-size: 24px;
		font-weight: 700;
		margin: 24px auto 0;
	}

	.status-row {
		display: flex;
		align-items: center;
		gap: 10px;
		color: var(--text-dim);
		font-size: 13px;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--accent);
		animation: pulse 1.4s ease-in-out infinite;
		flex-shrink: 0;
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; transform: scale(1); }
		50%       { opacity: 0.4; transform: scale(0.85); }
	}
</style>
