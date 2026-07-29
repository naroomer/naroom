<script>
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { lang, t as tFn } from '$lib/i18n.js';
	import { CITIES } from '$lib/cities.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	let city = $derived(page.params.city);
	let listings = $state([]);
	let loading = $state(true);
	let error = $state('');
	let savedListingIds = $state(new Set());

	function urgencyColor(u) {
		if (u === 'urgent')   return 'var(--urgent)';
		if (u === 'soon')     return 'var(--warn)';
		return 'var(--can-wait)';
	}

	function timeLeft(sec) {
		if (sec <= 0) return t('time.expired');
		const h = Math.floor(sec / 3600);
		const m = Math.floor((sec % 3600) / 60);
		if (h > 0) return t('time.h_m_left', { h, m });
		return t('time.m_left', { m });
	}

	function reputationAge(memberSinceUnix) {
		if (!memberSinceUnix) return '';
		const days = Math.floor((Date.now() / 1000 - memberSinceUnix) / 86400);
		if (days < 7) return t('v2.rep.days', { n: days });
		if (days < 30) return t('v2.rep.weeks', { n: Math.floor(days / 7) });
		if (days < 365) return t('v2.rep.months', { n: Math.floor(days / 30) });
		return t('v2.rep.years', { n: Math.floor(days / 365) });
	}

	async function loadBoard() {
		loading = true;
		error = '';
		try {
			const res = await fetch(`/api/v2/board/${city}`);
			if (res.status === 404) { listings = []; return; }
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			listings = await res.json();
		} catch (e) {
			error = e.message;
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadBoard();
		// Scan localStorage for any saved helper purchases
		try {
			for (let i = 0; i < localStorage.length; i++) {
				const key = localStorage.key(i);
				if (key && key.startsWith('v2_hpt_')) {
					const lhptId = key.replace('v2_hpt_', '');
					savedListingIds = new Set([...savedListingIds, lhptId]);
				}
			}
		} catch {}
	});

	// Reload when city changes (SvelteKit re-uses the same component instance)
	$effect(() => { city; loadBoard(); });
</script>

<div class="page">
	<header>
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
		<nav>
			<a href="/v2/restore">{t('v2.nav.restore')}</a>
			<a href="/v2/informer">{t('v2.nav.informer')}</a>
			<a href="/v2/how-it-works">{t('v2.nav.how_it_works')}</a>
		</nav>
	</header>

	<div class="tabs">
		{#each CITIES as c}
			<a
				href="/v2/board/{c.id}"
				class="tab"
				class:active={c.id === city}
			>{c.label}</a>
		{/each}
	</div>

	{#if loading}
		<div class="status-msg">{t('v2.loading')}</div>
	{:else if error}
		<div class="status-msg error">{t('v2.error', { msg: error })} <button onclick={loadBoard}>{t('v2.retry')}</button></div>
	{:else}
		<div class="grid">
			<!-- CTA card -->
			<a href="/v2/new?fresh=1" class="card cta">
				<div class="cta-inner">
					<div class="cta-plus">+</div>
					<div class="cta-text">{t('v2.board.i_need_help')}</div>
				</div>
			</a>

			{#each listings as l}
				<a href="/v2/listing/{l.id}" class="card listing">
					<div class="urgency-strip" style="background: {urgencyColor(l.urgency)}"></div>
					<div class="card-body">
						<div class="dep">{t('dep.' + l.dependency_type)}</div>
						<div class="help">{t('help.' + l.help_type)}</div>
						<div class="meta">
							<span class="urgency-tag" style="color: {urgencyColor(l.urgency)}">
								{t('urgency.' + l.urgency)}
							</span>
							<span class="langs">{(l.languages || []).join(', ').toUpperCase()}</span>
						</div>
						{#if l.client_reputation}
							<div class="rep">
								<span class="rep-age">{reputationAge(l.client_reputation.member_since)}</span>
								{#if l.client_reputation.positive_count > 0 || l.client_reputation.negative_count > 0}
									<span class="rep-score">
										👍{l.client_reputation.positive_count}
										{#if l.client_reputation.negative_count > 0}
										 👎{l.client_reputation.negative_count}
										{/if}
									</span>
								{/if}
							</div>
						{/if}
						{#if l.display_name}
							<div class="client-name">{l.display_name}</div>
						{/if}
						{#if savedListingIds.has(l.id)}
							<div class="continue-badge">{t('v2.helper.continue_purchase')}</div>
						{/if}
						<div class="footer">
							<span class="time">{timeLeft(l.time_left_sec)}</span>
						</div>
					</div>
				</a>
			{/each}

			{#if listings.length === 0}
				{#each [1,2,3,4,5] as _}
					<div class="card empty">
						<div class="empty-label">{t('board.waiting')}</div>
					</div>
				{/each}
			{/if}
		</div>
	{/if}
</div>

<style>
	.page {
		max-width: 960px;
		margin: 0 auto;
		padding: 0 16px 40px;
	}

	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 20px 0 16px;
		border-bottom: 1px solid var(--border);
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

	nav { display: flex; gap: 16px; align-items: center; flex-wrap: wrap; justify-content: flex-end; }
	nav a { color: var(--text-dim); font-size: 13px; white-space: nowrap; }
	nav a:hover { color: var(--text); }
	@media (max-width: 480px) {
		nav { gap: 10px; }
		nav a { font-size: 12px; }
	}

	.tabs {
		display: flex;
		gap: 4px;
		padding: 12px 0;
		border-bottom: 1px solid var(--border);
		overflow-x: auto;
		scrollbar-width: none;
		margin-bottom: 20px;
	}
	.tabs::-webkit-scrollbar { display: none; }
	.tab {
		padding: 6px 14px;
		border-radius: 6px;
		color: var(--text-dim);
		font-size: 13px;
		white-space: nowrap;
		transition: background 0.15s, color 0.15s;
	}
	.tab:hover { background: var(--bg-card); color: var(--text); }
	.tab.active { background: var(--bg-card); color: var(--text); }

	.status-msg {
		text-align: center;
		padding: 40px 20px;
		color: var(--text-dim);
	}
	.status-msg.error { color: var(--danger); }
	.status-msg button {
		margin-left: 8px;
		color: var(--accent);
		cursor: pointer;
		background: none;
		border: none;
		font-size: inherit;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 12px;
	}
	@media (max-width: 600px) {
		.grid { grid-template-columns: 1fr 1fr; }
	}

	.card {
		background: var(--bg-card);
		border-radius: 10px;
		overflow: hidden;
		position: relative;
		min-height: 140px;
	}
	.cta {
		border: 2px solid var(--accent);
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		text-decoration: none;
		transition: background 0.15s;
	}
	.cta:hover { background: var(--bg-hover); }
	.cta-inner { text-align: center; }
	.cta-plus { font-size: 32px; color: var(--accent); line-height: 1; margin-bottom: 6px; }
	.cta-text { color: var(--accent); font-size: 13px; font-weight: 500; }

	.listing {
		border: 1px solid var(--border);
		display: block;
		text-decoration: none;
		transition: border-color 0.15s;
	}
	.listing:hover { border-color: var(--text-faint); }

	.urgency-strip { height: 3px; width: 100%; }
	.card-body { padding: 12px 14px; display: flex; flex-direction: column; gap: 4px; }
	.dep { font-size: 15px; font-weight: 600; color: var(--text); }
	.help { font-size: 12px; color: var(--text-dim); }
	.meta { display: flex; align-items: center; gap: 8px; margin-top: 4px; }
	.urgency-tag { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; }
	.langs { font-size: 11px; color: var(--text-faint); }

	.rep {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-top: 2px;
	}
	.rep-age { font-size: 10px; color: var(--text-faint); }
	.rep-score { font-size: 11px; color: var(--text-dim); }

	.footer { display: flex; justify-content: space-between; align-items: center; margin-top: 8px; padding-top: 8px; border-top: 1px solid var(--border); }
	.time { font-size: 11px; color: var(--text-dim); }

	.empty {
		border: 1px dashed var(--border);
		background: transparent;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.empty-label { font-size: 11px; color: var(--text-faint); letter-spacing: 1px; text-transform: uppercase; }
	.client-name { font-size: 10px; color: var(--text-faint); font-style: italic; }

	.continue-badge {
		font-size: 10px;
		background: var(--accent);
		color: var(--bg);
		border-radius: 4px;
		padding: 2px 6px;
		margin-top: 6px;
		font-weight: 600;
		display: inline-block;
	}
</style>
