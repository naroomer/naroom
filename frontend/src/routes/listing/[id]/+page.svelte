<script>
	import { goto } from '$app/navigation';
	import { onMount, onDestroy } from 'svelte';
	import nacl from 'tweetnacl';
	import { lang, t as tFn, pluralRu } from '$lib/i18n.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	function bytesToHex(bytes) {
		return Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
	}

	let { data } = $props();
	const listing = $derived(data.listing);

	function urgencyColor(u) {
		if (u === 'urgent') return 'var(--urgent)';
		if (u === 'soon')   return 'var(--warn)';
		return 'var(--can-wait)';
	}
	function timeLeft(seconds) {
		if (seconds <= 0) return t('time.expired');
		const h = Math.floor(seconds / 3600);
		const m = Math.floor((seconds % 3600) / 60);
		if (h > 0) return t('time.h_m_left', {h, m});
		return t('time.m_left', {m});
	}
	function sessionsLabel(n) {
		if ($lang === 'ru') {
			const form = pluralRu(n);
			const key = form === 'one' ? 'listing.sessions_one' : form === 'few' ? 'listing.sessions_few' : 'listing.sessions_other';
			return t(key, {n});
		}
		return t(n === 1 ? 'listing.sessions_one' : 'listing.sessions_other', {n});
	}
	function shortAddr(addr) {
		if (!addr) return '';
		return addr.length > 16 ? addr.slice(0, 8) + '...' + addr.slice(-6) : addr;
	}

	function balanceTierStr(tier) {
		if (!tier || tier < 1) return '';
		return '$'.repeat(Math.min(tier, 5));
	}

	function memberSinceStr(ts) {
		if (!ts) return '';
		const days = Math.floor((Date.now() / 1000 - ts) / 86400);
		if (days < 7)   return t('listing.days_platform',   {n: days});
		if (days < 30)  return t('listing.weeks_platform',  {n: Math.floor(days/7)});
		if (days < 365) return t('listing.months_platform', {n: Math.floor(days/30)});
		return t('listing.years_platform', {n: Math.floor(days/365)});
	}

	function ratingStr(rep) {
		const total = rep.thumbs_up + rep.thumbs_down;
		if (total === 0) return null;
		const pct = Math.round(rep.thumbs_up / total * 100);
		return t('listing.positive', {pct});
	}
	function timeAgo(ts) {
		const diff = Math.floor(Date.now()/1000) - ts;
		if (diff < 60) return 'just now';
		if (diff < 3600) return `${Math.floor(diff/60)}m ago`;
		return `${Math.floor(diff/3600)}h ago`;
	}

	// Detect BTC vs LTC from address prefix
	function detectCurrency(addr) {
		if (!addr || addr.length < 3) return null;
		const a = addr.trim();
		if (/^ltc1/i.test(a) || /^[LM]/.test(a)) return 'LTC';
		if (/^bc1/i.test(a) || /^[13]/.test(a)) return 'BTC';
		return null;
	}

	// ── Peer respond form ──────────────────────────────────────────────
	let showRespond  = $state(false);
	let peerWallet = $state('');
	let peerCurrency = $state('BTC');
	let respondLoading = $state(false);
	let respondError   = $state('');
	let peerBalanceLow = $state(null); // {balance, required} when balance < $1000
	let responded         = $state(false);
	let peerPendingInvoice = $state(null); // invoice peer needs to pay to open chat
	let peerInvoiceAddrCopied = $state(false);

	function copyPeerInvoiceAddr() {
		if (!peerPendingInvoice?.address) return;
		navigator.clipboard.writeText(peerPendingInvoice.address).then(() => {
			peerInvoiceAddrCopied = true;
			setTimeout(() => peerInvoiceAddrCopied = false, 2000);
		});
	}

	// Region lock state
	let regionLockState = $state('idle'); // idle | warning | locked_other
	let peerLockedCity  = $state('');     // city peer is already locked to

	// Peer session recovery gate
	let peerRecoveryCode = $state('');
	let peerShowRecovery = $state(false);
	let peerPendingFn    = $state(null);
	// Client session recovery gate
	let clientRecoveryCode = $state('');
	let clientShowRecovery = $state(false);
	let clientPendingFn    = $state(null);

	// Get or create a principal session for `role`.
	// Returns {token, recoveryCode?}. recoveryCode is non-null only when a new session was just created.
	async function ensureSession(role) {
		const key = `naroom_session_${role}`;
		const stored = sessionStorage.getItem(key) ?? '';
		if (stored) {
			const r = await fetch('/api/session/status', { headers: { 'Authorization': `Bearer ${stored}` } });
			if (r.ok) return { token: stored, recoveryCode: null };
		}
		const initR = await fetch('/api/session/init', {
			method: 'POST', headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ role }),
		});
		if (!initR.ok) throw new Error('Failed to initialize session');
		const { session_token, recovery_code } = await initR.json();
		sessionStorage.setItem(key, session_token);
		return { token: session_token, recoveryCode: recovery_code };
	}

	// Poll for chat room after peer responds (waiting for client to accept)
	let peerPollTimer;

	function startPeerPoll(listingId) {
		const token = sessionStorage.getItem('naroom_session_peer') ?? '';
		peerPollTimer = setInterval(async () => {
			try {
				// First check if chat room already opened (peer already paid)
				const res = await fetch(`/api/peer/chatroom?listing_id=${encodeURIComponent(listingId)}`, {
					headers: {
						...(token ? { 'Authorization': `Bearer ${token}` } : {}),
						'X-Dev-Wallet': peerWallet,
						'X-Dev-Role': 'peer',
					},
				});
				if (res.ok) {
					const data = await res.json();
					if (data.room_id) {
						clearInterval(peerPollTimer);
						goto(`/chat/${data.room_id}`);
					}
					return;
				}
				// No chat room yet — check if client accepted and invoice appeared
				if (!peerPendingInvoice) {
					const pi = await fetch('/api/peer/invoice', {
						headers: token ? { 'Authorization': `Bearer ${token}` } : {},
					});
					if (pi.ok) {
						const piData = await pi.json();
						peerPendingInvoice = piData;
					}
				}
			} catch {}
		}, 4000);
	}

	// Generate or recall NaCl keypair for peer — returns public key hex
	function getPeerPubkey() {
		let pubHex = sessionStorage.getItem('peer_pubkey');
		if (!pubHex) {
			const kp = nacl.box.keyPair();
			pubHex = bytesToHex(kp.publicKey);
			sessionStorage.setItem('peer_pubkey', pubHex);
			sessionStorage.setItem('peer_privkey', bytesToHex(kp.secretKey));
		}
		return pubHex;
	}

	// Auto-detect currency from peer wallet address
	$effect(() => {
		const detected = detectCurrency(peerWallet);
		if (detected) peerCurrency = detected;
	});

	// Auto-check for pending invoice when peer enters wallet (debounced)
	let peerWalletCheckTimer;
	$effect(() => {
		if (!peerWallet || peerWallet.length < 20) return;
		clearTimeout(peerWalletCheckTimer);
		peerWalletCheckTimer = setTimeout(() => checkPeerInvoiceQuick(), 800);
	});

	async function checkPeerInvoiceQuick() {
		if (!peerWallet) return;
		const detectedQuick = detectCurrency(peerWallet);
		if (detectedQuick) peerCurrency = detectedQuick;
		try {
			// Auto-check only works when a valid peer session is already present; don't silently init one.
			const stored = sessionStorage.getItem('naroom_session_peer') ?? '';
			if (!stored) return;
			const sr = await fetch('/api/session/status', { headers: { 'Authorization': `Bearer ${stored}` } });
			if (!sr.ok) return;
			const token = stored;
			const vr = await fetch('/api/wallet/register', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
				body: JSON.stringify({ wallet_address: peerWallet, currency: peerCurrency, role: 'peer' }),
			});
			if (!vr.ok) {
				if (vr.status === 402) {
					const errData = await vr.json().catch(() => ({}));
					if (errData.balance_usd !== undefined) {
						peerBalanceLow = { balance: Math.round(errData.balance_usd), required: errData.required_usd ?? 1000 };
					}
				}
				return;
			}
			peerBalanceLow = null;
			const pi = await fetch('/api/peer/invoice', {
				headers: { 'Authorization': `Bearer ${token}` },
			});
			if (pi.ok) {
				const piData = await pi.json();
				peerPendingInvoice = piData;
				startPeerPoll(piData.listing_id || listing.id);
			}
		} catch {}
	}

	async function checkRegionAndRespond() {
		if (!peerWallet) return;
		respondLoading = true; respondError = '';
		const detectedPeer = detectCurrency(peerWallet);
		if (detectedPeer) peerCurrency = detectedPeer;
		try {
			const { token, recoveryCode } = await ensureSession('peer');
			if (recoveryCode) {
				peerRecoveryCode = recoveryCode;
				peerPendingFn = async () => {
					peerShowRecovery = false;
					await continueCheckRegionAndRespond(token);
				};
				peerShowRecovery = true;
				respondLoading = false;
				return;
			}
			await continueCheckRegionAndRespond(token);
		} catch(e) { respondError = e.message; }
		finally { respondLoading = false; }
	}

	async function continueCheckRegionAndRespond(token) {
		respondLoading = true; respondError = '';
		try {
			// Register wallet (balance check + link to principal) with Bearer token
			const vr = await fetch('/api/wallet/register', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
				body: JSON.stringify({ wallet_address: peerWallet, currency: peerCurrency, role: 'peer' }),
			});
			if (!vr.ok) {
				const errData = await vr.json().catch(() => ({}));
				if (vr.status === 402 && errData.balance_usd !== undefined) {
					peerBalanceLow = { balance: Math.round(errData.balance_usd), required: errData.required_usd ?? 1000 };
					return;
				}
				throw new Error(errData.error ?? 'Wallet verification failed');
			}
			peerBalanceLow = null;

			// Check if peer already has a pending invoice (lost page recovery)
			const pi = await fetch('/api/peer/invoice', {
				headers: { 'Authorization': `Bearer ${token}` },
			});
			if (pi.ok) {
				const piData = await pi.json();
				peerPendingInvoice = piData;
				startPeerPoll(piData.listing_id || listing.id);
				return;
			}

			// Check region lock
			const rr = await fetch('/api/peer/region', {
				headers: {
					'Authorization': `Bearer ${token}`,
					'X-Dev-Wallet': peerWallet,
					'X-Dev-Role': 'peer',
				},
			});
			const regionData = rr.ok ? await rr.json() : { region: null };

			if (regionData.region === null) {
				regionLockState = 'warning';
			} else if (regionData.region !== listing.city) {
				peerLockedCity = regionData.region;
				regionLockState = 'locked_other';
			} else {
				await doSubmitRespond(token);
			}
		} catch(e) { respondError = e.message; }
		finally { respondLoading = false; }
	}

	async function doSubmitRespond(token) {
		token = token ?? sessionStorage.getItem('naroom_session_peer') ?? '';
		respondLoading = true; respondError = '';
		try {
			const rr = await fetch(`/api/listing/${listing.id}/respond`, {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					...(token ? { 'Authorization': `Bearer ${token}` } : {}),
					'X-Dev-Wallet': peerWallet,
					'X-Dev-Role': 'peer',
				},
				body: JSON.stringify({ peer_pubkey: getPeerPubkey() }),
			});
			if (!rr.ok) throw new Error((await rr.json()).error ?? 'Failed to submit response');
			regionLockState = 'idle';
			responded = true;
			startPeerPoll(listing.id);
		} catch(e) { respondError = e.message; }
		finally { respondLoading = false; }
	}

	// ── Client: view my responses ──────────────────────────────────────────
	let clientWallet = $state('');
	let clientCurrency = $state('BTC');
	let myResponses    = $state(null); // null = not loaded
	let loadingResponses = $state(false);
	let responsesError   = $state('');

	// Invoice for accepted peer ($15)
	let acceptInvoice  = $state(null);
	let acceptLoading  = $state(false);
	let acceptError    = $state('');

	// Existing chat room (recovery after page refresh)
	let existingChatRoom = $state(null);

	// Poll for chat room after accept
	let chatPollTimer;

	// Ensures a valid client session and registers the wallet with Bearer token.
	// Returns {token, recoveryCode?}. recoveryCode is set when a new session was just created
	// (caller must show recovery gate before calling /wallet/register).
	async function getClientSession() {
		const { token, recoveryCode } = await ensureSession('client');
		if (recoveryCode) {
			return { token, recoveryCode };
		}
		// Register wallet (balance check + link) with Bearer token
		const vr = await fetch('/api/wallet/register', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
			body: JSON.stringify({ wallet_address: clientWallet, currency: clientCurrency, role: 'client' }),
		});
		if (!vr.ok) throw new Error((await vr.json()).error ?? 'Wallet verification failed');
		sessionStorage.setItem('naroom_wallet_client', clientWallet);
		sessionStorage.setItem('naroom_currency_client', clientCurrency);
		return { token, recoveryCode: null };
	}

	function clientAuthHeaders(token) {
		return {
			'Content-Type': 'application/json',
			...(token ? { 'Authorization': `Bearer ${token}` } : {}),
			'X-Dev-Wallet': clientWallet,
			'X-Dev-Role': 'client',
		};
	}

	async function loadResponses() {
		if (!clientWallet) { responsesError = 'Enter your wallet address'; return; }
		loadingResponses = true; responsesError = '';
		try {
			const { token, recoveryCode } = await getClientSession();
			if (recoveryCode) {
				clientRecoveryCode = recoveryCode;
				clientPendingFn = async () => {
					clientShowRecovery = false;
					// Register wallet after user acks recovery code
					const vr = await fetch('/api/wallet/register', {
						method: 'POST',
						headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
						body: JSON.stringify({ wallet_address: clientWallet, currency: clientCurrency, role: 'client' }),
					});
					if (!vr.ok) { responsesError = (await vr.json()).error ?? 'Wallet verification failed'; return; }
					sessionStorage.setItem('naroom_wallet_client', clientWallet);
					await doLoadResponses(token);
				};
				clientShowRecovery = true;
				loadingResponses = false;
				return;
			}
			await doLoadResponses(token);
		} catch(e) { responsesError = e.message; }
		finally { loadingResponses = false; }
	}

	async function doLoadResponses(token) {
		loadingResponses = true;
		try {
			// Recovery: check if chat room already exists (e.g. after page refresh post-accept)
			const cr = await fetch(`/api/listing/${listing.id}/chatroom`, {
				headers: clientAuthHeaders(token),
			});
			if (cr.ok) {
				const crData = await cr.json();
				if (crData.room_id) { existingChatRoom = crData; return; }
			}
			const res = await fetch(`/api/listing/${listing.id}/responses`, {
				headers: clientAuthHeaders(token),
			});
			if (!res.ok) throw new Error((await res.json()).error ?? 'Failed to load');
			myResponses = await res.json();
		} catch(e) { responsesError = e.message; }
		finally { loadingResponses = false; }
	}

	// Generate or recall NaCl keypair for client — returns public key hex
	function getClientPubkey() {
		let pubHex = sessionStorage.getItem('client_pubkey_' + listing.id);
		if (!pubHex) {
			const kp = nacl.box.keyPair();
			pubHex = bytesToHex(kp.publicKey);
			sessionStorage.setItem('client_pubkey_' + listing.id, pubHex);
			sessionStorage.setItem('client_privkey', bytesToHex(kp.secretKey));
		}
		return pubHex;
	}

	async function acceptResponse(responseId) {
		acceptLoading = true; acceptError = '';
		try {
			const { token } = await ensureSession('client');
			const res = await fetch(`/api/response/${responseId}/accept`, {
				method: 'POST',
				headers: clientAuthHeaders(token),
				body: JSON.stringify({
					client_pubkey: getClientPubkey(),
					currency:      clientCurrency,
				}),
			});
			if (!res.ok) throw new Error((await res.json()).error ?? 'Accept failed');
			acceptInvoice = await res.json();
			startChatPoll(token);
		} catch(e) { acceptError = e.message; }
		finally { acceptLoading = false; }
	}

	function startChatPoll(token) {
		chatPollTimer = setInterval(async () => {
			try {
				const res = await fetch(`/api/listing/${listing.id}/chatroom`, {
					headers: clientAuthHeaders(token),
				});
				if (!res.ok) return;
				const data = await res.json();
				if (data.room_id) {
					clearInterval(chatPollTimer);
					goto(`/chat/${data.room_id}`);
				}
			} catch {}
		}, 3000);
	}

	// ── Listing renew ──────────────────────────────────────────────────
	let renewLoading = $state(false);
	let renewError   = $state('');
	let renewDone    = $state(false);

	// Show renew button when: owner wallet entered, can_renew=true, expiring soon or expired
	let showRenew = $derived(
		listing.can_renew &&
		!!clientWallet &&
		myResponses !== null && // owner confirmed by loading responses
		(listing.time_left < 3600 || listing.status === 'expired')
	);

	async function startRenew() {
		renewLoading = true; renewError = '';
		try {
			const { token } = await ensureSession('client');
			const res = await fetch(`/api/listing/${listing.id}/renew`, {
				method: 'POST',
				headers: clientAuthHeaders(token),
			});
			if (!res.ok) throw new Error((await res.json()).error ?? 'Renew failed');
			renewDone = true;
			// Reload after short delay so user sees success message
			setTimeout(() => window.location.reload(), 1500);
		} catch(e) { renewError = e.message; }
		finally { renewLoading = false; }
	}

	// Auto-detect currency from client wallet address
	$effect(() => {
		const detected = detectCurrency(clientWallet);
		if (detected) clientCurrency = detected;
	});

	// Load saved wallet from sessionStorage (browser-only)
	onMount(async () => {
		const saved = sessionStorage.getItem('my_wallet_' + listing.id);
		if (saved) clientWallet = saved;
		const savedCurrency = sessionStorage.getItem('my_currency_' + listing.id);
		if (savedCurrency) clientCurrency = savedCurrency;

		// Auto-check for an existing chat room using stored session (validate first).
		// Covers the case where the client returns after a peer accepted and paid.
		const stored = sessionStorage.getItem('naroom_session_client') ?? '';
		if (stored && listing.status === 'active') {
			try {
				const sr = await fetch('/api/session/status', { headers: { 'Authorization': `Bearer ${stored}` } });
				if (sr.ok) {
					const res = await fetch(`/api/listing/${listing.id}/chatroom`, {
						headers: { 'Authorization': `Bearer ${stored}` },
					});
					if (res.ok) {
						const d = await res.json();
						if (d.room_id) existingChatRoom = d;
					}
				}
			} catch {}
		}

		// Check Telegram notification status for owner (non-blocking, runs after critical checks).
		await checkOwnerTelegram();
	});

	// ── Owner Telegram section ────────────────────────────────────────────
	// States: idle | checking | not_owner | disconnected | connecting | connected | expired | error
	let ownerTgState  = $state('idle');
	let ownerTgBotUrl = $state('');
	let ownerTgError  = $state('');
	let ownerTgPollTimer;

	async function checkOwnerTelegram() {
		if (listing.status !== 'active' || listing.time_left <= 0) return;
		const clientToken = sessionStorage.getItem('naroom_session_client') ?? '';
		if (!clientToken) return;
		ownerTgState = 'checking';
		try {
			const sr = await fetch('/api/session/status', { headers: { 'Authorization': `Bearer ${clientToken}` } });
			if (!sr.ok) { ownerTgState = 'idle'; return; }
			const cr = await fetch(`/api/telegram/client/confirm?listing_id=${listing.id}`, {
				headers: { 'Authorization': `Bearer ${clientToken}` },
			});
			if (cr.status === 403 || cr.status === 404) { ownerTgState = 'not_owner'; return; }
			if (!cr.ok) { ownerTgState = 'idle'; return; }
			const cd = await cr.json();
			ownerTgState = cd.confirmed ? 'connected' : 'disconnected';
		} catch { ownerTgState = 'idle'; }
	}

	async function connectTelegram() {
		const clientToken = sessionStorage.getItem('naroom_session_client') ?? '';
		if (!clientToken) return;
		ownerTgState = 'connecting';
		ownerTgError = '';
		ownerTgBotUrl = '';
		try {
			const res = await fetch('/api/telegram/client/token', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${clientToken}` },
				body: JSON.stringify({ listing_id: listing.id }),
			});
			if (!res.ok) {
				const e = await res.json().catch(() => ({}));
				ownerTgError = e.error ?? 'Failed to get token';
				ownerTgState = 'error';
				return;
			}
			const d = await res.json();
			ownerTgBotUrl = d.bot_url;
			startOwnerTgPoll(clientToken, d.expires_in ?? 600);
		} catch (e) {
			ownerTgError = e.message;
			ownerTgState = 'error';
		}
	}

	function startOwnerTgPoll(clientToken, expiresIn) {
		clearInterval(ownerTgPollTimer);
		const deadline = Date.now() + expiresIn * 1000;
		ownerTgPollTimer = setInterval(async () => {
			if (Date.now() > deadline) {
				clearInterval(ownerTgPollTimer);
				ownerTgState = 'expired';
				return;
			}
			try {
				const res = await fetch(`/api/telegram/client/confirm?listing_id=${listing.id}`, {
					headers: { 'Authorization': `Bearer ${clientToken}` },
				});
				if (!res.ok) return;
				const d = await res.json();
				if (d.confirmed) {
					clearInterval(ownerTgPollTimer);
					ownerTgState = 'connected';
				}
			} catch {}
		}, 3000);
	}

	// Cleanup poll timers on unmount
	onDestroy(() => {
		clearInterval(chatPollTimer);
		clearInterval(peerPollTimer);
		clearInterval(ownerTgPollTimer);
	});
</script>

<div class="page">
	<header>
		<a href="/board/{listing.city}" class="back">{t('back_to_board')}</a>
	</header>

	<div class="urgency-bar" style="background: {urgencyColor(listing.urgency)}"></div>

	{#if listing.is_sample}
		<div class="sample-banner">{t('board.example')} — {t('listing.sample_hint')}</div>
	{/if}

	<!-- Listing info card -->
	<div class="listing-card">
		<div class="listing-header">
			<div>
				<div class="dep">{t('dep.' + listing.dependency_type)}</div>
				<div class="help">{t('help.' + listing.help_type)}</div>
			</div>
			<span class="urgency-tag" style="color: {urgencyColor(listing.urgency)}; border-color: {urgencyColor(listing.urgency)}">
				{t('urgency.' + listing.urgency)}
			</span>
		</div>
		<div class="listing-meta">
			<div class="meta-item">
				<span class="meta-label">{t('listing.languages')}</span>
				<span class="meta-value">{listing.languages?.map(l => l.toUpperCase()).join(', ')}</span>
			</div>
			<div class="meta-item">
				<span class="meta-label">{t('listing.time_left')}</span>
				<span class="meta-value">{timeLeft(listing.time_left)}</span>
			</div>
			{#if listing.responses_count > 0}
			<div class="meta-item">
				<span class="meta-label">{t('listing.responses')}</span>
				<span class="meta-value" style="color: var(--accent)">{listing.responses_count}</span>
			</div>
			{/if}
		</div>
	</div>

	{#if listing.status === 'active' && listing.time_left > 0}

		<!-- ── COUNSELOR SECTION (only when a second peer can still respond) ── -->
		{#if (listing.opened_chats_count ?? 0) < 2}
		<div class="section">
			<div class="section-title">{t('listing.can_help')}</div>

			{#if peerPendingInvoice}
				<div class="success-box">
					<span class="success-icon">✓</span>
					<div>
						<div class="success-title">{t('listing.accepted_pay_title')}</div>
						<div class="success-sub">{t('listing.accepted_pay_sub')}</div>
					</div>
				</div>
				<div class="invoice-box invoice-box--pay">
					<div class="invoice-amount">{peerPendingInvoice.amount_crypto} {peerPendingInvoice.currency}</div>
					<div class="invoice-addr-row">
						<div class="invoice-addr">{peerPendingInvoice.address}</div>
						<button class="copy-btn" onclick={copyPeerInvoiceAddr}>
							{peerInvoiceAddrCopied ? t('listing.copied') : t('listing.copy_address')}
						</button>
					</div>
				</div>
				<div class="balance-warn">⚠ {t('listing.peer_balance_warn')}</div>
				<div class="poll-row">
					<span class="dot"></span>
					{t('checking_auto')}
				</div>
			{:else if responded}
				<div class="success-box">
					<span class="success-icon">✓</span>
					<div>
						<div class="success-title">{t('listing.response_sent')}</div>
						<div class="success-sub">{t('listing.response_sent_sub')}</div>
					</div>
				</div>
				<div class="poll-row">
					<span class="dot"></span>
					{t('checking_auto')}
				</div>
			{:else if !showRespond}
				<p class="section-desc">
					{t('listing.peer_desc', {dep: t('dep.' + listing.dependency_type).toLowerCase()})}
				</p>
				<button class="btn-primary" onclick={() => showRespond = true}>{t('listing.respond_as_peer')}</button>
			{:else}
				<div class="sub-form">
					<div class="field">
						<label for="peer-wallet">{t('listing.peer_wallet')}</label>
						<input id="peer-wallet" type="text" placeholder={t('listing.enter_address')} bind:value={peerWallet} />
					</div>
					{#if regionLockState === 'locked_other'}
						<div class="region-blocked">
							{t('hiw.region_locked_other', { city: peerLockedCity })}
						</div>
					{:else if regionLockState === 'warning'}
						<div class="region-warning">
							<div class="region-warning-title">⚠ {t('hiw.region_lock_title')}</div>
							<p class="region-warning-body">{t('hiw.region_lock_body', { city: listing.city })}</p>
							<div class="form-actions">
								<button class="btn-primary" disabled={respondLoading} onclick={() => doSubmitRespond(null)}>
									{respondLoading ? t('listing.sending') : t('hiw.region_lock_confirm')}
								</button>
								<button class="btn-ghost" onclick={() => { regionLockState = 'idle'; showRespond = false; }}>{t('cancel')}</button>
							</div>
						</div>
					{:else}
						{#if peerBalanceLow}
							<div class="error">{t('listing.peer_low_balance', {balance: peerBalanceLow.balance, required: peerBalanceLow.required})}</div>
						{:else if respondError}
							<div class="error">{respondError}</div>
						{/if}
						<div class="form-actions">
							<button class="btn-primary" disabled={!peerWallet || respondLoading} onclick={checkRegionAndRespond}>
								{respondLoading ? t('listing.sending') : t('listing.send_response')}
							</button>
							<button class="btn-ghost" onclick={() => { showRespond = false; respondError = ''; regionLockState = 'idle'; peerBalanceLow = null; }}>{t('cancel')}</button>
						</div>
						<p class="fine">{t('listing.no_funds')}</p>
					{/if}
				</div>
			{/if}
		</div>
		{/if}

		<!-- ── CLIENT SECTION ── -->
		<div class="section">
			<div class="section-title">{t('listing.i_posted')}</div>

			{#if existingChatRoom}
				<!-- Recovery: chat room already exists -->
				<div class="invoice-box" style="cursor:pointer" onclick={() => goto(`/chat/${existingChatRoom.room_id}`)}>
					<div class="invoice-icon">💬</div>
					<div>
						<div class="invoice-title">{t('listing.chat_ready') || 'Chat is open'}</div>
						<div class="invoice-sub">{t('listing.chat_ready_sub') || 'Your session is active. Tap to continue.'}</div>
					</div>
				</div>
				<button class="btn-primary" style="margin-top:10px" onclick={() => goto(`/chat/${existingChatRoom.room_id}`)}>
					{t('listing.go_to_chat') || 'Go to chat →'}
				</button>

			{:else if acceptInvoice}
				<!-- Waiting for peer to pay $15 -->
				<div class="invoice-box">
					<div class="invoice-icon">⏳</div>
					<div>
						<div class="invoice-title">{t('listing.waiting_peer')}</div>
						<div class="invoice-sub">{t('listing.waiting_peer_sub', {amount: acceptInvoice.amount_crypto, currency: acceptInvoice.currency})}</div>
					</div>
				</div>
				<div class="poll-row">
					<span class="dot"></span>
					{t('checking_auto')}
				</div>

			{:else if myResponses !== null}
				<!-- Responses loaded -->
				{#if myResponses.length === 0}
					<div class="empty-responses">{t('listing.no_responses')}</div>
				{:else}
					<div class="responses-list">
						{#each myResponses as resp, i}
							<div class="response-card" class:new-counselor={resp.reputation?.is_new}>
								<div class="resp-main">
									<div class="resp-header">
										<span class="resp-label">{t('listing.peer_n', {n: i + 1})}</span>
										{#if resp.reputation?.is_new}
											<span class="badge-new">{t('listing.badge_new')}</span>
										{/if}
										{#if balanceTierStr(resp.reputation?.balance_tier)}
											<span class="badge-tier">{balanceTierStr(resp.reputation?.balance_tier)}</span>
										{/if}
									</div>
									<div class="resp-meta">
										{#if resp.reputation?.sessions_completed > 0}
											<span>{sessionsLabel(resp.reputation.sessions_completed)}</span>
										{/if}
										{#if ratingStr(resp.reputation)}
											<span>· {ratingStr(resp.reputation)}</span>
										{/if}
										{#if memberSinceStr(resp.reputation?.member_since)}
											<span>· {memberSinceStr(resp.reputation?.member_since)}</span>
										{/if}
									</div>
								</div>
								<button
									class="btn-accept"
									disabled={acceptLoading}
									onclick={() => acceptResponse(resp.id)}
								>
									{acceptLoading ? '...' : t('listing.accept')}
								</button>
							</div>
						{/each}
					</div>
					{#if acceptError}<div class="error">{acceptError}</div>{/if}
					<p class="fine">{t('listing.accept_fine')}</p>
				{/if}

			{:else}
				<!-- Enter wallet to unlock responses -->
				<p class="section-desc">{t('listing.enter_wallet_desc')}</p>
				<div class="sub-form">
					<div class="field">
						<label for="client-wallet">{t('listing.your_wallet')}</label>
						<input id="client-wallet" type="text" placeholder={t('listing.btc_ltc_ph')} bind:value={clientWallet} />
					</div>
					{#if responsesError}<div class="error">{responsesError}</div>{/if}
					<button class="btn-primary" disabled={!clientWallet || loadingResponses} onclick={loadResponses}>
						{loadingResponses ? t('listing.loading') : t('listing.view_responses')}
					</button>
				</div>
			{/if}
		</div>

	<!-- ── OWNER TELEGRAM SECTION (shown only to authenticated owner of active listing) ── -->
	{#if ownerTgState !== 'idle' && ownerTgState !== 'not_owner' && ownerTgState !== 'checking'}
	<div class="section">
		<div class="section-title">{t('listing.tg_section')}</div>

		{#if ownerTgState === 'connected'}
			<div class="tg-connected">
				<span class="tg-check">✓</span>
				<div>
					<div class="tg-connected-title">{t('listing.tg_connected')}</div>
					<div class="tg-connected-sub">{t('listing.tg_connected_sub')}</div>
				</div>
			</div>
		{:else if ownerTgState === 'disconnected' || ownerTgState === 'error'}
			<p class="section-desc">{t('listing.tg_connect_body')}</p>
			{#if ownerTgError}<div class="error">{ownerTgError}</div>{/if}
			<button class="btn-primary" onclick={connectTelegram}>{t('listing.tg_connect')}</button>
		{:else if ownerTgState === 'connecting' && ownerTgBotUrl}
			<p class="section-desc">{t('listing.tg_connect_body')}</p>
			<a class="btn-primary" href={ownerTgBotUrl} target="_blank" rel="noopener noreferrer">{t('listing.tg_open_bot')}</a>
			<div class="poll-row">
				<span class="dot"></span>
				{t('listing.tg_waiting')}
			</div>
		{:else if ownerTgState === 'expired'}
			<p class="section-desc">{t('listing.tg_expired')}</p>
			<button class="btn-primary" onclick={connectTelegram}>{t('listing.tg_retry')}</button>
		{/if}
	</div>
	{/if}

	{:else if listing.status === 'expired'}
		<!-- Expired listing: show owner wallet form to unlock renewal, or a note for others -->
		{#if listing.can_renew}
			<div class="section">
				<div class="section-title">{t('listing.i_posted')}</div>
				{#if myResponses !== null}
					<p class="section-desc">{t('listing.chats_used', {n: listing.opened_chats_count ?? 0})}</p>
				{:else}
					<p class="section-desc">{t('listing.expired_owner_desc')}</p>
					<div class="sub-form">
						<div class="field">
							<label for="client-wallet">{t('listing.your_wallet')}</label>
							<input id="client-wallet" type="text" placeholder={t('listing.btc_ltc_ph')} bind:value={clientWallet} />
						</div>
						{#if responsesError}<div class="error">{responsesError}</div>{/if}
						<button class="btn-primary" disabled={!clientWallet || loadingResponses} onclick={loadResponses}>
							{loadingResponses ? t('listing.loading') : t('listing.view_responses')}
						</button>
					</div>
				{/if}
			</div>
		{:else if listing.opened_chats_count >= 2}
			<div class="expired-note">{t('listing.fully_used')}</div>
		{:else}
			<div class="expired-note">{t('listing.expired_note')}</div>
		{/if}
	{:else}
		<div class="expired-note">{t('listing.expired_note')}</div>
	{/if}

	<!-- ── RECOVERY GATE (shown when a new session was just created for peer or client) ── -->
	{#if peerShowRecovery || clientShowRecovery}
	<div class="recovery-overlay">
		<div class="recovery-card">
			<h3 style="margin:0;font-size:17px;font-weight:600;color:var(--text)">Save your recovery code</h3>
			<p style="font-size:13px;color:var(--text-dim);line-height:1.6;margin:0">
				This is the only way to restore access if you close the browser. Write it down — it will not be shown again.
			</p>
			<div class="recovery-code-box">
				{peerShowRecovery ? peerRecoveryCode : clientRecoveryCode}
			</div>
			<button class="btn-primary" onclick={() => peerShowRecovery ? peerPendingFn?.() : clientPendingFn?.()}>
				I saved it — continue →
			</button>
		</div>
	</div>
	{/if}

	<!-- ── RENEW SECTION (owner only, expiring/expired, < 2 responses) ── -->
	{#if showRenew}
	<div class="section renew-section">
		<div class="section-title">{t('listing.extend')}</div>
		{#if renewDone}
			<p class="section-desc">{t('listing.renew_done')}</p>
		{:else}
			<p class="section-desc">
				{listing.status === 'expired' ? t('listing.has_expired') : t('listing.expires_in', {time: timeLeft(listing.time_left)})}
				{t('listing.renew_free_hint')}
			</p>
			{#if renewError}<div class="error">{renewError}</div>{/if}
			<button class="btn-primary" disabled={renewLoading} onclick={startRenew}>
				{renewLoading ? '...' : t('listing.renew_free')}
			</button>
			<p class="fine">{t('listing.renew_fine2', {n: (listing.renewal_count ?? 0) + 1})}</p>
		{/if}
	</div>
	{/if}
</div>

<style>
	.page {
		max-width: 560px;
		margin: 0 auto;
		padding: 0 16px 60px;
	}
	header { padding: 20px 0 20px; }
	.back { color: var(--text-dim); font-size: 13px; }
	.back:hover { color: var(--text); }

	.urgency-bar { height: 3px; border-radius: 2px; margin-bottom: 20px; }

	.sample-banner {
		font-size: 12px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 1px;
		color: var(--accent);
		border: 1px solid var(--accent);
		border-radius: 6px;
		padding: 8px 14px;
		text-align: center;
		margin-bottom: 16px;
	}

	/* Listing card */
	.listing-card {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 12px;
		padding: 20px;
		display: flex;
		flex-direction: column;
		gap: 16px;
		margin-bottom: 28px;
	}
	.listing-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
	}
	.dep { font-size: 20px; font-weight: 600; color: var(--text); margin-bottom: 4px; }
	.help { font-size: 14px; color: var(--text-dim); }
	.urgency-tag {
		font-size: 11px; font-weight: 700; text-transform: uppercase;
		letter-spacing: 0.8px; padding: 4px 10px; border-radius: 20px;
		border: 1px solid; white-space: nowrap;
	}
	.listing-meta { display: flex; gap: 20px; flex-wrap: wrap; }
	.meta-item { display: flex; flex-direction: column; gap: 2px; }
	.meta-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px; color: var(--text-faint); }
	.meta-value { font-size: 13px; color: var(--text); font-weight: 500; }

	/* Sections */
	.section {
		border: 1px solid var(--border);
		border-radius: 12px;
		padding: 20px;
		display: flex;
		flex-direction: column;
		gap: 14px;
		margin-bottom: 16px;
	}
	.section-title {
		font-size: 12px; font-weight: 600; text-transform: uppercase;
		letter-spacing: 0.8px; color: var(--text-faint);
	}
	.section-desc { font-size: 14px; color: var(--text-dim); line-height: 1.6; }

	/* Sub-form */
	.sub-form { display: flex; flex-direction: column; gap: 14px; }
	.field { display: flex; flex-direction: column; gap: 8px; }
	label, .field-label { font-size: 13px; font-weight: 500; color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.5px; }
	.wallet-row { display: flex; gap: 8px; }
	input {
		flex: 1; background: var(--bg-card); border: 1px solid var(--border);
		border-radius: 8px; padding: 10px 14px; color: var(--text); font-size: 13px;
		font-family: monospace; outline: none; transition: border-color 0.15s;
	}
	input:focus { border-color: var(--accent); }
	input::placeholder { color: var(--text-faint); }
	.currency-toggle {
		display: flex; border: 1px solid var(--border); border-radius: 8px; overflow: hidden;
	}
	.currency-toggle button {
		padding: 0 14px; background: var(--bg-card); color: var(--text-dim);
		font-size: 12px; font-weight: 600; transition: all 0.15s;
	}
	.currency-toggle button.active { background: var(--accent); color: var(--bg); }
	.form-actions { display: flex; gap: 12px; }
	.fine { font-size: 12px; color: var(--text-faint); text-align: center; }
	.error {
		background: rgba(212, 132, 90, 0.12); border: 1px solid var(--danger);
		border-radius: 8px; padding: 10px 14px; color: var(--danger); font-size: 13px;
	}

	/* Buttons */
	.btn-primary {
		padding: 12px 22px; background: var(--accent); color: var(--bg);
		border-radius: 10px; font-size: 14px; font-weight: 600;
		transition: opacity 0.15s; align-self: flex-start;
	}
	.btn-primary:disabled { opacity: 0.4; cursor: not-allowed; }
	.btn-primary:not(:disabled):hover { opacity: 0.85; }
	.btn-ghost {
		padding: 12px 18px; border: 1px solid var(--border);
		border-radius: 10px; color: var(--text-dim); font-size: 14px; transition: all 0.15s;
	}
	.btn-ghost:hover { border-color: var(--text-faint); color: var(--text); }

	/* Success */
	.success-box {
		background: rgba(123, 166, 142, 0.1); border: 1px solid var(--accent);
		border-radius: 10px; padding: 16px; display: flex; gap: 14px; align-items: flex-start;
	}
	.success-icon { font-size: 20px; color: var(--accent); flex-shrink: 0; }
	.success-title { font-size: 15px; font-weight: 600; color: var(--text); margin-bottom: 4px; }
	.success-sub { font-size: 13px; color: var(--text-dim); }

	/* Responses list */
	.responses-list { display: flex; flex-direction: column; gap: 8px; }
	.response-card {
		display: flex; align-items: center; gap: 10px;
		background: var(--bg-card); border: 1px solid var(--border);
		border-radius: 8px; padding: 12px 14px;
	}
	.response-card.new-counselor { border-color: rgba(212, 180, 90, 0.4); }
	.resp-main { flex: 1; display: flex; flex-direction: column; gap: 4px; }
	.resp-header { display: flex; align-items: center; gap: 6px; }
	.resp-label { font-size: 14px; font-weight: 600; color: var(--text); }
	.resp-meta { font-size: 12px; color: var(--text-faint); display: flex; gap: 4px; flex-wrap: wrap; }
	.badge-new {
		font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.5px;
		color: var(--warn); border: 1px solid var(--warn); border-radius: 4px; padding: 1px 5px;
	}
	.badge-tier {
		font-size: 11px; font-weight: 700; color: var(--accent);
		letter-spacing: 1px;
	}
	.btn-accept {
		padding: 6px 16px; background: var(--accent); color: var(--bg);
		border-radius: 6px; font-size: 13px; font-weight: 600; transition: opacity 0.15s;
	}
	.btn-accept:disabled { opacity: 0.4; cursor: not-allowed; }
	.btn-accept:not(:disabled):hover { opacity: 0.85; }
	.empty-responses { font-size: 13px; color: var(--text-faint); text-align: center; padding: 12px 0; }

	/* Invoice waiting */
	.invoice-box {
		display: flex; gap: 14px; align-items: flex-start;
		background: var(--bg-card); border: 1px solid var(--border);
		border-radius: 10px; padding: 16px;
	}
	.invoice-icon { font-size: 22px; flex-shrink: 0; }
	.invoice-title { font-size: 15px; font-weight: 600; color: var(--text); margin-bottom: 4px; }
	.invoice-sub { font-size: 13px; color: var(--text-dim); line-height: 1.5; }
	.invoice-box--pay {
		display: block;
	}
	.invoice-amount {
		font-size: 22px; font-weight: 700; color: var(--accent);
		letter-spacing: 0.02em; margin-bottom: 10px;
	}
	.invoice-addr-row {
		display: flex; flex-direction: column; gap: 8px;
	}
	.invoice-addr {
		font-family: monospace; font-size: 13px; color: var(--text-dim);
		word-break: break-all; width: 100%;
	}
	.copy-btn {
		align-self: flex-start; padding: 6px 14px; font-size: 12px; font-weight: 600;
		background: var(--bg-card2, var(--bg-card)); border: 1px solid var(--border);
		border-radius: 6px; color: var(--accent); cursor: pointer; white-space: nowrap;
		transition: background 0.15s;
	}
	.copy-btn:hover { background: var(--bg-hover, rgba(255,255,255,0.06)); }
	.poll-row {
		display: flex; align-items: center; gap: 8px;
		color: var(--text-dim); font-size: 13px;
	}
	.dot {
		width: 8px; height: 8px; border-radius: 50%;
		background: var(--accent); animation: pulse 1.5s infinite; flex-shrink: 0;
	}
	@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }

	.balance-warn {
		font-size: 12px; color: var(--warn); background: rgba(212,180,90,0.08);
		border: 1px solid rgba(212,180,90,0.3); border-radius: 8px;
		padding: 10px 14px; line-height: 1.5;
	}

	.expired-note { font-size: 14px; color: var(--text-faint); text-align: center; padding: 40px 0; }

	/* Region lock */
	.region-warning {
		background: rgba(212, 180, 90, 0.08); border: 1px solid var(--warn);
		border-radius: 10px; padding: 16px; display: flex; flex-direction: column; gap: 12px;
	}
	.region-warning-title { font-size: 14px; font-weight: 600; color: var(--warn); }
	.region-warning-body  { font-size: 13px; color: var(--text-dim); line-height: 1.6; margin: 0; }
	.region-blocked {
		background: rgba(212, 132, 90, 0.08); border: 1px solid var(--danger);
		border-radius: 8px; padding: 12px 14px; font-size: 13px; color: var(--danger);
	}

	/* Recovery gate overlay */
	.recovery-overlay {
		position: fixed; inset: 0; background: rgba(0,0,0,0.65);
		display: flex; align-items: center; justify-content: center;
		z-index: 200; backdrop-filter: blur(4px);
	}
	.recovery-card {
		background: var(--bg-card); border: 1px solid var(--accent);
		border-radius: 14px; padding: 28px; width: 100%; max-width: 420px;
		display: flex; flex-direction: column; gap: 16px; margin: 16px;
	}
	.recovery-code-box {
		background: var(--bg-card2, #1a1a2e); border: 1px solid var(--accent);
		border-radius: 8px; padding: 14px; font-family: monospace;
		font-size: 12px; word-break: break-all; color: var(--text); line-height: 1.6;
	}

	/* Owner Telegram section */
	.tg-connected {
		display: flex; gap: 14px; align-items: center;
		background: rgba(123, 166, 142, 0.1); border: 1px solid var(--accent);
		border-radius: 10px; padding: 16px;
	}
	.tg-check { font-size: 20px; color: var(--accent); flex-shrink: 0; }
	.tg-connected-title { font-size: 15px; font-weight: 600; color: var(--text); margin-bottom: 4px; }
	.tg-connected-sub   { font-size: 13px; color: var(--text-dim); }

	/* Renew section */
	.renew-section { border-color: var(--warn); margin-top: 4px; }
	.address-box {
		background: var(--bg-card); border: 1px solid var(--border);
		border-radius: 8px; padding: 14px 16px; font-family: monospace;
		font-size: 13px; word-break: break-all; color: var(--text);
	}
</style>
