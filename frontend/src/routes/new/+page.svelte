<script>
	import { goto } from '$app/navigation';
	import { untrack } from 'svelte';
	import { lang, t as tFn } from '$lib/i18n.js';
	import { CITIES } from '$lib/cities.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	const DEP_VALUES    = ['alcohol','opioids','stimulants','cannabis','cocaine','mephedrone','benzodiazepines','polysubstance','gambling'];
	const HELP_VALUES   = ['crisis','relapse_prevention','motivation','just_talk','recovery_plan'];
	const URGENCY_VALUES = [
		{ value: 'urgent',   color: 'var(--urgent)' },
		{ value: 'soon',     color: 'var(--warn)' },
		{ value: 'can_wait', color: 'var(--can-wait)' },
	];

	let DEPENDENCY_OPTIONS = $derived(DEP_VALUES.map(v => ({ value: v, label: t('dep.' + v) })));
	let HELP_OPTIONS       = $derived(HELP_VALUES.map(v => ({ value: v, label: t('help.' + v) })));
	let URGENCY_OPTIONS    = $derived(URGENCY_VALUES.map(o => ({ ...o, label: t('urgency.' + o.value) })));

	const LANGUAGE_OPTIONS = [
		{ value: 'en', label: 'EN' },
		{ value: 'ru', label: 'RU' },
		{ value: 'ka', label: 'ქარ' },
		{ value: 'es', label: 'ES' },
	];

	// Languages available per city (empty = all)
	const CITY_LANGS = {
		tbilisi:      ['en', 'ru', 'ka', 'es'],
		batumi:       ['en', 'ru', 'ka', 'es'],
		buenos_aires: ['en', 'es'],
		sao_paulo:    ['en', 'es'],
		almaty:       ['en', 'ru', 'es'],
		yerevan:      ['en', 'ru', 'es'],
		moscow:       ['ru', 'en', 'es'],
		nha_trang:    ['en', 'es'],
		da_nang:      ['en', 'es'],
	};

	const CITY_OPTIONS = CITIES.map(c => ({ value: c.id, label: c.label }));

	// Form state
	let city           = $state('tbilisi');
	let dependency     = $state('');
	let helpType       = $state('');
	let urgency        = $state('');
	let languages      = $state([]);
	let walletAddress  = $state('');
	let currency       = $state('BTC');

	// UI state
	let step           = $state(1); // 1=form 2=crisis 3=invoice 4=telegram
	let loading        = $state(false);
	let error          = $state('');
	let balanceLow     = $state(false); // true when 402 — show retry button
	let invoice        = $state(null);
	let telegramBotUrl = $state('');
	let telegramError  = $state('');

	// Languages available for currently selected city
	let availableLangs = $derived(
		LANGUAGE_OPTIONS.filter(o => !CITY_LANGS[city] || CITY_LANGS[city].includes(o.value))
	);

	function toggleLang(val) {
		if (languages.includes(val)) {
			languages = languages.filter(l => l !== val);
		} else {
			languages = [...languages, val];
		}
	}

	// When city changes, drop any selected language not available there
	$effect(() => {
		const allowed = CITY_LANGS[city]; // tracks city only
		if (allowed) {
			const current = untrack(() => languages); // read without tracking
			languages = current.filter(l => allowed.includes(l));
		}
	});

	function canSubmit() {
		return city && dependency && helpType && urgency && languages.length > 0 && walletAddress;
	}

	async function handleSubmit() {
		if (!canSubmit()) return;

		// Crisis screen перед отправкой если urgent
		if (urgency === 'urgent' && step === 1) {
			step = 2;
			return;
		}

		await submitListing();
	}

	async function submitListing() {
		loading = true;
		error = '';
		balanceLow = false;

		try {
			// 1. Проверить баланс → получить session token
			const verifyRes = await fetch('/api/wallet/register', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					wallet_address: walletAddress,
					currency,
					role: 'client',
				}),
			});

			const verifyData = await verifyRes.json();

			if (verifyRes.status === 402) {
				balanceLow = true;
				throw new Error(t('new.balance_low', {
					balance: Math.round(verifyData.balance_usd ?? 0),
					required: verifyData.required_usd ?? 150,
				}));
			}
			if (!verifyRes.ok) {
				throw new Error(verifyData.error ?? 'Wallet verification failed');
			}

			const sessionToken = verifyData.session_token ?? '';
			if (sessionToken) sessionStorage.setItem('naroom_session_client', sessionToken);
			// Сохраняем адрес для повторных проверок (только в браузере)
			sessionStorage.setItem('naroom_wallet_client', walletAddress);
			sessionStorage.setItem('naroom_currency_client', currency);

			// 2. Создать объявление (wallet_address берётся из сессии на сервере)
			const listRes = await fetch('/api/listing/create', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					...(sessionToken ? { 'Authorization': `Bearer ${sessionToken}` } : {}),
					'X-Dev-Wallet': walletAddress,
					'X-Dev-Role': 'client',
				},
				body: JSON.stringify({
					city,
					dependency_type: dependency,
					help_type: helpType,
					urgency,
					languages,
					currency,
				}),
			});

			if (!listRes.ok) {
				const e = await listRes.json();
				throw new Error(e.error ?? 'Failed to create listing');
			}

			invoice = await listRes.json();
			// Сохраняем wallet в sessionStorage для листинг-страницы
			sessionStorage.setItem('my_wallet_' + invoice.listing_id, walletAddress);
			sessionStorage.setItem('my_currency_' + invoice.listing_id, currency);
			step = 3;

		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	// Поллинг статуса invoice (Step 3)
	let pollTimer;
	$effect(() => {
		if (step !== 3 || !invoice) return;
		pollTimer = setInterval(async () => {
			try {
				const token = sessionStorage.getItem('naroom_session_client') ?? '';
				const res = await fetch(`/api/invoice/${invoice.invoice_id}/status`, {
					headers: token ? { 'Authorization': `Bearer ${token}` } : {},
				});
				if (!res.ok) return;
				const data = await res.json();
				if (data.status === 'confirmed') {
					clearInterval(pollTimer);
					const ok = await initTelegram();
					if (ok) step = 4;
				}
			} catch {}
		}, 3000);
		return () => clearInterval(pollTimer);
	});

	// Returns true if token was obtained successfully.
	async function initTelegram() {
		const token = sessionStorage.getItem('naroom_session_client') ?? '';
		try {
			const res = await fetch('/api/telegram/client/token', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					...(token ? { 'Authorization': `Bearer ${token}` } : {}),
					'X-Dev-Wallet': walletAddress,
					'X-Dev-Role': 'client',
				},
				body: JSON.stringify({ listing_id: invoice.listing_id }),
			});
			if (!res.ok) {
				const e = await res.json();
				telegramError = e.error ?? 'Failed to connect Telegram';
				return false;
			}
			const data = await res.json();
			telegramBotUrl = data.bot_url;
			return true;
		} catch (e) {
			telegramError = 'Connection error';
			return false;
		}
	}

	// Поллинг подтверждения Telegram (Step 4) — таймаут 5 минут
	$effect(() => {
		if (step !== 4 || !invoice) return;
		const deadline = Date.now() + 5 * 60 * 1000;
		const tgTimer = setInterval(async () => {
			if (Date.now() > deadline) {
				clearInterval(tgTimer);
				telegramError = 'Timeout — open the bot and press Start';
				return;
			}
			try {
				const res = await fetch(`/api/telegram/client/confirm?listing_id=${invoice.listing_id}`);
				if (!res.ok) return;
				const data = await res.json();
				if (data.confirmed) {
					clearInterval(tgTimer);
					goto(`/listing/${invoice.listing_id}`);
				}
			} catch {}
		}, 3000);
		return () => clearInterval(tgTimer);
	});
</script>

<div class="page">
	<header>
		<a href="/board/tbilisi" class="back">{t('back_to_board')}</a>
	</header>

	<!-- Step 1: Form -->
	{#if step === 1}
	<div class="form-wrap">
		<h1>{t('new.title')}</h1>
		<p class="subtitle">{t('new.subtitle')}</p>

		<!-- City -->
		<div class="field">
			<label>{t('new.city')}</label>
			<div class="options">
				{#each CITY_OPTIONS as opt}
					<button
						class="opt"
						class:selected={city === opt.value}
						onclick={() => city = opt.value}
					>{opt.label}</button>
				{/each}
			</div>
		</div>

		<!-- Dependency -->
		<div class="field">
			<label>{t('new.what_dealing')}</label>
			<div class="options">
				{#each DEPENDENCY_OPTIONS as opt}
					<button
						class="opt"
						class:selected={dependency === opt.value}
						onclick={() => dependency = opt.value}
					>{opt.label}</button>
				{/each}
			</div>
		</div>

		<!-- Help type -->
		<div class="field">
			<label>{t('new.what_help')}</label>
			<div class="options">
				{#each HELP_OPTIONS as opt}
					<button
						class="opt"
						class:selected={helpType === opt.value}
						onclick={() => helpType = opt.value}
					>{opt.label}</button>
				{/each}
			</div>
		</div>

		<!-- Urgency -->
		<div class="field">
			<label>{t('new.how_urgent')}</label>
			<div class="options">
				{#each URGENCY_OPTIONS as opt}
					<button
						class="opt urgency-opt"
						class:selected={urgency === opt.value}
						style="--uc: {opt.color}"
						onclick={() => urgency = opt.value}
					>{opt.label}</button>
				{/each}
			</div>
		</div>

		<!-- Languages -->
		<div class="field">
			<label>{t('new.languages')}</label>
			<div class="options">
				{#each availableLangs as opt}
					<button
						class="opt"
						class:selected={languages.includes(opt.value)}
						onclick={() => toggleLang(opt.value)}
					>{opt.label}</button>
				{/each}
			</div>
		</div>

		<!-- Wallet -->
		<div class="field">
			<label>{t('new.wallet_label', {currency})}</label>
			<p class="hint">{t('new.wallet_hint')}</p>
			<div class="wallet-row">
				<input
					type="text"
					placeholder={t('new.wallet_ph', {currency})}
					bind:value={walletAddress}
				/>
				<div class="currency-toggle">
					<button class:active={currency === 'BTC'} onclick={() => currency = 'BTC'}>BTC</button>
					<button class:active={currency === 'LTC'} onclick={() => currency = 'LTC'}>LTC</button>
				</div>
			</div>
		</div>

		<div class="balance-warning">
			{t('new.balance_warning')}
		</div>

		{#if error}
			<div class="error">
				{error}
				{#if balanceLow}
					<button class="retry-btn" onclick={submitListing} disabled={loading}>
						{t('new.check_again')}
					</button>
				{/if}
			</div>
		{/if}

		<button
			class="submit"
			disabled={!canSubmit() || loading}
			onclick={handleSubmit}
		>
			{loading ? t('new.processing') : t('new.post_listing')}
		</button>

		<p class="fine">{t('new.listing_fine', {currency})}</p>
	</div>

	<!-- Step 2: Crisis screen (urgent only) -->
	{:else if step === 2}
	<div class="crisis-wrap">
		<div class="crisis-icon">⚠</div>
		<h2>{t('new.crisis_title')}</h2>
		<p>{t('new.crisis_body')}</p>
		<p class="crisis-note">{t('new.crisis_note')}</p>
		<div class="crisis-actions">
			<button class="submit" onclick={submitListing} disabled={loading}>
				{loading ? t('new.processing') : t('new.crisis_confirm')}
			</button>
			<a href="/board/tbilisi" class="crisis-back">{t('new.go_back')}</a>
		</div>
		{#if error}
			<div class="error">{error}</div>
		{/if}
	</div>

	<!-- Step 3: Invoice / waiting for payment -->
	{:else if step === 3}
	<div class="invoice-wrap">
		<div class="invoice-icon">⏳</div>
		<h2>{t('new.send_payment')}</h2>
		<p>{@html t('new.send_exactly', {amount: `<strong>${invoice.amount_crypto}</strong>`, currency: invoice.currency})}</p>

		<div class="address-box">
			{invoice.address}
		</div>

		<p class="invoice-usd">{t('new.approx_usd')}</p>

		<div class="status-row">
			<span class="dot"></span>
			{t('waiting_confirmation')}
		</div>

		<p class="fine">{t('new.auto_check')}</p>
	</div>

	<!-- Step 4: Telegram connection -->
	{:else if step === 4}
	<div class="invoice-wrap">
		<div class="tg-icon">
			<svg width="48" height="48" viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg">
				<circle cx="24" cy="24" r="24" fill="#29A9EB"/>
				<path d="M10.7 23.3l26.4-10.2c1.2-.4 2.2.3 1.8 1.9l-4.5 21.2c-.3 1.4-1.2 1.7-2.4 1.1l-6.6-4.9-3.2 3.1c-.4.4-.7.7-1.4.7l.5-6.8 12.6-11.4c.5-.5-.1-.7-.8-.2L15.3 27.4l-6.5-2c-1.4-.4-1.4-1.4.9-2.1z" fill="white"/>
			</svg>
		</div>
		<h2>{t('new.tg_title')}</h2>
		<p>{t('new.tg_body')}</p>

		{#if telegramError}
			<div class="error">{telegramError}</div>
		{:else if telegramBotUrl}
			<a class="submit tg-btn" href={telegramBotUrl} target="_blank" rel="noopener noreferrer">
				{t('new.tg_open_bot')}
			</a>
		{/if}

		<div class="status-row">
			<span class="dot"></span>
			{t('new.tg_waiting')}
		</div>

		<p class="fine">
			<a class="tg-how" href="https://github.com/naroom/naroom" target="_blank" rel="noopener noreferrer">
				{t('new.tg_how')}
			</a>
		</p>
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

	/* Form */
	.form-wrap, .crisis-wrap, .invoice-wrap {
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
		margin-top: -20px;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	label {
		font-size: 13px;
		font-weight: 500;
		color: var(--text-dim);
		text-transform: uppercase;
		letter-spacing: 0.5px;
	}

	.hint {
		font-size: 12px;
		color: var(--text-faint);
		margin-top: -4px;
	}

	/* Option buttons */
	.options {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
	}

	.opt {
		padding: 7px 14px;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: var(--bg-card);
		color: var(--text-dim);
		font-size: 13px;
		transition: all 0.15s;
	}

	.opt:hover {
		border-color: var(--text-faint);
		color: var(--text);
	}

	.opt.selected {
		border-color: var(--accent);
		background: rgba(123, 166, 142, 0.1);
		color: var(--text);
	}

	.urgency-opt.selected {
		border-color: var(--uc);
		background: color-mix(in srgb, var(--uc) 12%, transparent);
		color: var(--uc);
	}

	/* Wallet input */
	.wallet-row {
		display: flex;
		gap: 8px;
	}

	input {
		flex: 1;
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 10px 14px;
		color: var(--text);
		font-size: 13px;
		font-family: monospace;
		outline: none;
		transition: border-color 0.15s;
	}

	input:focus { border-color: var(--accent); }
	input::placeholder { color: var(--text-faint); }

	.currency-toggle {
		display: flex;
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: hidden;
	}

	.currency-toggle button {
		padding: 0 14px;
		background: var(--bg-card);
		color: var(--text-dim);
		font-size: 12px;
		font-weight: 600;
		transition: all 0.15s;
	}

	.currency-toggle button.active {
		background: var(--accent);
		color: var(--bg);
	}

	/* Submit */
	.submit {
		padding: 13px 24px;
		background: var(--accent);
		color: var(--bg);
		border-radius: 10px;
		font-size: 15px;
		font-weight: 600;
		transition: opacity 0.15s;
		text-align: center;
	}

	.submit:disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.submit:not(:disabled):hover { opacity: 0.85; }

	.fine {
		font-size: 12px;
		color: var(--text-faint);
		text-align: center;
		margin-top: -16px;
	}

	.balance-warning {
		font-size: 12px;
		color: var(--text-dim);
		background: color-mix(in srgb, var(--warn, #c9a84c) 8%, transparent);
		border: 1px solid color-mix(in srgb, var(--warn, #c9a84c) 25%, transparent);
		border-radius: 8px;
		padding: 10px 14px;
		line-height: 1.5;
	}

	.error {
		background: rgba(212, 132, 90, 0.12);
		border: 1px solid var(--danger);
		border-radius: 8px;
		padding: 10px 14px;
		color: var(--danger);
		font-size: 13px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.retry-btn {
		align-self: flex-start;
		padding: 6px 14px;
		border-radius: 6px;
		border: 1px solid var(--danger);
		background: transparent;
		color: var(--danger);
		font-size: 12px;
		cursor: pointer;
	}

	.retry-btn:hover { background: rgba(212, 132, 90, 0.12); }

	/* Crisis screen */
	.crisis-icon {
		font-size: 36px;
		color: var(--danger);
	}

	.crisis-note {
		font-size: 13px;
		color: var(--text-dim);
	}

	.crisis-actions {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.crisis-back {
		text-align: center;
		color: var(--text-dim);
		font-size: 13px;
	}

	/* Invoice */
	.invoice-icon {
		font-size: 32px;
	}

	.address-box {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 14px 16px;
		font-family: monospace;
		font-size: 13px;
		word-break: break-all;
		color: var(--text);
	}

	.invoice-usd {
		color: var(--text-dim);
		font-size: 13px;
		margin-top: -20px;
	}

	.status-row {
		display: flex;
		align-items: center;
		gap: 8px;
		color: var(--text-dim);
		font-size: 13px;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--accent);
		animation: pulse 1.5s infinite;
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50%       { opacity: 0.3; }
	}

	/* Telegram step */
	.tg-icon {
		line-height: 0;
	}

	.tg-btn {
		display: block;
		text-decoration: none;
		text-align: center;
	}

	.tg-how {
		color: var(--accent);
		text-decoration: none;
	}

	.tg-how:hover {
		text-decoration: underline;
	}
</style>
