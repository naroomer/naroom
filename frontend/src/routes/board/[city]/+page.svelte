<script>
	import { lang, t as tFn, pluralRu } from '$lib/i18n.js';

	let { data } = $props();

	let t = $derived((key, params) => tFn($lang, key, params));

	import { CITIES } from '$lib/cities.js';

	function urgencyColor(u) {
		if (u === 'urgent')   return 'var(--urgent)';
		if (u === 'soon')     return 'var(--warn)';
		return 'var(--can-wait)';
	}

	function timeLeft(seconds) {
		if (seconds <= 0) return t('time.expired');
		const h = Math.floor(seconds / 3600);
		const m = Math.floor((seconds % 3600) / 60);
		if (h > 0) return t('time.h_m_left', {h, m});
		return t('time.m_left', {m});
	}

	function responsesLabel(n) {
		if ($lang === 'ru') {
			const form = pluralRu(n);
			return t('board.responses_' + (form === 'one' ? 'one' : form === 'few' ? 'few' : 'other'), {n});
		}
		return t(n === 1 ? 'board.responses_one' : 'board.responses_other', {n});
	}
</script>

<svelte:head>
	<link rel="canonical" href="https://naroom.net/board/{data.city}" />
	<meta property="og:url" content="https://naroom.net/board/{data.city}" />
</svelte:head>

<div class="page">
	<!-- Header -->
	<header>
		<div class="logo">NA Room</div>
		<nav>
			<a href="/resume" class="resume-link">{t('nav.resume_chat')}</a>
			<a href="/how-it-works">{t('nav.how_it_works')}</a>
			<a href="/helper">{t('nav.get_notified')}</a>
		</nav>
	</header>

	<!-- City tabs -->
	<div class="tabs">
		{#each CITIES as city}
			<a
				href="/board/{city.id}"
				class="tab"
				class:active={city.id === data.city}
			>
				{city.label}
				{#if city.id === data.city && data.listings.length > 0}
					<span class="badge">{data.listings.length}</span>
				{/if}
			</a>
		{/each}
	</div>

	<!-- Board grid -->
	<div class="grid">
		<!-- CTA card -->
		<a href="/new" class="card cta">
			<div class="cta-inner">
				<div class="cta-plus">+</div>
				<div class="cta-text">{t('board.i_need_help')}</div>
			</div>
		</a>

		<!-- Listings -->
		{#each data.listings as listing}
			<a href="/listing/{listing.id}" class="card listing" class:sample={listing.is_sample}>
				<div class="urgency-strip" style="background: {urgencyColor(listing.urgency)}"></div>
				<div class="card-body">
					<div class="dep">{t('dep.' + listing.dependency_type)}</div>
					<div class="help">{t('help.' + listing.help_type)}</div>
					<div class="meta">
						<span class="urgency-tag" style="color: {urgencyColor(listing.urgency)}">
							{t('urgency.' + listing.urgency)}
						</span>
						<span class="langs">{listing.languages?.join(', ').toUpperCase()}</span>
					</div>
					<div class="footer">
						{#if listing.is_sample}
							<span class="sample-badge">{t('board.example')}</span>
						{:else}
							<span class="time">{timeLeft(listing.time_left)}</span>
							{#if listing.responses_count > 0}
								<span class="responses">{responsesLabel(listing.responses_count)}</span>
							{/if}
						{/if}
					</div>
				</div>
			</a>
		{/each}

		<!-- Empty slots -->
		{#if data.listings.length === 0}
			{#each [1,2,3,4,5] as _}
				<div class="card empty">
					<div class="empty-label">{t('board.waiting')}</div>
				</div>
			{/each}
		{/if}
	</div>
</div>

<style>
	.page {
		max-width: 960px;
		margin: 0 auto;
		padding: 0 16px 40px;
	}

	/* Header */
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 20px 0 16px;
		border-bottom: 1px solid var(--border);
		margin-bottom: 0;
	}

	.logo {
		font-size: 18px;
		font-weight: 600;
		color: var(--text);
		letter-spacing: 0.5px;
	}

	nav {
		display: flex;
		gap: 24px;
		align-items: center;
	}

	nav a {
		color: var(--text-dim);
		font-size: 13px;
	}

	nav a:hover {
		color: var(--text);
	}

	nav a.resume-link {
		color: var(--accent);
		font-weight: 600;
	}

	nav a.resume-link:hover {
		opacity: 0.8;
	}

	/* Tabs */
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
		display: flex;
		align-items: center;
		gap: 6px;
		transition: background 0.15s, color 0.15s;
	}

	.tab:hover { background: var(--bg-card); color: var(--text); }

	.tab.active {
		background: var(--bg-card);
		color: var(--text);
	}

	.badge {
		background: var(--accent);
		color: var(--bg);
		font-size: 11px;
		font-weight: 600;
		padding: 1px 6px;
		border-radius: 10px;
	}

	/* Grid */
	.grid {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 12px;
	}

	@media (max-width: 600px) {
		.grid { grid-template-columns: 1fr 1fr; }
	}

	/* Card base */
	.card {
		background: var(--bg-card);
		border-radius: 10px;
		overflow: hidden;
		position: relative;
		min-height: 140px;
	}

	/* CTA card */
	.cta {
		border: 2px solid var(--accent);
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		transition: background 0.15s, box-shadow 0.15s;
		text-decoration: none;
	}

	.cta:hover {
		background: var(--bg-hover);
		box-shadow: 0 0 16px rgba(123, 166, 142, 0.2);
	}

	.cta-inner {
		text-align: center;
	}

	.cta-plus {
		font-size: 32px;
		color: var(--accent);
		line-height: 1;
		margin-bottom: 6px;
	}

	.cta-text {
		color: var(--accent);
		font-size: 13px;
		font-weight: 500;
	}

	/* Listing card */
	.listing {
		border: 1px solid var(--border);
		display: block;
		text-decoration: none;
		transition: border-color 0.15s;
	}

	.listing:hover { border-color: var(--text-faint); }

	.listing.sample {
		opacity: 0.75;
		border-style: dashed;
	}

	.sample-badge {
		font-size: 10px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.8px;
		color: var(--accent);
		border: 1px solid var(--accent);
		border-radius: 4px;
		padding: 2px 6px;
	}

	.urgency-strip {
		height: 3px;
		width: 100%;
	}

	.card-body {
		padding: 12px 14px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.dep {
		font-size: 15px;
		font-weight: 600;
		color: var(--text);
	}

	.help {
		font-size: 12px;
		color: var(--text-dim);
	}

	.meta {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-top: 4px;
	}

	.urgency-tag {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.5px;
	}

	.langs {
		font-size: 11px;
		color: var(--text-faint);
	}

	.footer {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-top: 8px;
		padding-top: 8px;
		border-top: 1px solid var(--border);
	}

	.time {
		font-size: 11px;
		color: var(--text-dim);
	}

	.responses {
		font-size: 11px;
		color: var(--accent);
	}

	/* Empty card */
	.empty {
		border: 1px dashed var(--border);
		background: transparent;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.empty-label {
		font-size: 11px;
		color: var(--text-faint);
		letter-spacing: 1px;
		text-transform: uppercase;
	}
</style>
