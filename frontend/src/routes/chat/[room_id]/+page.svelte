<script>
	import { page } from '$app/stores';
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import nacl from 'tweetnacl';
	import { lang, t as tFn } from '$lib/i18n.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	// ── Room & key setup ──────────────────────────────────────────────────
	const roomId = $page.params.room_id;
	// Session token — try client first, then peer
	const sessionToken = sessionStorage.getItem('naroom_session_client')
		?? sessionStorage.getItem('naroom_session_peer')
		?? '';
	// myPubkeyHex is resolved from room metadata after loadRoom(); populated via $state
	let myPubkeyHex = $state(sessionStorage.getItem('room_pubkey_' + roomId) ?? '');

	// Convert hex to Uint8Array
	function hexToBytes(hex) {
		const arr = new Uint8Array(hex.length / 2);
		for (let i = 0; i < arr.length; i++) arr[i] = parseInt(hex.slice(i*2, i*2+2), 16);
		return arr;
	}
	function bytesToHex(bytes) {
		return Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
	}

	// Recall or generate keypair for this session
	// Private key stored separately per listing to survive page reload
	function getMyKeypair() {
		const privKey = sessionStorage.getItem('privkey_' + roomId)
			?? sessionStorage.getItem('client_privkey')
			?? sessionStorage.getItem('peer_privkey');

		if (privKey) {
			const sk = hexToBytes(privKey);
			const kp = nacl.box.keyPair.fromSecretKey(sk);
			return kp;
		}
		// Fallback: generate ephemeral keypair (pubkey won't match — user navigated directly)
		const kp = nacl.box.keyPair();
		sessionStorage.setItem('privkey_' + roomId, bytesToHex(kp.secretKey));
		return kp;
	}

	// ── State ─────────────────────────────────────────────────────────────
	let room      = $state(null);       // room metadata from API
	let messages  = $state([]);         // { id, from: 'me'|'them', text, ts }
	let inputText = $state('');
	let loading   = $state(true);
	let error     = $state('');
	let closed    = $state(false);
	let peerLeft  = $state(false); // peer closed their side, client can still close
	let reviewToken = $state('');
	let timeLeftSec = $state(0);
	let closing   = $state(false);
	let showCloseModal = $state(false);

	let ws;
	let keypair;
	let sharedKey;
	let timerInterval;
	let wsReconnectTimer = null;

	// ── Lifecycle ─────────────────────────────────────────────────────────
	onMount(async () => {
		try {
			await loadRoom();
		} catch(e) {
			error = e.message;
			loading = false;
		}
	});

	onDestroy(() => {
		ws?.close();
		clearInterval(timerInterval);
		clearTimeout(wsReconnectTimer);
	});

	function chatHeaders() {
		return {
			'Content-Type': 'application/json',
			...(sessionToken ? { 'Authorization': `Bearer ${sessionToken}` } : {}),
		};
	}

	async function loadRoom() {
		const res = await fetch(`/api/chat/${roomId}`, { headers: chatHeaders() });
		if (!res.ok) throw new Error(t('chat.room_not_found'));
		room = await res.json();

		// Server returns my_pubkey based on session — store it for reconnects and WS
		if (room.my_pubkey) {
			myPubkeyHex = room.my_pubkey;
			sessionStorage.setItem('room_pubkey_' + roomId, room.my_pubkey);
		}

		if (room.status === 'closed' || room.status === 'expired') {
			closed = true; loading = false; return;
		}
		if (room.status === 'peer_left' && room.role === 'client') {
			peerLeft = true;
		}

		timeLeftSec = room.expires_at - Math.floor(Date.now() / 1000);
		timerInterval = setInterval(() => {
			timeLeftSec = room.expires_at - Math.floor(Date.now() / 1000);
			if (timeLeftSec <= 0) { clearInterval(timerInterval); }
		}, 1000);

		// Setup encryption
		keypair = getMyKeypair();
		const peerPubkey = hexToBytes(room.peer_pubkey);
		sharedKey = nacl.box.before(peerPubkey, keypair.secretKey);

		// Connect WebSocket
		connectWS();
		loading = false;
	}

	function connectWS() {
		if (closed) return;
		const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
		const host = location.host;
		const url = `${proto}//${host}/ws/chat/ws?room_id=${roomId}`;
		// Pass token as Sec-WebSocket-Protocol — only way browser WS API can send auth material.
		// Backend echoes it back as accepted subprotocol so the handshake completes.
		ws = new WebSocket(url, sessionToken ? [sessionToken] : []);

		ws.onopen = () => {
			// Clear any reconnect error once connected
			if (error === 'Reconnecting...') error = '';
		};

		ws.onmessage = (event) => {
			try {
				const data = JSON.parse(event.data);

				// System event (not encrypted chat message)
				if (data.type === 'system') {
					if (data.event === 'peer_left' && room?.role === 'client') {
						peerLeft = true;
					} else if (data.event === 'room_closed') {
						closed = true;
					}
					return;
				}

				// Skip own messages — already added optimistically in sendMessage/sendImage
				if (data.sender_pubkey === myPubkeyHex) return;
				const decrypted = decryptMsg(data.nonce, data.ciphertext);
				if (decrypted === null) return;
				messages = [...messages, {
					id: data.id,
					from: 'them',
					text: decrypted,
					msgType: data.msg_type ?? 'text',
					ts: data.created_at,
				}];
				scrollToBottom();
			} catch {}
		};

		ws.onerror = () => {};

		ws.onclose = async () => {
			if (closed) return;
			// Check if room was closed by the other side before reconnecting
			try {
				const r = await fetch(`/api/chat/${roomId}`, { headers: chatHeaders() });
				if (r.ok) {
					const d = await r.json();
					if (d.status === 'closed') {
						closed = true;
						return;
					}
				}
			} catch {}
			// Auto-reconnect after 2s
			error = t('chat.reconnecting');
			wsReconnectTimer = setTimeout(() => {
				error = '';
				connectWS();
			}, 2000);
		};
	}

	function encryptMsg(text) {
		const nonce = nacl.randomBytes(nacl.box.nonceLength);
		const encoded = new TextEncoder().encode(text);
		const box = nacl.box.after(encoded, nonce, sharedKey);
		return { nonce: bytesToHex(nonce), ciphertext: bytesToHex(box) };
	}

	function decryptMsg(nonceHex, ciphertextHex) {
		try {
			const nonce = hexToBytes(nonceHex);
			const box   = hexToBytes(ciphertextHex);
			const plain = nacl.box.open.after(box, nonce, sharedKey);
			if (!plain) return null;
			return new TextDecoder().decode(plain);
		} catch { return null; }
	}

	function sendMessage() {
		const text = inputText.trim();
		if (!text || !ws || ws.readyState !== WebSocket.OPEN) return;
		const { nonce, ciphertext } = encryptMsg(text);
		ws.send(JSON.stringify({ nonce, ciphertext, msg_type: 'text' }));
		inputText = '';
		messages = [...messages, {
			id: 'local_' + Date.now(),
			from: 'me',
			text,
			msgType: 'text',
			ts: Math.floor(Date.now() / 1000),
		}];
		scrollToBottom();
	}

	// ── Image / Camera ────────────────────────────────────────────────────
	let fileInput;
	let showCamera   = $state(false);
	let cameraError  = $state('');
	let videoEl      = null;        // DOM ref — не $state, bind:this сам управляет
	let cameraStream = null;

	function compressImage(source, maxPx = 900, quality = 0.65) {
		return new Promise((resolve) => {
			const isDataUrl = typeof source === 'string';
			const img = new Image();
			const process = () => {
				const scale = Math.min(1, maxPx / Math.max(img.width, img.height));
				const w = Math.round(img.width * scale);
				const h = Math.round(img.height * scale);
				const canvas = document.createElement('canvas');
				canvas.width = w; canvas.height = h;
				canvas.getContext('2d').drawImage(img, 0, 0, w, h);
				resolve(canvas.toDataURL('image/jpeg', quality));
			};
			img.onload = process;
			if (isDataUrl) {
				img.src = source;
			} else {
				const url = URL.createObjectURL(source);
				img.onload = () => { URL.revokeObjectURL(url); process(); };
				img.src = url;
			}
		});
	}

	async function sendImage(source, msgType) {
		if (!source || !ws || ws.readyState !== WebSocket.OPEN) return;
		const dataUrl = await compressImage(source);
		const { nonce, ciphertext } = encryptMsg(dataUrl);
		ws.send(JSON.stringify({ nonce, ciphertext, msg_type: msgType }));
		messages = [...messages, {
			id: 'local_' + Date.now(),
			from: 'me',
			text: dataUrl,
			msgType,
			ts: Math.floor(Date.now() / 1000),
		}];
		scrollToBottom();
	}

	function onFileChange(e) {
		const file = e.target.files?.[0];
		if (file) sendImage(file, 'image_file');
		e.target.value = '';
	}

	async function openCamera() {
		cameraError = '';
		showCamera = true;
		try {
			cameraStream = await navigator.mediaDevices.getUserMedia({ video: true, audio: false });
			await new Promise(r => setTimeout(r, 50));
			if (videoEl) { videoEl.srcObject = cameraStream; videoEl.play(); }
		} catch(e) {
			cameraError = e.name === 'NotAllowedError'
				? t('chat.camera_denied')
				: t('chat.camera_unavail', {msg: e.message});
		}
	}

	function closeCamera() {
		cameraStream?.getTracks().forEach(t => t.stop());
		cameraStream = null;
		showCamera = false;
	}

	function takePhoto() {
		if (!videoEl) return;
		const canvas = document.createElement('canvas');
		canvas.width = videoEl.videoWidth;
		canvas.height = videoEl.videoHeight;
		canvas.getContext('2d').drawImage(videoEl, 0, 0);
		const dataUrl = canvas.toDataURL('image/jpeg', 0.85);
		closeCamera();
		sendImage(dataUrl, 'image_camera');
	}

	function handleKeydown(e) {
		if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendMessage(); }
	}

	async function closeChat() {
		showCloseModal = true;
	}

	async function confirmClose() {
		showCloseModal = false;
		closing = true;
		try {
			const res = await fetch(`/api/chat/${roomId}/close`, {
				method: 'POST',
				headers: chatHeaders(),
				body: JSON.stringify({}),
			});
			const data = await res.json();
			closed = true;
			if (data.review_token) reviewToken = data.review_token;
		} catch(e) { error = e.message; }
		finally { closing = false; }
	}

	function scrollToBottom() {
		setTimeout(() => {
			const el = document.getElementById('msg-list');
			if (el) el.scrollTop = el.scrollHeight;
		}, 30);
	}

	function formatTime(ts) {
		return new Date(ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	}

	function formatTimeLeft(s) {
		if (s <= 0) return t('time.expired');
		const h = Math.floor(s / 3600);
		const m = Math.floor((s % 3600) / 60);
		if (h > 0) return t('time.h_m', {h, m});
		return t('time.m_s', {m, s: s % 60});
	}

	// ── Review state ──────────────────────────────────────────────────────
	let reviewScore = $state(0);
	let reviewNote  = $state('');
	let reviewDone  = $state(false);
	let reviewErr   = $state('');

	async function submitReview(thumbs) {
		reviewScore = thumbs;
		try {
			const res = await fetch('/api/review', {
				method: 'POST', headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ token: reviewToken, rating: thumbs > 0 ? 'up' : 'down' }),
			});
			if (!res.ok) throw new Error((await res.json()).error ?? 'Review failed');
			reviewDone = true;
		} catch(e) { reviewErr = e.message; }
	}
</script>

<div class="page">
	{#if loading}
		<div class="center"><div class="dot-pulse"></div></div>

	{:else if closed}
		<!-- Session ended -->
		<div class="closed-screen">
			<div class="closed-icon">✓</div>
			<h2>{t('chat.session_ended')}</h2>
			<p>{t('chat.messages_deleted')}</p>

			{#if reviewToken && !reviewDone && room?.role === 'client'}
				<div class="review-box">
					<div class="review-title">{t('chat.how_was')}</div>
					<div class="thumb-row">
						<button class="thumb" class:active={reviewScore === 1} onclick={() => submitReview(1)}>👍</button>
						<button class="thumb" class:active={reviewScore === -1} onclick={() => submitReview(-1)}>👎</button>
					</div>
					{#if reviewErr}<p class="err-small">{reviewErr}</p>{/if}
				</div>
			{:else if reviewDone}
				<div class="review-done">{t('chat.thanks_feedback')}</div>
			{/if}

			<a href="/" class="btn-primary" style="margin-top: 24px">{t('chat.back_to_board')}</a>
		</div>

	{:else}
		<!-- Chat header -->
		<header>
			<div class="header-left">
				<div class="room-label">{t('chat.anon_session')}</div>
				<div class="role-tag">{room?.role === 'client' ? t('chat.role_client') : t('chat.role_peer')}</div>
			</div>
			<div class="header-right">
				<div class="timer" class:urgent={timeLeftSec < 3600}>
					{formatTimeLeft(timeLeftSec)}
				</div>
				<button class="btn-close" onclick={closeChat} disabled={closing}>
					{closing ? '...' : t('chat.end')}
				</button>
			</div>
		</header>

		<div class="e2e-badge">{t('chat.e2e')}</div>
		{#if error}
		<div class="reconnect-banner">{error}</div>
		{/if}
		{#if peerLeft && room?.role === 'client'}
		<div class="peer-left-banner">{t('chat.peer_left')}</div>
		{/if}

		<!-- Messages -->
		<div class="msg-list" id="msg-list">
			{#if messages.length === 0}
				<div class="empty-chat">
					{room?.role === 'client' ? t('chat.empty_client') : t('chat.empty_peer')}
				</div>
			{/if}
			{#each messages as msg (msg.id)}
				<div class="msg-wrap" class:me={msg.from === 'me'}>
					<div class="msg-bubble" class:img-bubble={msg.msgType !== 'text'}>
						{#if msg.msgType === 'text'}
							{msg.text}
						{:else}
							{#if msg.msgType === 'image_camera'}
								<div class="live-badge">{t('chat.live_photo')}</div>
							{/if}
							<img src={msg.text} alt="image" class="chat-img" />
						{/if}
						<span class="msg-time">{formatTime(msg.ts)}</span>
					</div>
				</div>
			{/each}
		</div>

		<!-- Close confirmation modal -->
		{#if showCloseModal}
		<div class="modal-overlay" role="dialog" aria-modal="true">
			<div class="modal">
				<div class="modal-title">{t('chat.end_title')}</div>
				<p class="modal-body">{t('chat.end_body')}</p>
				<div class="modal-actions">
					<button class="modal-cancel" onclick={() => showCloseModal = false}>{t('cancel')}</button>
					<button class="modal-confirm" onclick={confirmClose}>{t('chat.end_confirm')}</button>
				</div>
			</div>
		</div>
		{/if}

		<!-- Camera modal -->
		{#if showCamera}
		<div class="modal-overlay">
			<div class="camera-modal">
				{#if cameraError}
					<div class="camera-err">{cameraError}</div>
				{:else}
					<video bind:this={videoEl} class="camera-preview" autoplay playsinline muted></video>
				{/if}
				<div class="camera-actions">
					<button class="modal-cancel" onclick={closeCamera}>Cancel</button>
					{#if !cameraError}
					<button class="camera-shoot" onclick={takePhoto}>{t('chat.take_photo')}</button>
					{/if}
				</div>
			</div>
		</div>
		{/if}

		<!-- Input -->
		<div class="input-row" class:input-disabled={peerLeft}>
			<input type="file" accept="image/*" class="hidden-input" bind:this={fileInput} onchange={onFileChange} disabled={peerLeft} />

			<button class="attach-btn" onclick={() => fileInput.click()} title="Attach image" disabled={peerLeft}>📎</button>
			<button class="attach-btn" onclick={openCamera} title="Take photo" disabled={peerLeft}>📷</button>

			<textarea
				placeholder={peerLeft ? t('chat.peer_left_ph') : t('chat.type_message')}
				bind:value={inputText}
				onkeydown={handleKeydown}
				rows="1"
				disabled={peerLeft}
			></textarea>
			<button class="send-btn" onclick={sendMessage} disabled={!inputText.trim() || peerLeft}>↑</button>
		</div>
	{/if}
</div>

<style>
	:global(body) { overflow: hidden; }

	.page {
		max-width: 640px;
		margin: 0 auto;
		height: 100vh;
		display: flex;
		flex-direction: column;
		padding: 0;
	}

	/* Loading / error / closed */
	.center {
		display: flex; flex-direction: column; align-items: center; justify-content: center;
		flex: 1; gap: 16px; padding: 40px;
	}
	.dot-pulse {
		width: 12px; height: 12px; border-radius: 50%;
		background: var(--accent); animation: pulse 1.2s infinite;
	}
	@keyframes pulse { 0%, 100% { opacity: 1; transform: scale(1); } 50% { opacity: 0.3; transform: scale(0.8); } }
	.err-icon { font-size: 32px; color: var(--danger); }
	.err-msg { color: var(--danger); font-size: 14px; text-align: center; }
	.err-small { color: var(--danger); font-size: 12px; margin-top: 4px; }

	/* Closed screen */
	.closed-screen {
		display: flex; flex-direction: column; align-items: center;
		justify-content: center; flex: 1; gap: 14px; padding: 40px; text-align: center;
	}
	.closed-icon { font-size: 40px; color: var(--accent); }
	h2 { font-size: 22px; font-weight: 600; color: var(--text); }
	.closed-screen p { font-size: 14px; color: var(--text-dim); max-width: 320px; }

	/* Review */
	.review-box {
		background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px;
		padding: 20px; display: flex; flex-direction: column; align-items: center; gap: 14px; width: 100%; max-width: 300px;
	}
	.review-title { font-size: 14px; font-weight: 500; color: var(--text); }
	.thumb-row { display: flex; gap: 20px; }
	.thumb {
		font-size: 28px; padding: 8px 16px; border-radius: 10px; border: 1px solid var(--border);
		background: var(--bg-card); transition: all 0.15s;
	}
	.thumb:hover, .thumb.active { background: var(--bg-hover); border-color: var(--accent); }
	.review-done { font-size: 13px; color: var(--accent); }

	/* Header */
	header {
		display: flex; align-items: center; justify-content: space-between;
		padding: 14px 16px; border-bottom: 1px solid var(--border); flex-shrink: 0;
	}
	.header-left { display: flex; flex-direction: column; gap: 2px; }
	.room-label { font-size: 14px; font-weight: 600; color: var(--text); }
	.role-tag { font-size: 12px; color: var(--text-faint); }
	.header-right { display: flex; align-items: center; gap: 12px; }
	.timer {
		font-size: 13px; font-weight: 600; color: var(--text-dim);
		font-variant-numeric: tabular-nums;
	}
	.timer.urgent { color: var(--danger); }
	.btn-close {
		padding: 6px 14px; border: 1px solid var(--border); border-radius: 8px;
		color: var(--text-dim); font-size: 13px; transition: all 0.15s;
	}
	.btn-close:hover { border-color: var(--danger); color: var(--danger); }

	/* E2E badge */
	.e2e-badge {
		text-align: center; font-size: 11px; color: var(--text-faint);
		padding: 6px; border-bottom: 1px solid var(--border); flex-shrink: 0;
	}
	.reconnect-banner {
		text-align: center; font-size: 12px; color: var(--warn);
		padding: 4px; background: rgba(212,180,90,0.08); flex-shrink: 0;
	}
	.peer-left-banner {
		text-align: center; font-size: 13px; color: var(--text-dim);
		padding: 10px 16px; background: rgba(212,180,90,0.06);
		border-bottom: 1px solid var(--border); flex-shrink: 0; line-height: 1.5;
	}
	.input-disabled .attach-btn,
	.input-disabled textarea {
		opacity: 0.4; cursor: not-allowed;
	}

	/* Messages */
	.msg-list {
		flex: 1; overflow-y: auto; padding: 16px;
		display: flex; flex-direction: column; gap: 8px;
		scrollbar-width: thin; scrollbar-color: var(--border) transparent;
	}
	.empty-chat {
		flex: 1; display: flex; align-items: center; justify-content: center;
		font-size: 13px; color: var(--text-faint); text-align: center;
		padding: 40px; line-height: 1.6;
	}
	.msg-wrap { display: flex; }
	.msg-wrap.me { justify-content: flex-end; }
	.msg-bubble {
		max-width: 70%; background: var(--bg-card); border: 1px solid var(--border);
		border-radius: 12px; padding: 10px 14px; font-size: 14px; line-height: 1.5;
		color: var(--text); white-space: pre-wrap; word-break: break-word;
		position: relative;
	}
	.msg-wrap.me .msg-bubble {
		background: rgba(123, 166, 142, 0.15);
		border-color: rgba(123, 166, 142, 0.3);
	}
	.msg-time {
		display: block; font-size: 10px; color: var(--text-faint);
		text-align: right; margin-top: 4px;
	}

	/* Images in chat */
	.img-bubble {
		padding: 6px !important;
		max-width: 80% !important;
	}
	.chat-img {
		display: block; max-width: 100%; border-radius: 8px;
		cursor: pointer;
	}
	.live-badge {
		font-size: 11px; color: var(--accent); font-weight: 600;
		margin-bottom: 4px; padding: 0 2px;
	}

	/* Input */
	.hidden-input { display: none; }
	.input-row {
		display: flex; gap: 6px; padding: 12px 16px;
		border-top: 1px solid var(--border); flex-shrink: 0;
		align-items: flex-end;
	}
	.attach-btn {
		width: 38px; height: 38px; flex-shrink: 0;
		border: 1px solid var(--border); border-radius: 8px;
		background: var(--bg-card); font-size: 16px;
		transition: border-color 0.15s;
		display: flex; align-items: center; justify-content: center;
	}
	.attach-btn:hover { border-color: var(--accent); }
	textarea {
		flex: 1; background: var(--bg-card); border: 1px solid var(--border);
		border-radius: 10px; padding: 10px 14px; color: var(--text); font-size: 14px;
		font-family: inherit; outline: none; resize: none; line-height: 1.5;
		transition: border-color 0.15s; max-height: 120px; overflow-y: auto;
	}
	textarea:focus { border-color: var(--accent); }
	textarea::placeholder { color: var(--text-faint); }
	.send-btn {
		width: 42px; height: 42px; align-self: flex-end; background: var(--accent);
		color: var(--bg); border-radius: 10px; font-size: 18px; font-weight: bold;
		transition: opacity 0.15s; flex-shrink: 0;
	}
	.send-btn:disabled { opacity: 0.3; cursor: not-allowed; }
	.send-btn:not(:disabled):hover { opacity: 0.85; }

	/* Close modal */
	.modal-overlay {
		position: fixed; inset: 0;
		background: rgba(0, 0, 0, 0.6);
		display: flex; align-items: center; justify-content: center;
		z-index: 100;
		backdrop-filter: blur(4px);
	}
	.modal {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 14px;
		padding: 24px;
		width: 300px;
		display: flex; flex-direction: column; gap: 14px;
	}
	.modal-title {
		font-size: 16px; font-weight: 600; color: var(--text);
	}
	.modal-body {
		font-size: 13px; color: var(--text-dim); line-height: 1.5;
	}
	.modal-actions {
		display: flex; gap: 10px; justify-content: flex-end; margin-top: 4px;
	}
	.modal-cancel {
		padding: 8px 18px; border: 1px solid var(--border);
		border-radius: 8px; color: var(--text-dim); font-size: 13px;
		transition: all 0.15s;
	}
	.modal-cancel:hover { border-color: var(--text-faint); color: var(--text); }
	.modal-confirm {
		padding: 8px 18px; background: var(--danger);
		border-radius: 8px; color: #fff; font-size: 13px; font-weight: 600;
		transition: opacity 0.15s;
	}
	.modal-confirm:hover { opacity: 0.85; }

	/* Camera modal */
	.camera-modal {
		background: #000; border-radius: 14px; overflow: hidden;
		display: flex; flex-direction: column;
		width: min(480px, 95vw);
	}
	.camera-preview {
		width: 100%; aspect-ratio: 4/3; object-fit: cover; display: block;
	}
	.camera-actions {
		display: flex; gap: 10px; justify-content: space-between;
		padding: 14px 16px; background: var(--bg-card);
	}
	.camera-shoot {
		flex: 1; padding: 10px 20px; background: var(--accent);
		color: var(--bg); border-radius: 8px; font-size: 14px; font-weight: 600;
		transition: opacity 0.15s;
	}
	.camera-shoot:hover { opacity: 0.85; }
	.camera-err {
		padding: 24px 20px; color: var(--danger); font-size: 13px;
		text-align: center; line-height: 1.5;
	}

	/* Shared buttons */
	.btn-primary {
		padding: 12px 24px; background: var(--accent); color: var(--bg);
		border-radius: 10px; font-size: 14px; font-weight: 600; transition: opacity 0.15s;
		text-align: center;
	}
	.btn-primary:hover { opacity: 0.85; }
	.btn-ghost {
		padding: 10px 20px; border: 1px solid var(--border); border-radius: 10px;
		color: var(--text-dim); font-size: 14px; transition: all 0.15s;
	}
	.btn-ghost:hover { border-color: var(--text-faint); color: var(--text); }
</style>
