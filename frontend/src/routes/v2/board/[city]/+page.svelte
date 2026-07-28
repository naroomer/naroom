<script>
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { lang, t as tFn } from '$lib/i18n.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	let city = $derived(page.params.city);
	let listings = $state([]);
	let loading = $state(true);
	let error = $state('');
	let savedListingIds = $state(new Set());
	let hasPurchases = $state(false);

	// City selector state — loaded from /api/v2/board/cities (source of truth)
	let citiesData = $state([]); // [{id, label, country_code, active_count, sample_count}]
	let cityDropdownOpen = $state(false);

	// Grouped by country — pure API data, no static fallback registry
	let groupedCities = $derived(() => {
		const groups = {};
		for (const c of citiesData) {
			if (!groups[c.country_code]) groups[c.country_code] = [];
			groups[c.country_code].push(c);
		}
		return Object.entries(groups).map(([code, cities]) => ({
			code,
			name: cities[0]?.country_label || code,
			cities,
		}));
	});

	let currentCityLabel = $derived(() => {
		const found = citiesData.find(c => c.id === city);
		return found ? found.label : city;
	});

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
		if (!memberSinceUnix) return null;
		const days = Math.floor((Date.now() / 1000 - memberSinceUnix) / 86400);
		if (days === 0) return t('v2.rep.today');
		if (days < 7)   return t('v2.rep.days_short',   { n: days });
		if (days < 30)  return t('v2.rep.weeks_short',  { n: Math.floor(days / 7) });
		if (days < 365) return t('v2.rep.months_short', { n: Math.floor(days / 30) });
		return t('v2.rep.years_short', { n: Math.floor(days / 365) });
	}

	async function loadBoard() {
		loading = true;
		error = '';
		try {
			const ctrl = new AbortController();
			const tid = setTimeout(() => ctrl.abort(), 10000);
			const res = await fetch(`/api/v2/board/${city}`, { signal: ctrl.signal });
			clearTimeout(tid);
			if (res.status === 404) { listings = []; return; }
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			listings = await res.json();
		} catch (e) {
			if (e.name !== 'AbortError') error = e.message;
		} finally {
			loading = false;
		}
	}

	async function loadCities() {
		try {
			const ctrl = new AbortController();
			const tid = setTimeout(() => ctrl.abort(), 8000);
			const res = await fetch('/api/v2/board/cities', { signal: ctrl.signal });
			clearTimeout(tid);
			if (res.ok) {
				citiesData = await res.json();
				// Redirect to default if the current city is not in the registry.
				if (citiesData.length > 0 && !citiesData.find(c => c.id === page.params.city)) {
					goto('/v2/board/tbilisi', { replaceState: true });
				}
			}
		} catch {
			// API unavailable — city tabs won't show; board content loads independently.
		}
	}

	onMount(() => {
		loadBoard();
		loadCities();

		// Scan localStorage for any saved helper purchases
		try {
			let count = 0;
			for (let i = 0; i < localStorage.length; i++) {
				const key = localStorage.key(i);
				if (key && key.startsWith('v2_hpt_')) {
					const lhptId = key.replace('v2_hpt_', '');
					savedListingIds = new Set([...savedListingIds, lhptId]);
					count++;
				}
			}
			hasPurchases = count > 0;
		} catch {}
	});

	// Reload when city changes (SvelteKit re-uses the same component instance)
	$effect(() => { city; loadBoard(); });

	function closeDropdown(e) {
		if (cityDropdownOpen && e.target && !e.target.closest('.city-selector')) {
			cityDropdownOpen = false;
		}
	}
</script>

<svelte:window onclick={closeDropdown} />

<div class="page">
	<header>
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
		<nav>
			{#if hasPurchases}
				<a href="/v2/helper/purchases" class="purchases-link">{t('v2.helper.my_purchases')}</a>
			{/if}
			<a href="/v2/restore">{t('v2.nav.restore')}</a>
			<a href="/v2/informer">{t('v2.nav.informer')}</a>
			<a href="/v2/how-it-works">{t('v2.nav.how_it_works')}</a>
		</nav>
	</header>

	<!-- City selector (grouped dropdown) -->
	<div class="city-selector-row">
		<div class="city-selector">
			<button
				class="city-trigger"
				onclick={() => cityDropdownOpen = !cityDropdownOpen}
				aria-haspopup="listbox"
				aria-expanded={cityDropdownOpen}
			>
				<span class="city-trigger-label">{currentCityLabel()}</span>
				<span class="city-trigger-arrow" aria-hidden="true">{cityDropdownOpen ? '▲' : '▼'}</span>
			</button>

			{#if cityDropdownOpen}
				<div class="city-dropdown" role="listbox">
					{#each groupedCities() as group}
						<div class="city-group">
							<div class="city-group-label">{group.name}</div>
							{#each group.cities as c}
								<a
									href="/v2/board/{c.id}"
									class="city-option"
									class:selected={c.id === city}
									role="option"
									aria-selected={c.id === city}
									onclick={() => cityDropdownOpen = false}
								>
									<span class="city-option-name">{c.label}</span>
									{#if c.active_count > 0}
										<span class="city-active-badge">{c.active_count}</span>
									{/if}
								</a>
							{/each}
						</div>
					{/each}
				</div>
			{/if}
		</div>
	</div>

	{#if loading}
		<div class="status-msg">{t('v2.loading')}</div>
	{:else if error}
		<div class="status-msg error">{t('v2.error', { msg: error })} <button onclick={loadBoard}>{t('v2.retry')}</button></div>
	{:else}
		<div class="grid">
			<!-- CTA card -->
			<a href="/v2/new" class="card cta">
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
						{#if l.client_reputation || l.display_name}
							<div class="rep-block">
								{#if l.display_name}
									<div class="rep-nickname">
										<span class="rep-nickname-label">{t('v2.rep.label')}:</span>
										<span class="rep-nickname-value">{l.display_name}</span>
									</div>
								{/if}
								{#if l.client_reputation}
									{@const age = reputationAge(l.client_reputation.member_since_unix ?? l.client_reputation.member_since)}
									{#if age}
										<div class="rep-since">{t('v2.rep.since', { age })}</div>
									{/if}
									{#if l.client_reputation.positive_count >= 0 && l.client_reputation.negative_count >= 0}
										<div class="rep-rating">
											{t('v2.rep.rating', { pos: l.client_reputation.positive_count, neg: l.client_reputation.negative_count })}
										</div>
									{/if}
								{/if}
							</div>
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
		</div>

		<!-- Empty state -->
		{#if listings.length === 0}
			<div class="empty-state">
				<p class="empty-state-text">{t('v2.board.empty_state')}</p>
				<a href="/v2/new" class="btn-cta">{t('v2.board.empty_cta')}</a>
			</div>
		{/if}

		<!-- Examples section -->
		<div class="examples-section">
			<div class="examples-heading">{t('v2.board.examples_heading')}</div>
			<div class="grid">
				<div class="card listing example-card">
					<div class="urgency-strip" style="background: var(--warn)"></div>
					<div class="card-body">
						<div class="example-badge-label">{t('v2.board.example_badge')}</div>
						<div class="dep">{t('v2.board.example_1_title')}</div>
						<div class="help">{t('v2.board.example_1_desc')}</div>
						<div class="meta">
							<span class="urgency-tag" style="color: var(--warn)">
								{t('urgency.soon')}
							</span>
						</div>
					</div>
				</div>
				<div class="card listing example-card">
					<div class="urgency-strip" style="background: var(--warn)"></div>
					<div class="card-body">
						<div class="example-badge-label">{t('v2.board.example_badge')}</div>
						<div class="dep">{t('v2.board.example_2_title')}</div>
						<div class="help">{t('v2.board.example_2_desc')}</div>
						<div class="meta">
							<span class="urgency-tag" style="color: var(--warn)">
								{t('urgency.soon')}
							</span>
						</div>
					</div>
				</div>
			</div>
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
	.purchases-link { color: var(--accent) !important; font-weight: 600; }
	@media (max-width: 480px) {
		nav { gap: 10px; }
		nav a { font-size: 12px; }
	}

	/* ── City selector ── */
	.city-selector-row {
		padding: 12px 0;
		border-bottom: 1px solid var(--border);
		margin-bottom: 20px;
	}

	.city-selector {
		position: relative;
		display: inline-block;
	}

	.city-trigger {
		display: flex;
		align-items: center;
		gap: 8px;
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 8px 14px;
		color: var(--text);
		font-size: 14px;
		font-weight: 500;
		cursor: pointer;
		transition: border-color 0.15s;
		font-family: inherit;
	}
	.city-trigger:hover { border-color: var(--text-dim); }
	.city-trigger-label { flex: 1; }
	.city-trigger-arrow { font-size: 10px; color: var(--text-faint); }

	.city-dropdown {
		position: absolute;
		top: calc(100% + 4px);
		left: 0;
		min-width: 260px;
		max-width: 320px;
		max-height: 420px;
		overflow-y: auto;
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 10px;
		z-index: 100;
		box-shadow: 0 8px 24px rgba(0,0,0,0.25);
		padding: 8px 0;
	}

	.city-group {
		padding: 0 0 8px;
	}

	.city-group-label {
		font-size: 10px;
		font-weight: 700;
		color: var(--text-faint);
		text-transform: uppercase;
		letter-spacing: 0.8px;
		padding: 6px 14px 3px;
	}

	.city-option {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 7px 14px;
		font-size: 13px;
		color: var(--text-dim);
		text-decoration: none;
		transition: background 0.1s, color 0.1s;
		cursor: pointer;
	}
	.city-option:hover { background: var(--bg-hover, rgba(255,255,255,0.05)); color: var(--text); }
	.city-option.selected { color: var(--accent); font-weight: 600; }
	.city-option-name { flex: 1; }

	.city-active-badge {
		background: var(--accent);
		color: var(--bg);
		font-size: 10px;
		font-weight: 700;
		border-radius: 10px;
		padding: 1px 6px;
		min-width: 18px;
		text-align: center;
	}

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

	/* Reputation block */
	.rep-block {
		display: flex;
		flex-direction: column;
		gap: 2px;
		margin-top: 4px;
	}
	.rep-nickname {
		display: flex;
		align-items: baseline;
		gap: 4px;
		font-size: 10px;
	}
	.rep-nickname-label { color: var(--text-faint); }
	.rep-nickname-value { color: var(--text-dim); font-style: italic; }
	.rep-since { font-size: 10px; color: var(--text-faint); }
	.rep-rating { font-size: 11px; color: var(--text-dim); }

	.footer { display: flex; justify-content: space-between; align-items: center; margin-top: 8px; padding-top: 8px; border-top: 1px solid var(--border); }
	.time { font-size: 11px; color: var(--text-dim); }

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

	/* Empty state */
	.empty-state {
		text-align: center;
		padding: 24px 20px 8px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 12px;
	}
	.empty-state-text { color: var(--text-dim); font-size: 14px; margin: 0; }
	.btn-cta {
		display: inline-block;
		background: var(--accent);
		color: var(--bg);
		border-radius: 8px;
		padding: 10px 20px;
		font-size: 13px;
		font-weight: 600;
		text-decoration: none;
		transition: opacity 0.15s;
	}
	.btn-cta:hover { opacity: 0.85; }

	/* Examples section */
	.examples-section {
		margin-top: 32px;
		border-top: 1px solid var(--border);
		padding-top: 20px;
	}
	.examples-heading {
		font-size: 12px;
		font-weight: 700;
		color: var(--text-faint);
		text-transform: uppercase;
		letter-spacing: 1px;
		margin-bottom: 12px;
	}

	/* Example cards: not clickable, slightly muted */
	.example-card {
		opacity: 0.7;
		cursor: default;
		pointer-events: none;
	}
	.example-badge-label {
		font-size: 9px;
		font-weight: 700;
		color: var(--text-faint);
		text-transform: uppercase;
		letter-spacing: 1px;
		margin-bottom: 2px;
		border: 1px solid var(--border);
		border-radius: 3px;
		padding: 1px 5px;
		display: inline-block;
	}
</style>
