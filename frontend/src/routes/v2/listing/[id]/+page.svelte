<script>
	import { onMount, onDestroy } from 'svelte';
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

	// ── Owner mode (server-authenticated via management_code) ───────────────────
	// storage (localStorage) is never itself an authorization basis — it only
	// carries the code as input; the server independently re-verifies it on
	// every load via POST /api/v2/listings/{id}/owner-view. A private window
	// (no localStorage entry) always falls through to the public Helper view.
	let ownerView = $state(null);
	let ownerCode = $state('');
	let ownerWalletAddr = $state('');
	let ownerChecking = $state(true);
	let ownerError = $state('');
	let ownerTgLinkUrl = $state('');
	let ownerTgStatus = $state('needs_link');
	let ownerReactivating = $state(false);
	let ownerRemainingSec = $state(0);
	let ownerCountdownTimer = null;

	async function checkOwnerCapability() {
		let saved = null;
		try { saved = JSON.parse(localStorage.getItem(`v2_mgmt_${id}`) || 'null'); } catch {}
		if (!saved?.code || !saved?.wallet) { ownerChecking = false; return; }
		ownerCode = saved.code;
		ownerWalletAddr = saved.wallet;
		try {
			const res = await fetch(`/api/v2/listings/${id}/owner-view`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: ownerCode }),
			});
			if (res.ok) {
				ownerView = await res.json();
				startOwnerCountdown();
			} else {
				// Stale/invalid code for this listing — never treat storage as authority.
				try { localStorage.removeItem(`v2_mgmt_${id}`); } catch {}
				ownerView = null;
			}
		} catch {} finally {
			ownerChecking = false;
		}
	}

	function startOwnerCountdown() {
		stopOwnerCountdown();
		updateOwnerCountdown();
		ownerCountdownTimer = setInterval(updateOwnerCountdown, 1000);
	}
	function stopOwnerCountdown() {
		if (ownerCountdownTimer) { clearInterval(ownerCountdownTimer); ownerCountdownTimer = null; }
	}
	function updateOwnerCountdown() {
		if (!ownerView?.visible_until) { ownerRemainingSec = 0; return; }
		const now = Math.floor(Date.now() / 1000);
		ownerRemainingSec = Math.max(0, ownerView.visible_until - now);
	}
	function formatOwnerRemaining(sec) {
		const h = Math.floor(sec / 3600);
		const m = Math.floor((sec % 3600) / 60);
		return `${h}h ${m}m`;
	}

	async function createOwnerTelegramLink() {
		ownerError = '';
		try {
			const res = await fetch('/api/v2/client/telegram-links', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: ownerCode, wallet_address: ownerWalletAddr }),
			});
			const data = await res.json();
			if (!res.ok) {
				if (data.code === 'already_visible') { ownerTgStatus = 'active'; return; }
				ownerError = data.error || `HTTP ${res.status}`;
				return;
			}
			ownerTgLinkUrl = data.bot_url;
			ownerTgStatus = 'link_pending';
			window.open(ownerTgLinkUrl, '_blank', 'noopener');
			startOwnerTgPoll();
		} catch (e) {
			ownerError = e.message;
		}
	}

	let ownerTgPollTimer = null;
	function startOwnerTgPoll() { stopOwnerTgPoll(); ownerTgPollTimer = setInterval(pollOwnerTgStatus, 3000); }
	function stopOwnerTgPoll() { if (ownerTgPollTimer) { clearInterval(ownerTgPollTimer); ownerTgPollTimer = null; } }
	async function pollOwnerTgStatus() {
		try {
			const res = await fetch('/api/v2/client/telegram-links/status', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: ownerCode, wallet_address: ownerWalletAddr }),
			});
			if (!res.ok) return;
			const data = await res.json();
			ownerTgStatus = data.status;
			if (data.status === 'ready' || data.status === 'active') {
				stopOwnerTgPoll();
				await checkOwnerCapability();
			}
		} catch {}
	}

	async function reactivateOwnerListing() {
		ownerReactivating = true;
		ownerError = '';
		try {
			const res = await fetch('/api/v2/client/listings/reactivate', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ management_code: ownerCode, wallet_address: ownerWalletAddr }),
			});
			const data = await res.json();
			if (!res.ok) {
				ownerError = data.error || `HTTP ${res.status}`;
				return;
			}
			await checkOwnerCapability();
		} catch (e) {
			ownerError = e.message;
		} finally {
			ownerReactivating = false;
		}
	}

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
		if (!memberSinceUnix) return null;
		const days = Math.floor((Date.now() / 1000 - memberSinceUnix) / 86400);
		if (days === 0) return t('v2.rep.today');
		if (days < 7)   return t('v2.rep.days_short',   { n: days });
		if (days < 30)  return t('v2.rep.weeks_short',  { n: Math.floor(days / 7) });
		if (days < 365) return t('v2.rep.months_short', { n: Math.floor(days / 30) });
		return t('v2.rep.years_short', { n: Math.floor(days / 365) });
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
	onMount(checkOwnerCapability);
	onDestroy(() => {
		stopOwnerCountdown();
		stopOwnerTgPoll();
	});

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
				} else if (data.code === 'duplicate_active_purchase') {
					balanceOutage = false;
					helperError = t('v2.helper.duplicate_active_help');
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
			// Save to localStorage for cross-session restore (include city for back-nav)
			try {
				const lhptData = JSON.stringify({ token: purchaseToken, wallet: helperWallet.trim(), city: listing?.city || '' });
				localStorage.setItem(`v2_hpt_${id}`, lhptData);
				localStorage.setItem('v2_active_hpt', JSON.stringify({ token: purchaseToken, wallet: helperWallet.trim(), listingId: id, city: listing?.city || '' }));
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
			<a href="/v2/board/{listing?.city || ownerView?.city || 'tbilisi'}" class="back-link">
				← {t('v2.listing.back')}
			</a>
		</nav>
	</header>

	{#if loading || ownerChecking}
		<div class="status-msg">{t('v2.loading')}</div>

	{:else if ownerView}
		<!-- ── Owner mode: server-authenticated via management_code. ──────────────
		     Independent of the public listing fetch above (which 404s for
		     hidden/finished listings) — owner mode uses ownerView exclusively.
		     The Helper wallet form, Helper progress, and "Get contact" button
		     are entirely absent here. -->
		<div class="owner-card" data-testid="owner-mode">
			<div class="owner-head">
				<h2>{t('v2.owner.title')}</h2>
				<p class="sub">{t('v2.owner.subtitle')}</p>
			</div>

			<div class="listing-card">
				<div class="urgency-strip" style="background: {urgencyColor(ownerView.urgency)}"></div>
				<div class="listing-body">
					<div class="listing-head">
						<div class="dep">{t('dep.' + ownerView.dep_type)}</div>
					</div>
					<div class="help">{t('help.' + ownerView.help_type)}</div>
					<div class="meta-row">
						<span class="langs">{(ownerView.languages || []).join(', ').toUpperCase()}</span>
					</div>
					{#if ownerView.display_name}
						<div class="client-name">
							<span class="client-name-label">{t('v2.rep.label')}:</span>
							<span>{ownerView.display_name}</span>
						</div>
					{/if}
				</div>
			</div>

			<div class="owner-meta">
				<div class="owner-meta-row">
					<span class="owner-meta-label">{t('v2.done.city')}</span>
					<span class="owner-meta-val">{ownerView.city}</span>
				</div>
				<div class="owner-meta-row">
					<span class="owner-meta-label">{t('v2.owner.title')}</span>
					<span class="owner-meta-val" data-testid="owner-state">
						{#if ownerView.state === 'visible'}{t('v2.owner.state_visible')}
						{:else if ownerView.state === 'hidden'}{t('v2.owner.state_hidden')}
						{:else}{t('v2.owner.state_finished')}{/if}
					</span>
				</div>
				{#if ownerView.state === 'visible' && ownerView.visible_until}
					<div class="owner-meta-row">
						<span class="owner-meta-label">{t('v2.done.visible_until')}</span>
						<span class="owner-meta-val">{new Date(ownerView.visible_until * 1000).toLocaleString()}</span>
					</div>
					<div class="owner-meta-row owner-remaining">
						{t('v2.owner.remaining', { time: formatOwnerRemaining(ownerRemainingSec) })}
					</div>
				{/if}
				{#if ownerView.state !== 'finished'}
					<div class="owner-meta-row">
						<span class="owner-meta-label">Telegram</span>
						<span class="owner-meta-val" data-testid="owner-telegram-status">
							{ownerView.telegram_ready ? t('v2.owner.telegram_ready') : t('v2.owner.telegram_not_ready')}
						</span>
					</div>
				{/if}
			</div>

			{#if ownerError}<div class="err">{ownerError}</div>{/if}

			{#if ownerView.state === 'hidden'}
				{#if ownerView.telegram_ready}
					<button class="btn-primary" data-testid="owner-reactivate-btn" onclick={reactivateOwnerListing} disabled={ownerReactivating}>
						{ownerReactivating ? t('v2.loading') : t('v2.owner.reactivate_btn')}
					</button>
				{:else if ownerTgStatus === 'link_pending' && ownerTgLinkUrl}
					<a href={ownerTgLinkUrl} target="_blank" rel="noopener" class="btn-primary owner-tg-btn">{t('v2.telegram.open_bot')}</a>
				{:else}
					<button class="btn-primary" data-testid="owner-connect-telegram-btn" onclick={createOwnerTelegramLink}>
						{t('v2.owner.connect_telegram_btn')}
					</button>
				{/if}
			{:else if ownerView.state === 'finished'}
				<a href="/v2/new?fresh=1" class="btn-primary btn-link">{t('v2.owner.state_finished')} · {t('back_to_board')}</a>
			{/if}

			<a href="/v2/board/{listing?.city || ownerView.city || 'tbilisi'}" class="btn-secondary" data-testid="owner-back-board">{t('v2.listing.back')}</a>
		</div>

	{:else if error}
		<div class="status-msg error">{error}</div>
	{:else if listing}
		<div class="listing-card">
			<div class="urgency-strip" style="background: {urgencyColor(listing.urgency)}"></div>
			<div class="listing-body">
				<div class="listing-head">
					<div class="dep">{t('dep.' + listing.dependency_type)}</div>
				</div>
				<div class="help">{t('help.' + listing.help_type)}</div>
				<div class="meta-row">
					<span class="langs">{(listing.languages || []).join(', ').toUpperCase()}</span>
					{#if listing.client_reputation}
						{@const repAge = reputationAge(listing.client_reputation.member_since_unix ?? listing.client_reputation.member_since)}
						{#if repAge}
							<span class="rep-age">{t('v2.rep.since', { age: repAge })}</span>
						{/if}
						<span class="rep-score">
							{t('v2.rep.rating', { pos: listing.client_reputation.positive_count ?? 0, neg: listing.client_reputation.negative_count ?? 0 })}
						</span>
					{/if}
				</div>
				{#if listing.display_name}
					<div class="client-name">
						<span class="client-name-label">{t('v2.rep.label')}:</span>
						<span>{listing.display_name}</span>
					</div>
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
	/* ── Owner mode ── */
	.owner-card {
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	.owner-head h2 { font-size: 18px; font-weight: 700; color: var(--text); margin: 0 0 4px; }
	.owner-head .sub { color: var(--text-dim); font-size: 13px; margin: 0; }
	.owner-meta {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 12px 16px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.owner-meta-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
		font-size: 13px;
	}
	.owner-meta-label { color: var(--text-faint); }
	.owner-meta-val { color: var(--text); font-weight: 600; }
	.owner-remaining {
		justify-content: flex-start;
		color: var(--accent);
		font-weight: 600;
	}

	.page {
		max-width: 600px;
		width: 100%;
		box-sizing: border-box;
		margin: 0 auto;
		padding: 0 16px 60px;
		overflow-x: hidden;
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
	.help { font-size: 13px; color: var(--text-dim); }
	.meta-row { display: flex; align-items: center; gap: 12px; margin-top: 4px; }
	.langs { font-size: 11px; color: var(--text-faint); }
	.rep-age { font-size: 10px; color: var(--text-faint); }
	.rep-score { font-size: 12px; color: var(--text-dim); }

	.helper-section { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
	h2 { font-size: 18px; font-weight: 700; color: var(--text); margin: 0; }
	.sub { color: var(--text-dim); font-size: 14px; line-height: 1.5; margin: 0; }

	.field { display: flex; flex-direction: column; gap: 6px; }
	.field label { font-size: 13px; font-weight: 600; color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.5px; }

	input {
		width: 100%;
		min-width: 0;
		box-sizing: border-box;
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
	.err {
		color: var(--danger);
		font-size: 13px;
		line-height: 1.4;
		overflow-wrap: anywhere;
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

	.notice-box {
		background: rgba(196, 163, 90, 0.08);
		border: 1px solid var(--warn);
		border-radius: 8px;
		padding: 12px 14px;
	}
	.notice-title { font-size: 12px; font-weight: 700; color: var(--warn); text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 8px; }
	.notice-list { margin: 0; padding: 0 0 0 16px; display: flex; flex-direction: column; gap: 4px; }
	.notice-list li { font-size: 12px; color: var(--text-dim); line-height: 1.4; }
	.client-name { font-size: 11px; color: var(--text-faint); display: flex; gap: 4px; align-items: baseline; }
	.client-name-label { color: var(--text-faint); font-weight: 600; }

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

	@media (max-width: 600px) {
		.page {
			padding: 0 14px 72px;
		}
		header {
			padding: 14px 0 16px;
			margin-bottom: 16px;
		}
		.listing-card {
			margin-bottom: 16px;
		}
		.listing-body {
			padding: 13px;
		}
		.helper-section {
			gap: 12px;
		}
		.sub {
			font-size: 13px;
		}
		.progress-bar {
			width: 100%;
			min-width: 0;
		}
		.progress-step {
			flex: 0 0 auto;
		}
		.progress-label {
			display: none;
		}
		.progress-line {
			min-width: 12px;
			margin: 0 6px;
		}
		.notice-box {
			padding: 10px 12px;
		}
		.notice-list li {
			font-size: 11px;
		}
		.btn-primary,
		.btn-secondary {
			width: 100%;
			box-sizing: border-box;
		}
		.fine-print {
			margin: 0;
		}
	}
</style>
