<script>
	import { onMount, onDestroy } from 'svelte';
	import { lang, t as tFn } from '$lib/i18n.js';
	import { FALLBACK_CITY_ID } from '$lib/cities.js';
	import V2QR from '$lib/V2QR.svelte';

	// Cities come solely from the backend registry (/api/v2/board/cities), never
	// from a duplicated frontend array. FALLBACK_CITY_ID is the only static value
	// used, and only until the fetch completes (or if it fails).
	let cities = $state([{ id: FALLBACK_CITY_ID, label: FALLBACK_CITY_ID }]);

	async function fetchCities() {
		try {
			const r = await fetch('/api/v2/board/cities');
			if (r.ok) {
				const data = await r.json();
				if (Array.isArray(data) && data.length > 0) cities = data;
			}
		} catch {}
	}

	let t = $derived((key, params) => tFn($lang, key, params));

	// ── Public config ──────────────────────────────────────────────────────────────
	const DEFAULT_CONFIG = { client_public_min_usd: 150, client_hard_floor_usd: 120, helper_pre_invoice_min_usd: 1010, helper_post_payment_min_usd: 1000, informer_min_usd: 1000 };
	let pubConfig = $state({ ...DEFAULT_CONFIG });
	let configLoaded = $state(false);

	async function fetchPubConfig() {
		try {
			const r = await fetch('/api/v2/public-config');
			if (r.ok) pubConfig = { ...DEFAULT_CONFIG, ...(await r.json()) };
		} catch {}
		configLoaded = true;
	}

	// ── State ──────────────────────────────────────────────────────────────────
	let step = $state('wallet');      // wallet | invoice | code | balance | form | telegram | done
	let loading = $state(false);
	let error = $state('');
	let copyMsg = $state('');

	// Form fields
	let walletAddress = $state('');
	let city = $state(FALLBACK_CITY_ID);
	let depType = $state('');
	let helpType = $state('');
	let urgency = $state('');
	let languages = $state([]);
	let contactType = $state('telegram');
	let contactValue = $state('');

	// API responses
	let managementCode = $state('');
	let flowId = $state('');
	let invoiceId = $state('');
	let currency = $state('BTC');
	let invoice = $state(null);
	let balanceUSD = $state(null);
	let listingId = $state('');
	let telegramBotUrl = $state('');
	let telegramStatus = $state('needs_link');
	let displayName = $state('');
	let visibleUntil = $state(null);

	// Code acknowledgement gate
	let codeSaved = $state(false);

	// Restore tracking
	let wasRestored = $state(false);

	// Auto-transition timer
	let autoTransTimer = null;

	// Double-submit guard
	let submitting = $state(false);

	// ── Currency detection ─────────────────────────────────────────────────────
	function detectCurrency(addr) {
		const a = (addr || '').trim();
		if (!a) return null;
		if (/^ltc1/i.test(a) || /^[LM]/.test(a)) return 'LTC';
		if (/^bc1/i.test(a) || /^[13]/.test(a)) return 'BTC';
		return null;
	}

	let detectedCurrency = $derived(detectCurrency(walletAddress) || 'BTC');

	// ── Computed progress step ─────────────────────────────────────────────────
	let progressStep = $derived(
		step === 'done' ? 5 :
		step === 'telegram' ? 4 :
		step === 'form' ? 3 :
		step === 'balance' ? 2 : 1
	);

	// ── Restore on mount (idempotency) ─────────────────────────────────────────
	onMount(() => {
		fetchPubConfig();
		fetchCities();

		// Every explicit "create" link carries fresh=1. It means the user chose
		// to start a new listing, so an older completed/hidden flow must not
		// hijack that action. Remove the flag immediately: after a new intent is
		// created, an ordinary refresh must continue that same payment.
		const params = new URLSearchParams(window.location.search);
		if (params.get('fresh') === '1') {
			clearClientState();
			window.history.replaceState(window.history.state, '', window.location.pathname + window.location.hash);
			return;
		}

		const saved = sessionStorage.getItem('v2_client_state');
		if (saved) {
			try {
				const s = JSON.parse(saved);
				managementCode = s.managementCode || '';
				walletAddress = s.walletAddress || '';
				flowId = s.flowId || '';
				invoiceId = s.invoiceId || '';
				currency = s.currency || 'BTC';
				city = s.city || FALLBACK_CITY_ID;
				if (managementCode && walletAddress) {
					restoreFlow();
				}
			} catch {}
		}
	});

	onDestroy(() => {
		stopPoll();
		stopTgPoll();
		if (autoTransTimer) clearTimeout(autoTransTimer);
	});

	function saveState() {
		const s = { managementCode, walletAddress, flowId, invoiceId, currency, city };
		try { sessionStorage.setItem('v2_client_state', JSON.stringify(s)); } catch {}
	}

	// Clears the in-progress create-flow marker so a completed flow can never
	// intercept a fresh "+" click later in the same browser session.
	function clearClientState() {
		try { sessionStorage.removeItem('v2_client_state'); } catch {}
	}

	// Stores the management code + wallet locally, keyed by listing ID, so the
	// listing page can offer server-authenticated owner mode. The code is only
	// a convenience carrier for the frontend — the listing page's owner-view
	// endpoint independently re-verifies it server-side on every load; storage
	// is never itself an authorization basis.
	function saveOwnerCapability(lid) {
		if (!lid) return;
		try {
			localStorage.setItem(`v2_mgmt_${lid}`, JSON.stringify({
				code: managementCode,
				wallet: walletAddress,
			}));
		} catch {}
	}

	// ── Step: create payment intent ────────────────────────────────────────────
	async function createIntent() {
		if (!walletAddress.trim()) return;
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/v2/client/payment-intents', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ wallet_address: walletAddress.trim() }),
			});
			const data = await res.json();
			if (!res.ok) {
				if (data.code === 'wallet_already_visible') {
					error = t('new.wallet_already_visible');
				} else {
					error = data.error || `HTTP ${res.status}`;
				}
				return;
			}

			managementCode = data.management_code;
			flowId = data.flow_id;
			invoiceId = data.invoice_id;
			currency = data.currency;
			invoice = data.invoice;
			saveState();
			step = 'code';
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	// ── Step: restore flow ─────────────────────────────────────────────────────
	async function restoreFlow() {
		wasRestored = true;
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/v2/client/listings/restore', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: managementCode, wallet_address: walletAddress }),
			});
			const data = await res.json();
			if (!res.ok) { error = data.error || `HTTP ${res.status}`; return; }

			// Route by phase/next_action
			const phase = data.phase;
			const nextAction = data.next_action;

			if (phase === 'awaiting_payment' || phase === 'payment_detected') {
				// Also fetch invoice data
				const r2 = await fetch('/api/v2/client/payment-intents/restore', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ management_code: managementCode, wallet_address: walletAddress }),
				});
				const d2 = await r2.json();
				if (r2.ok) {
					invoiceId = d2.invoice_id;
					currency = d2.currency;
					invoice = d2.invoice;
				}
				step = 'invoice';
			} else if (phase === 'paid_low_balance') {
				step = 'balance';
			} else if (phase === 'form_ready') {
				if (nextAction === 'publish') {
					step = 'form';
				} else {
					// Needs telegram first
					step = 'telegram';
					telegramStatus = data.telegram_status || 'needs_link';
				}
			} else if (phase === 'visible' || phase === 'hidden') {
				// A listing already exists (visible or hidden). Create NEVER drives
				// reactivation or Telegram-for-reactivation itself — that whole
				// journey lives on the listing page (owner mode). Hand off there.
				const lid = data.listing?.id || '';
				clearClientState();
				if (lid) {
					saveOwnerCapability(lid);
					window.location.href = `/v2/listing/${lid}`;
					return;
				}
				error = t('v2.client.entitlement_expired');
			} else if (phase === 'finished' || phase === 'payment_expired') {
				// Terminal: this marker must not intercept the next "+" click.
				clearClientState();
				error = t('v2.client.entitlement_expired');
			}
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	// ── Invoice status polling ─────────────────────────────────────────────────
	let pollTimer = null;

	function startInvoicePoll() {
		stopPoll();
		pollTimer = setInterval(pollInvoiceStatus, 5000);
	}

	function stopPoll() {
		if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
	}

	$effect(() => {
		if (step === 'invoice') startInvoicePoll();
		else stopPoll();
	});

	async function pollInvoiceStatus() {
		if (!managementCode || !walletAddress) return;
		try {
			const res = await fetch('/api/v2/client/payment-intents/restore', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: managementCode, wallet_address: walletAddress }),
			});
			if (!res.ok) return;
			const data = await res.json();
			if (data.invoice) {
				invoice = data.invoice;
				invoiceId = data.invoice_id;
				currency = data.currency;
			}
			if (data.invoice?.status === 'confirmed') {
				stopPoll();
				// Check balance
				await recheckBalance();
			} else if (data.invoice?.status === 'expired') {
				stopPoll();
				error = t('v2.client.invoice_expired');
			}
		} catch {}
	}

	// ── Step: recheck balance ──────────────────────────────────────────────────
	async function recheckBalance() {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/v2/client/payment-intents/recheck-balance', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: managementCode, wallet_address: walletAddress }),
			});
			const data = await res.json();
			if (!res.ok) { error = data.error || `HTTP ${res.status}`; return; }
			balanceUSD = data.last_balance_usd;
			if (data.state === 'form_ready') {
				step = 'balance';
				autoTransTimer = setTimeout(() => { if (step === 'balance') step = 'telegram'; }, 2000);
			} else if (data.state === 'paid_low_balance') {
				step = 'balance';
			}
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	// ── Step: Telegram link ────────────────────────────────────────────────────
	let tgLinkUrl = $state('');
	let tgPollTimer = null;

	async function createTelegramLink() {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/v2/client/telegram-links', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: managementCode, wallet_address: walletAddress }),
			});
			const data = await res.json();
			if (!res.ok) {
				if (data.code === 'already_visible') {
					// Already linked — proceed
					telegramStatus = 'active';
					return;
				}
				error = data.error || `HTTP ${res.status}`;
				return;
			}
			tgLinkUrl = data.bot_url;
			window.open(tgLinkUrl, '_blank', 'noopener');
			startTgPoll();
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	function startTgPoll() {
		stopTgPoll();
		tgPollTimer = setInterval(pollTelegramStatus, 3000);
	}

	function stopTgPoll() {
		if (tgPollTimer) { clearInterval(tgPollTimer); tgPollTimer = null; }
	}

	async function pollTelegramStatus() {
		if (!managementCode || !walletAddress) return;
		try {
			const res = await fetch('/api/v2/client/telegram-links/status', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: managementCode, wallet_address: walletAddress }),
			});
			if (!res.ok) return;
			const data = await res.json();
			telegramStatus = data.status;
			if (data.status === 'ready' || data.status === 'active') {
				stopTgPoll();
				if (step === 'telegram') {
					step = 'form';
				}
			}
		} catch {}
	}

	// ── Step: publish listing ──────────────────────────────────────────────────
	function canPublish() {
		return city && depType && helpType && urgency && languages.length > 0 && contactValue.trim();
	}

	async function publishListing() {
		if (!canPublish()) return;
		if (submitting) return;
		submitting = true;
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/v2/client/listings/publish', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					management_code: managementCode,
					wallet_address: walletAddress,
					city,
					dependency_type: depType,
					help_type: helpType,
					urgency,
					languages,
					contact_type: contactType,
					contact: contactValue.trim(),
				}),
			});
			const data = await res.json();
			if (!res.ok) {
				if (res.status === 409) {
					// Already published — just navigate
					listingId = data.listing?.id || '';
					displayName = data.listing?.display_name || '';
					saveOwnerCapability(listingId);
					clearClientState();
					step = 'done';
					return;
				}
				error = data.error || `HTTP ${res.status}`;
				return;
			}
			listingId = data.id;
			displayName = data.display_name || '';
			visibleUntil = data.visible_until || null;
			saveOwnerCapability(listingId);
			clearClientState();
			step = 'done';
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
			submitting = false;
		}
	}

	// ── Copy helper ────────────────────────────────────────────────────────────
	async function copyText(text, label) {
		try {
			await navigator.clipboard.writeText(text);
			copyMsg = label || t('v2.copied');
			setTimeout(() => { copyMsg = ''; }, 2000);
		} catch {}
	}

	// ── Language toggle ────────────────────────────────────────────────────────
	function toggleLang(v) {
		if (languages.includes(v)) languages = languages.filter(l => l !== v);
		else languages = [...languages, v];
	}

	const DEP_VALUES  = ['alcohol','opioids','stimulants','cannabis','cocaine','mephedrone','benzodiazepines','polysubstance','gambling'];
	const HELP_VALUES = ['crisis','relapse_prevention','motivation','just_talk','recovery_plan'];
	const LANG_VALUES = ['en','ru','ka','es'];

	function paymentURI(inv, curr) {
		if (!inv?.payment_address) return '';
		const a = inv.amount_atomic;
		const addr = inv.payment_address;
		if (curr === 'BTC') return `bitcoin:${addr}?amount=${(a/1e8).toFixed(8)}`;
		if (curr === 'LTC') return `litecoin:${addr}?amount=${(a/1e8).toFixed(8)}`;
		return addr;
	}
</script>

<div class="page">
	<a href="/v2/board/{city || 'tbilisi'}" class="back">← {t('back_to_board')}</a>

	<!-- Progress indicator (hidden on wallet/code steps since user hasn't committed yet) -->
	{#if step !== 'wallet' && step !== 'code'}
	<div class="progress-bar" aria-label="Progress">
		{#each [1,2,3,4,5] as n}
			<div class="prog-step" class:active={progressStep === n} class:done={progressStep > n}>
				<div class="prog-dot"></div>
				<span class="prog-label">{t('v2.progress.step' + n)}</span>
			</div>
			{#if n < 5}<div class="prog-line" class:done={progressStep > n}></div>{/if}
		{/each}
	</div>
	{/if}

	{#if wasRestored && step !== 'wallet' && step !== 'done'}
	<div class="restore-banner" data-testid="restore-banner">
		<strong>{t('v2.restore.found')}</strong> {t('v2.restore.nopay')}
	</div>
	{/if}

	{#if step === 'wallet'}
		<!-- Step 1: Enter wallet -->
		<div class="section">
			<h1>{t('v2.client.title')}</h1>
			<p class="sub">{t('v2.client.sub')}</p>

			<div class="field">
				<label>{t('v2.client.wallet_label')}</label>
				<input
					bind:value={walletAddress}
					placeholder={t('v2.client.wallet_ph')}
					type="text"
					autocomplete="off"
					spellcheck="false"
					class:detected={!!detectCurrency(walletAddress)}
				/>
				{#if detectCurrency(walletAddress)}
					<div class="currency-tag">{detectCurrency(walletAddress)} {t('v2.client.detected')}</div>
				{/if}
				<p class="hint">{t('v2.client.wallet_hint')}</p>
			</div>

			{#if error}
				<div class="err">{error}</div>
			{/if}

			<button class="btn-primary" onclick={createIntent} disabled={loading || !walletAddress.trim()}>
				{loading ? t('v2.loading') : t('v2.client.create_intent')}
			</button>

			<p class="fine-print">{t('v2.client.fine_print')}</p>
		</div>

	{:else if step === 'invoice'}
		<!-- Step 2: Invoice -->
		<div class="section">
			<h2>{t('v2.client.invoice_title')}</h2>
			{#if invoice}
				<div class="invoice-box">
					<div class="inv-status" class:confirmed={invoice.status === 'confirmed'} class:detected={invoice.status === 'payment_detected'}>
						{t('v2.invoice.' + invoice.status)}
					</div>

					{#if invoice.status === 'pending'}
						<p class="inv-note">{t('v2.invoice.await_send')}</p>
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
						<div class="qr-wrap">
							<V2QR data={paymentURI(invoice, currency)} />
						</div>
						<ul class="inv-notes">
							<li>{t('v2.invoice.await_auto')}</li>
							<li>{t('v2.invoice.await_close')}</li>
							<li>{t('v2.invoice.await_expiry')}</li>
						</ul>
					{:else if invoice.status === 'payment_detected'}
						<p class="inv-note accent">{t('v2.invoice.detect_note')}</p>
						<p class="inv-note warn">{t('v2.invoice.detect_nopay')}</p>
						<div class="spinner"></div>
					{:else if invoice.status === 'confirmed'}
						<p class="inv-note accent">{t('v2.invoice.confirm_note')}</p>
						<div class="spinner"></div>
					{/if}
				</div>
			{/if}
			{#if error}
				<div class="err">{error} <button onclick={pollInvoiceStatus}>{t('v2.retry')}</button></div>
			{/if}
		</div>

	{:else if step === 'code'}
		<!-- Step 3: Management code gate -->
		<div class="section">
			<h2>{t('v2.code.title')}</h2>
			<p class="sub">{t('v2.code.sub')}</p>
			<div class="code-box">
				<code class="code-text">{managementCode}</code>
				<button class="copy-btn-lg" onclick={() => copyText(managementCode, t('v2.copied'))}>
					{copyMsg || t('v2.copy')}
				</button>
			</div>
			<p class="warn-box">{t('v2.code.warn')}</p>
			<label class="checkbox-row">
				<input type="checkbox" bind:checked={codeSaved} />
				<span>{t('v2.code.confirm')}</span>
			</label>
			<button class="btn-primary" onclick={() => step = 'invoice'} disabled={!codeSaved}>
				{t('v2.code.continue')}
			</button>
		</div>

	{:else if step === 'balance'}
		<!-- Step 4: Balance check result -->
		<div class="section">
			<h2>{t('v2.balance.title')}</h2>
			{#if balanceUSD !== null && balanceUSD < pubConfig.client_hard_floor_usd}
				<div class="balance-box low">
					<p>{t('v2.balance.low', { balance: balanceUSD.toFixed(0), min: '$' + pubConfig.client_hard_floor_usd })}</p>
					<p class="inv-note">{t('v2.balance.low_note')}</p>
					<p class="inv-note">{t('v2.balance.floor_applied', { floor: '$' + pubConfig.client_hard_floor_usd })}</p>
				</div>
				<button class="btn-secondary" onclick={recheckBalance} disabled={loading}>
					{loading ? t('v2.loading') : t('v2.balance.recheck')}
				</button>
			{:else}
				<div class="balance-box ok">
					<p>{t('v2.balance.ok', { balance: (balanceUSD || 0).toFixed(0) })}</p>
					<p class="inv-note">{t('v2.balance.auto_note')}</p>
				</div>
				<button class="btn-primary" onclick={() => { if (autoTransTimer) clearTimeout(autoTransTimer); step = 'telegram'; }}>
					{t('v2.balance.continue')}
				</button>
			{/if}
			{#if error}<div class="err">{error}</div>{/if}
		</div>

	{:else if step === 'telegram'}
		<!-- Step 5: Telegram connection -->
		<div class="section">
			<h2>{t('v2.telegram.title')}</h2>
			<p class="sub">{t('v2.telegram.sub')}</p>
			{#if telegramStatus === 'ready' || telegramStatus === 'active'}
				<div class="ok-badge">{t('v2.telegram.connected')}</div>
				<button class="btn-primary" onclick={() => step = 'form'}>
					{t('v2.telegram.continue')}
				</button>
			{:else if telegramStatus === 'link_pending'}
				<div class="tg-wait">{t('v2.telegram.pending')}</div>
				{#if tgLinkUrl}
					<a href={tgLinkUrl} target="_blank" rel="noopener" class="tg-btn">{t('v2.telegram.open_bot')}</a>
				{/if}
			{:else}
				<button class="tg-btn" onclick={createTelegramLink} disabled={loading}>
					{loading ? t('v2.loading') : t('v2.telegram.connect_btn')}
				</button>
			{/if}
			{#if error}<div class="err">{error}</div>{/if}
		</div>

	{:else if step === 'form'}
		<!-- Step 6: Listing form -->
		<div class="section">
			<h2>{t('v2.form.title')}</h2>

			<div class="field">
				<label>{t('new.city')}</label>
				<select bind:value={city}>
					{#each cities as c}
						<option value={c.id}>{c.label}</option>
					{/each}
				</select>
			</div>

			<div class="field">
				<label>{t('new.what_dealing')}</label>
				<div class="chip-group">
					{#each DEP_VALUES as v}
						<button
							class="chip"
							class:active={depType === v}
							onclick={() => depType = v}
						>{t('dep.' + v)}</button>
					{/each}
				</div>
			</div>

			<div class="field">
				<label>{t('new.what_help')}</label>
				<div class="chip-group">
					{#each HELP_VALUES as v}
						<button
							class="chip"
							class:active={helpType === v}
							onclick={() => helpType = v}
						>{t('help.' + v)}</button>
					{/each}
				</div>
			</div>

			<div class="field">
				<label>{t('new.how_urgent')}</label>
				<div class="chip-group">
					{#each ['urgent','soon','can_wait'] as v}
						<button class="chip" class:active={urgency === v} onclick={() => urgency = v}>
							{t('urgency.' + v)}
						</button>
					{/each}
				</div>
			</div>

			<div class="field">
				<label>{t('new.languages')}</label>
				<div class="chip-group">
					{#each LANG_VALUES as v}
						<button class="chip" class:active={languages.includes(v)} onclick={() => toggleLang(v)}>
							{v.toUpperCase()}
						</button>
					{/each}
				</div>
			</div>

			<div class="field">
				<label>{t('v2.form.contact_label')}</label>
				<div class="contact-row">
					<select bind:value={contactType} class="contact-type">
						<option value="telegram">Telegram</option>
						<option value="signal">Signal</option>
					</select>
					<input
						bind:value={contactValue}
						placeholder={contactType === 'telegram' ? '@username' : '+phonenumber'}
						type="text"
						autocomplete="off"
						class="contact-val"
					/>
				</div>
				<p class="hint">{t('v2.form.contact_hint')}</p>
			</div>

			{#if error}<div class="err">{error}</div>{/if}

			<button class="btn-primary" onclick={publishListing} disabled={loading || submitting || !canPublish()}>
				{loading ? t('v2.loading') : t('v2.form.publish')}
			</button>
		</div>

	{:else if step === 'done'}
		<!-- Step 7: Done -->
		<div class="section done">
			<div class="done-icon">✓</div>
			<h2>{t('v2.done.title')}</h2>
			<p>{t('v2.done.sub')}</p>
			{#if displayName}
				<div class="done-meta">
					<div class="done-meta-row">
						<span class="done-meta-label">{t('v2.done.display_name')}</span>
						<span class="done-meta-val" data-testid="done-display-name">{displayName}</span>
					</div>
					<p class="done-meta-note">{t('v2.done.name_note')}</p>
				</div>
			{/if}
			{#if city}
				<div class="done-meta">
					<div class="done-meta-row">
						<span class="done-meta-label">{t('v2.done.city')}</span>
						<span class="done-meta-val">{city}</span>
					</div>
				</div>
			{/if}
			{#if visibleUntil}
				<div class="done-meta">
					<div class="done-meta-row">
						<span class="done-meta-label">{t('v2.done.visible_until')}</span>
						<span class="done-meta-val">{new Date(visibleUntil * 1000).toLocaleDateString()}</span>
					</div>
				</div>
			{/if}
			<div class="done-actions">
				{#if listingId}
					<a href="/v2/listing/{listingId}" class="btn-primary" data-testid="manage-listing-btn">{t('v2.done.manage_listing')}</a>
				{/if}
				<a href="/v2/board/{city}" class="btn-secondary">{t('v2.done.board')}</a>
			</div>
			<p class="restore-hint">{t('v2.done.restore_hint')}</p>
		</div>
	{/if}
</div>

<style>
	.page {
		max-width: 540px;
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
	.field label { font-size: 13px; font-weight: 600; color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.5px; }

	input, select, textarea {
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

	@media (max-width: 600px) {
		input, select, textarea {
			font-size: 16px;
			max-width: 100%;
		}
	}

	.currency-tag {
		font-size: 11px;
		color: var(--accent);
		font-weight: 600;
		letter-spacing: 0.5px;
		text-transform: uppercase;
	}

	.hint { font-size: 12px; color: var(--text-faint); line-height: 1.4; }

	.chip-group { display: flex; flex-wrap: wrap; gap: 6px; }
	.chip {
		padding: 6px 12px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-card);
		color: var(--text-dim);
		font-size: 13px;
		cursor: pointer;
		transition: all 0.15s;
	}
	.chip:hover { border-color: var(--accent); color: var(--text); }
	.chip.active { background: var(--accent); border-color: var(--accent); color: var(--bg); font-weight: 600; }

	.contact-row { display: flex; gap: 8px; }
	.contact-type { flex: 0 0 110px; }
	.contact-val { flex: 1; }

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
		text-decoration: none;
		display: inline-block;
		text-align: center;
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

	.err { color: var(--danger); font-size: 13px; }
	.err button { color: var(--accent); background: none; border: none; cursor: pointer; margin-left: 8px; }

	.fine-print { font-size: 12px; color: var(--text-faint); line-height: 1.4; }

	/* Invoice */
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
	}
	.copy-btn:hover { border-color: var(--accent); color: var(--accent); }
	.qr-wrap { text-align: center; }
	.qr { width: 140px; height: 140px; border-radius: 6px; }
	.poll-note { font-size: 12px; color: var(--text-faint); text-align: center; }

	/* Code gate */
	.code-box {
		background: var(--bg-card);
		border: 1px solid var(--accent);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.code-text {
		font-family: monospace;
		font-size: 14px;
		color: var(--text);
		word-break: break-all;
		display: block;
	}
	.copy-btn-lg {
		background: var(--bg-hover);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text);
		font-size: 13px;
		padding: 8px 16px;
		cursor: pointer;
		align-self: flex-start;
	}
	.copy-btn-lg:hover { border-color: var(--accent); }
	.warn-box {
		background: rgba(196, 163, 90, 0.1);
		border: 1px solid var(--warn);
		border-radius: 8px;
		padding: 12px;
		font-size: 13px;
		color: var(--warn);
		line-height: 1.4;
	}
	.checkbox-row {
		display: flex;
		align-items: center;
		gap: 10px;
		cursor: pointer;
		font-size: 13px;
		color: var(--text);
	}

	/* Telegram */
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
		text-decoration: none;
		text-align: center;
		transition: opacity 0.15s;
	}
	.tg-btn:hover:not(:disabled) { opacity: 0.85; }
	.tg-btn:disabled { opacity: 0.5; cursor: not-allowed; }
	.tg-wait { color: var(--text-dim); font-size: 13px; animation: pulse 2s infinite; }
	@keyframes pulse { 0%,100% { opacity:1 } 50% { opacity:0.5 } }
	.ok-badge {
		background: rgba(123, 166, 142, 0.15);
		border: 1px solid var(--accent);
		border-radius: 8px;
		padding: 10px 14px;
		color: var(--accent);
		font-size: 13px;
		font-weight: 600;
	}

	/* Progress indicator */
	.progress-bar {
		display: flex;
		align-items: center;
		margin: 0 0 24px;
		overflow-x: auto;
		padding-bottom: 4px;
	}
	.prog-step {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 4px;
		flex-shrink: 0;
	}
	.prog-dot {
		width: 10px; height: 10px;
		border-radius: 50%;
		background: var(--border);
		transition: background 0.2s;
	}
	.prog-step.active .prog-dot { background: var(--accent); }
	.prog-step.done .prog-dot { background: var(--accent); opacity: 0.5; }
	.prog-label {
		font-size: 10px;
		color: var(--text-faint);
		white-space: nowrap;
		max-width: 64px;
		text-align: center;
		line-height: 1.2;
	}
	.prog-step.active .prog-label { color: var(--accent); font-weight: 600; }
	.prog-step.done .prog-label { color: var(--text-dim); }
	.prog-line {
		flex: 1;
		height: 1px;
		background: var(--border);
		min-width: 16px;
	}
	.prog-line.done { background: var(--accent); opacity: 0.4; }

	/* Restore banner */
	.restore-banner {
		background: rgba(196, 163, 90, 0.08);
		border: 1px solid var(--warn);
		border-radius: 8px;
		padding: 10px 14px;
		font-size: 13px;
		color: var(--warn);
		line-height: 1.4;
		margin-bottom: 4px;
	}

	/* Invoice notes */
	.inv-note { font-size: 13px; color: var(--text-dim); line-height: 1.4; margin: 0; }
	.inv-note.accent { color: var(--accent); }
	.inv-note.warn { color: var(--warn); font-weight: 600; }
	.inv-notes { font-size: 12px; color: var(--text-faint); line-height: 1.6; padding-left: 16px; margin: 0; }

	/* Spinner */
	.spinner {
		width: 24px; height: 24px;
		border: 2px solid var(--border);
		border-top-color: var(--accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
		align-self: center;
	}
	@keyframes spin { to { transform: rotate(360deg); } }

	/* Balance box */
	.balance-box {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 14px 16px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.balance-box.ok { border-color: var(--accent); }
	.balance-box.low { border-color: var(--danger); }
	.balance-box p { margin: 0; font-size: 14px; color: var(--text); }

	/* Done */
	.done { align-items: center; text-align: center; padding-top: 40px; }
	.done-icon {
		width: 56px; height: 56px;
		background: rgba(123, 166, 142, 0.15);
		border: 2px solid var(--accent);
		border-radius: 50%;
		display: flex; align-items: center; justify-content: center;
		font-size: 24px; color: var(--accent);
	}
	.done-actions { display: flex; gap: 12px; flex-wrap: wrap; justify-content: center; }
	.restore-hint { font-size: 12px; color: var(--text-faint); line-height: 1.4; }

	/* Done meta */
	.done-meta {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 10px 14px;
		width: 100%;
		max-width: 320px;
		text-align: left;
	}
	.done-meta-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
	}
	.done-meta-label { font-size: 12px; color: var(--text-faint); flex-shrink: 0; }
	.done-meta-val { font-size: 14px; color: var(--accent); font-weight: 600; text-align: right; }
	.done-meta-note { font-size: 11px; color: var(--text-faint); margin: 4px 0 0; }
</style>
