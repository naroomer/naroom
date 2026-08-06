<script>
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { t as tFn } from '$lib/i18n.js';
	import SeoHead from '$lib/SeoHead.svelte';
	import { FALLBACK_CITY_ID } from '$lib/cities.js';
	import { SITE_ORIGIN, SUPPORTED_SEO_LANGS, langFromUrl, langQuery } from '$lib/seoConfig.js';

	// The URL's ?lang= is authoritative on this page — during SSR and after
	// hydration alike, since both read the same page.url. It never flips to
	// the browser/localStorage language post-hydration; the global switcher
	// (in +layout.svelte) updates this same URL instead when used here.
	let effectiveLang = $derived(langFromUrl(page.url));
	let t = $derived((key, params) => tFn(effectiveLang, key, params));

	let boardCity = $state(FALLBACK_CITY_ID);
	let boardUrl = $derived('/v2/board/' + boardCity + langQuery(effectiveLang));

	const CLIENT_MIN_USD = '150';
	const HELPER_PRE_MIN_USD = '1,010';
	const HELPER_POST_MIN_USD = '1,000';
	const INFORMER_MIN_USD = '1,000';

	let canonicalUrl = $derived(SITE_ORIGIN + page.url.pathname + langQuery(effectiveLang));
	let hreflangs = $derived(
		SUPPORTED_SEO_LANGS.map((l) => ({
			lang: l,
			href: SITE_ORIGIN + page.url.pathname + langQuery(l),
		})).concat([{ lang: 'x-default', href: SITE_ORIGIN + page.url.pathname }])
	);
	let jsonLd = $derived([
		{
			'@context': 'https://schema.org',
			'@type': 'WebSite',
			name: 'NA Room',
			url: SITE_ORIGIN + '/v2/how-it-works',
		},
		{
			'@context': 'https://schema.org',
			'@type': 'WebPage',
			name: t('v2.seo.hiw.title'),
			description: t('v2.seo.hiw.description'),
			url: canonicalUrl,
			inLanguage: effectiveLang,
		},
	]);

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

<SeoHead
	title={t('v2.seo.hiw.title')}
	description={t('v2.seo.hiw.description')}
	robots="index, follow"
	canonicalUrl={canonicalUrl}
	lang={effectiveLang}
	ogImageUrl={SITE_ORIGIN + '/og-preview.png'}
	hreflangs={hreflangs}
	jsonLd={jsonLd}
/>

<div class="page">
	<header>
		<div class="logo">NA Room <span class="v2-badge">V2</span></div>
		<nav>
			<a href={boardUrl}>{t('v2.hiw.back')}</a>
		</nav>
	</header>

	<div class="content">
		<h1 class="page-title">{t('v2.hiw.title')}</h1>
		<p>{t('v2.hiw.intro')}</p>

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
				<li>{t('v2.hiw.helper.step1', { min: HELPER_PRE_MIN_USD })}</li>
				<li>{t('v2.hiw.helper.step2')}</li>
				<li>{t('v2.hiw.helper.step3', { min: HELPER_POST_MIN_USD })}</li>
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
				<li>{t('v2.hiw.privacy.step4')}</li>
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
