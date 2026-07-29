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
	const samples = [
		{ dependency_type: 'cannabis', help_type: 'crisis', languages: ['EN', 'RU'], display_name: 'Quiet Harbor · A7KM', age_key: 'v2.rep.days_short', age_n: 12, positive_count: 0, negative_count: 0 },
		{ dependency_type: 'cocaine', help_type: 'just_talk', languages: ['EN', 'ES'], display_name: 'Clear Path · R4NX', age_key: 'v2.rep.months_short', age_n: 3, positive_count: 2, negative_count: 0 },
		{ dependency_type: 'alcohol', help_type: 'relapse_prevention', languages: ['EN', 'KA'], display_name: 'Still River · K9TW', age_key: 'v2.rep.weeks_short', age_n: 5, positive_count: 1, negative_count: 0 }
	];

	function urgencyColor(u) {
		if (u === 'urgent')   return 'var(--urgent)';
		if (u === 'soon')     return 'var(--warn)';
		return 'var(--can-wait)';
	}

	function reputationAge(memberSinceUnix) {
		if (!memberSinceUnix) return '';
		const days = Math.floor((Date.now() / 1000 - memberSinceUnix) / 86400);
		if (days <= 0) return t('v2.rep.today');
		if (days < 7) return t('v2.rep.days_short', { n: days });
		if (days < 30) return t('v2.rep.weeks_short', { n: Math.floor(days / 7) });
		if (days < 365) return t('v2.rep.months_short', { n: Math.floor(days / 30) });
		return t('v2.rep.years_short', { n: Math.floor(days / 365) });
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
							<span class="langs">{(l.languages || []).join(', ').toUpperCase()}</span>
						</div>
						{#if l.display_name}
							<div class="identity">
								<span>{t('v2.rep.label')}:</span>
								<strong>{l.display_name}</strong>
							</div>
						{/if}
						{#if l.client_reputation}
							<div class="rep">
								<span class="rep-age">{t('v2.rep.since', { age: reputationAge(l.client_reputation.member_since) })}</span>
								<span class="rep-score">{t('v2.rep.reviews', {
									pos: l.client_reputation.positive_count,
									neg: l.client_reputation.negative_count
								})}</span>
							</div>
						{/if}
						{#if savedListingIds.has(l.id)}
							<div class="continue-badge">{t('v2.helper.continue_purchase')}</div>
						{/if}
					</div>
				</a>
			{/each}

			{#each samples as sample}
				<article class="card listing sample" aria-label={t('v2.board.example_badge')}>
					<div class="urgency-strip" style="background: var(--can-wait)"></div>
					<div class="card-body">
						<div class="dep">{t('dep.' + sample.dependency_type)}</div>
						<div class="help">{t('help.' + sample.help_type)}</div>
						<div class="meta">
							<span class="langs">{sample.languages.join(', ')}</span>
						</div>
						<div class="identity">
							<span>{t('v2.rep.label')}:</span>
							<strong>{sample.display_name}</strong>
						</div>
						<div class="rep">
							<span class="rep-age">{t('v2.rep.since', { age: t(sample.age_key, { n: sample.age_n }) })}</span>
							<span class="rep-score">{t('v2.rep.reviews', {
								pos: sample.positive_count,
								neg: sample.negative_count
							})}</span>
						</div>
						<span class="example-badge">{t('v2.board.example_badge')}</span>
					</div>
				</article>
			{/each}
		</div>

		{#if listings.length === 0}
			<div class="empty-note">
				<span>{t('v2.board.empty_state')}</span>
				<a href="/v2/new?fresh=1">{t('v2.board.empty_cta')}</a>
			</div>
		{/if}
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
	.card-body {
		box-sizing: border-box;
		height: calc(100% - 3px);
		padding: 12px 14px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.dep { font-size: 15px; font-weight: 600; color: var(--text); }
	.help { font-size: 12px; color: var(--text-dim); }
	.meta { display: flex; align-items: center; gap: 8px; margin-top: 4px; }
	.langs { font-size: 11px; color: var(--text-faint); }

	.identity {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		font-size: 10px;
		color: var(--text-faint);
		margin-top: 2px;
	}
	.identity strong { color: var(--text-dim); font-weight: 500; }

	.rep {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 2px;
		margin-top: 2px;
	}
	.rep-age { font-size: 10px; color: var(--text-faint); }
	.rep-score { font-size: 11px; color: var(--text-dim); }

	.empty-note {
		display: flex;
		gap: 8px;
		align-items: center;
		justify-content: center;
		text-align: center;
		padding: 14px 0 0;
	}
	.empty-note span { font-size: 12px; color: var(--text-dim); }
	.empty-note a { font-size: 12px; color: var(--accent); }

	.sample { pointer-events: none; }
	.example-badge {
		align-self: flex-end;
		margin-top: auto;
		background: var(--warn);
		color: var(--bg);
		border-radius: 4px;
		padding: 3px 7px;
		font-size: 9px;
		font-weight: 700;
		letter-spacing: 0.8px;
		text-transform: uppercase;
	}

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
