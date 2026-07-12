<script>
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { lang, t as tFn } from '$lib/i18n.js';
	let t = $derived((key, params) => tFn($lang, key, params));

	function detectCurrency(addr) {
		if (!addr) return 'BTC';
		if (/^ltc1/i.test(addr) || /^[LM]/.test(addr)) return 'LTC';
		return 'BTC';
	}

	let wallet   = $state('');
	let loading  = $state(false);
	let error    = $state('');
	let foundListing = $state(null); // { id, status, can_renew }

	function handleResumeData(data) {
		if (data.room_id) { goto(`/chat/${data.room_id}`); return true; }
		if (data.listing_id) {
			foundListing = { id: data.listing_id, status: data.listing_status, can_renew: data.can_renew };
			return true;
		}
		return false;
	}

	// On mount: try stored session tokens before showing the wallet input form.
	// Covers the common case where client has sessionStorage from when they created/accepted.
	onMount(async () => {
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
	});

	async function resume() {
		if (!wallet.trim()) return;
		loading = true; error = ''; foundListing = null;
		try {
			const currency = detectCurrency(wallet.trim());
			// Try peer first, then client — both use same /resume endpoint
			for (const role of ['peer', 'client']) {
				const vr = await fetch('/api/wallet/register', {
					method: 'POST', headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ wallet_address: wallet.trim(), currency, role }),
				});
				if (!vr.ok) continue;
				const { session_token } = await vr.json();
				if (!session_token) continue;
				sessionStorage.setItem(role === 'peer' ? 'naroom_session_peer' : 'naroom_session_client', session_token);

				const pr = await fetch('/api/resume', {
					headers: { 'Authorization': `Bearer ${session_token}` },
				});
				if (pr.ok) {
					if (handleResumeData(await pr.json())) return;
				}
			}
			error = 'No active session found for this wallet.';
		} catch(e) { error = e.message; }
		finally { loading = false; }
	}
</script>

<svelte:head>
	<meta name="robots" content="noindex, nofollow" />
</svelte:head>

<div class="page">
	<div class="card">
		<a href="/board/tbilisi" class="back">{t('back_to_board')}</a>
		<h2>Resume session</h2>
		<p>Enter your wallet address to find your active chat.</p>
		<input
			type="text"
			placeholder="Your BTC or LTC address..."
			bind:value={wallet}
			onkeydown={(e) => e.key === 'Enter' && resume()}
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
			<button disabled={!wallet || loading} onclick={resume}>
				{loading ? '...' : 'Find my session →'}
			</button>
		{/if}
	</div>
</div>

<style>
	.page {
		min-height: 100vh;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 24px;
	}
	.card {
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 16px;
		padding: 32px;
		width: 100%;
		max-width: 420px;
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	.back { font-size: 13px; color: var(--text-dim); }
	.back:hover { color: var(--text); }
	h2 { margin: 0; font-size: 20px; }
	p  { margin: 0; color: var(--text-dim); font-size: 14px; }
	input {
		padding: 10px 14px;
		background: var(--bg-input, var(--bg-card2));
		border: 1px solid var(--border);
		border-radius: 8px;
		color: var(--text);
		font-size: 14px;
		width: 100%;
		box-sizing: border-box;
	}
	button {
		padding: 12px;
		background: var(--accent);
		color: #fff;
		border: none;
		border-radius: 8px;
		font-size: 15px;
		font-weight: 600;
		cursor: pointer;
	}
	button:disabled { opacity: 0.5; cursor: not-allowed; }
	.err { color: var(--error, #e55); font-size: 13px; }
	.matched { background: var(--bg-card2, #1a1a2e); border: 1px solid var(--accent); border-radius: 8px; padding: 14px; font-size: 14px; display: flex; flex-direction: column; gap: 10px; }
	.matched p { margin: 0; }
	.matched a { color: var(--accent); }
	.btn-link { display: inline-block; padding: 10px 16px; background: var(--accent); color: #fff; border-radius: 8px; font-size: 14px; font-weight: 600; text-decoration: none; text-align: center; }
</style>
