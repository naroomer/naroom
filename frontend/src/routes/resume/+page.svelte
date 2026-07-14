<script>
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { lang, t as tFn } from '$lib/i18n.js';
	let t = $derived((key, params) => tFn($lang, key, params));

	let recoveryCode = $state('');
	let loading      = $state(false);
	let checking     = $state(true);  // true during onMount auto-check
	let error        = $state('');
	let foundListing = $state(null);
	let showForm     = $state(false); // shown after auto-check fails

	function handleResumeData(data) {
		if (data.room_id) { goto(`/chat/${data.room_id}`); return true; }
		if (data.listing_id) {
			foundListing = { id: data.listing_id, status: data.listing_status, can_renew: data.can_renew };
			return true;
		}
		return false;
	}

	onMount(async () => {
		checking = true;
		for (const storageKey of ['naroom_session_client', 'naroom_session_peer']) {
			const token = sessionStorage.getItem(storageKey);
			if (!token) continue;
			try {
				const pr = await fetch('/api/resume', {
					headers: { 'Authorization': `Bearer ${token}` },
				});
				if (pr.ok) {
					if (handleResumeData(await pr.json())) return;
				}
			} catch {}
		}
		checking = false;
		showForm = true;
	});

	async function recover() {
		if (!recoveryCode.trim()) return;
		loading = true; error = '';
		try {
			const res = await fetch('/api/session/recover', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ recovery_code: recoveryCode.trim() }),
			});
			const data = await res.json();
			if (!res.ok) throw new Error(data.error ?? 'Recovery failed');

			const token = data.session_token;

			// Store session token under the role returned by /session/recover (never both)
			if (!data.role) throw new Error('/session/recover missing role');
			sessionStorage.setItem(`naroom_session_${data.role}`, token);

			// Always show new recovery code before redirecting (if provided)
			if (data.recovery_code) {
				newRecoveryCode = data.recovery_code;
				// Pre-fetch resume data to redirect after ack
				try {
					const pr = await fetch('/api/resume', {
						headers: { 'Authorization': `Bearer ${token}` },
					});
					if (pr.ok) pendingResume = await pr.json();
				} catch {}
				showNewCode = true;
				loading = false;
				return;
			}

			const pr = await fetch('/api/resume', {
				headers: { 'Authorization': `Bearer ${token}` },
			});
			if (pr.ok) {
				if (handleResumeData(await pr.json())) return;
			}
			error = 'No active session found for this recovery code.';
		} catch(e) { error = e.message; }
		finally { loading = false; }
	}

	let newRecoveryCode = $state('');
	let pendingResume = $state(null);
	let showNewCode = $state(false);

	function acknowledgeNewCode() {
		showNewCode = false;
		if (pendingResume) handleResumeData(pendingResume);
	}
</script>

<svelte:head>
	<meta name="robots" content="noindex, nofollow" />
</svelte:head>

<div class="page">
	<div class="card">
		<a href="/board/tbilisi" class="back">{t('back_to_board')}</a>
		<h2>Resume session</h2>

		{#if checking}
			<p class="checking">Checking saved session…</p>
		{:else if showNewCode}
			<p>Your new recovery code (save it — not shown again):</p>
			<div class="recovery-box">{newRecoveryCode}</div>
			<button onclick={acknowledgeNewCode}>I saved it — continue →</button>
		{:else if showForm}
			<p>Enter your recovery code to restore access.</p>
			<input
				type="text"
				placeholder="Paste your recovery code..."
				bind:value={recoveryCode}
				onkeydown={(e) => e.key === 'Enter' && recover()}
			/>
			{#if error}<div class="err">{error}</div>{/if}
			{#if foundListing}
				<div class="matched">
					{#if foundListing.status === 'expired' && foundListing.can_renew}
						<p>{t('listing.resume_expired')}</p>
					{:else}
						<p>{t('listing.resume_active')}</p>
					{/if}
					<a href="/listing/{foundListing.id}" class="btn-link">{t('listing.resume_view')}</a>
				</div>
			{:else}
				<button disabled={!recoveryCode || loading} onclick={recover}>
					{loading ? '...' : 'Restore access →'}
				</button>
			{/if}
		{/if}
	</div>
</div>

<style>
	.page { min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 24px; }
	.card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 16px; padding: 32px; width: 100%; max-width: 420px; display: flex; flex-direction: column; gap: 16px; }
	.back { font-size: 13px; color: var(--text-dim); }
	.back:hover { color: var(--text); }
	h2 { margin: 0; font-size: 20px; }
	p { margin: 0; color: var(--text-dim); font-size: 14px; }
	.checking { font-style: italic; }
	input { padding: 10px 14px; background: var(--bg-input, var(--bg-card2)); border: 1px solid var(--border); border-radius: 8px; color: var(--text); font-size: 14px; width: 100%; box-sizing: border-box; }
	button { padding: 12px; background: var(--accent); color: #fff; border: none; border-radius: 8px; font-size: 15px; font-weight: 600; cursor: pointer; }
	button:disabled { opacity: 0.5; cursor: not-allowed; }
	.err { color: var(--error, #e55); font-size: 13px; }
	.recovery-box { background: var(--bg-card2, #1a1a2e); border: 1px solid var(--accent); border-radius: 8px; padding: 12px; font-family: monospace; font-size: 12px; word-break: break-all; color: var(--text); }
	.matched { background: var(--bg-card2, #1a1a2e); border: 1px solid var(--accent); border-radius: 8px; padding: 14px; font-size: 14px; display: flex; flex-direction: column; gap: 10px; }
	.matched p { margin: 0; }
	.matched .btn-link { color: #fff; }
	.btn-link { display: inline-block; padding: 10px 16px; background: var(--accent); color: #fff; border-radius: 8px; font-size: 14px; font-weight: 600; text-decoration: none; text-align: center; }
</style>
