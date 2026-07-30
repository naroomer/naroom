<script>
	import { onMount } from 'svelte';
	import { lang, t as tFn } from '$lib/i18n.js';
	import { FALLBACK_CITY_ID } from '$lib/cities.js';

	let t = $derived((key, params) => tFn($lang, key, params));

	let boardCity = $state(FALLBACK_CITY_ID);
	let boardUrl = $derived('/v2/board/' + boardCity);

	const CLIENT_MIN_USD = '$150';
	const HELPER_MIN_USD = '$1,000';
	const INFORMER_MIN_USD = '$1,000';

	onMount(async () => {
		try {
			const r = await fetch('/api/v2/board/cities');
			if (!r.ok) return;
			const cities = await r.json();
			if (Array.isArray(cities) && cities.length > 0) {
				const fallbackEnabled = cities.some((city) => city.id === FALLBACK_CITY_ID);
				boardCity = fallbackEnabled ? FALLBACK_CITY_ID : cities[0].id;
			}
		} catch {}
	});
</script>

<div class="page">
	<header>
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
		<nav>
			<a href={boardUrl}>{t('v2.hiw.back')}</a>
		</nav>
	</header>

	<div class="content">
		<h1 class="page-title">{t('v2.hiw.title')}</h1>

		<section>
			<h2>{t('v2.hiw.client.title')}</h2>
			<ul>
				<li>{t('v2.hiw.client.step1')}</li>
				<li>{t('v2.hiw.client.step2', { min: CLIENT_MIN_USD })}</li>
				<li>{t('v2.hiw.client.step3')}</li>
				<li>{t('v2.hiw.client.step4')}</li>
				<li>{t('v2.hiw.client.step5', { min: CLIENT_MIN_USD })}</li>
				<li>{t('v2.hiw.client.step6')}</li>
			</ul>
		</section>

		<section>
			<h2>{t('v2.hiw.helper.title')}</h2>
			<ul>
				<li>{t('v2.hiw.helper.step1', { min: HELPER_MIN_USD })}</li>
				<li>{t('v2.hiw.helper.step2')}</li>
				<li>{t('v2.hiw.helper.step3', { min: HELPER_MIN_USD })}</li>
				<li>{t('v2.hiw.helper.step4')}</li>
				<li>{t('v2.hiw.helper.step5')}</li>
				<li>{t('v2.hiw.helper.step6')}</li>
			</ul>
		</section>

		<section>
			<h2>{t('v2.hiw.informer.title')}</h2>
			<ul>
				<li>{t('v2.hiw.informer.step1', { min: INFORMER_MIN_USD })}</li>
				<li>{t('v2.hiw.informer.step2')}</li>
				<li>{t('v2.hiw.informer.step3')}</li>
			</ul>
		</section>

		<section>
			<h2>{t('v2.hiw.privacy.title')}</h2>
			<ul>
				<li>{t('v2.hiw.privacy.step1')}</li>
				<li>{t('v2.hiw.privacy.step2')}</li>
				<li>{t('v2.hiw.privacy.step3')}</li>
			</ul>
		</section>

		<div class="cta-row">
			<a href="/v2/new?fresh=1" class="cta-btn">{t('v2.board.i_need_help')}</a>
		</div>
	</div>
</div>

<style>
	.page {
		max-width: 720px;
		margin: 0 auto;
		padding: 0 16px 60px;
	}

	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 20px 0 16px;
		border-bottom: 1px solid var(--border);
		margin-bottom: 32px;
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

	nav a {
		color: var(--text-dim);
		font-size: 13px;
	}
	nav a:hover { color: var(--text); }

	.content {
		display: flex;
		flex-direction: column;
		gap: 36px;
	}

	.page-title {
		font-size: 22px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}

	section {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	h2 {
		font-size: 15px;
		font-weight: 600;
		color: var(--accent);
		margin: 0;
		padding-bottom: 8px;
		border-bottom: 1px solid var(--border);
	}

	ul {
		list-style: none;
		padding: 0;
		margin: 0;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	li {
		padding-left: 16px;
		position: relative;
		color: var(--text);
		font-size: 14px;
		line-height: 1.6;
	}

	li::before {
		content: '—';
		position: absolute;
		left: 0;
		color: var(--text-faint);
	}

	.cta-row {
		padding-top: 8px;
	}

	.cta-btn {
		display: inline-block;
		background: var(--accent);
		color: var(--bg);
		padding: 10px 24px;
		border-radius: 8px;
		font-weight: 600;
		font-size: 14px;
		text-decoration: none;
		transition: opacity 0.15s;
	}
	.cta-btn:hover { opacity: 0.85; }
</style>
