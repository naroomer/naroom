<script>
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { env } from '$env/dynamic/public';
	import { lang, initLang, setLang, SUPPORTED_LANGS } from '$lib/i18n.js';
	import { isAnalyticsRoute } from '$lib/analytics.js';
	import { SEO_MANAGED_ROUTE_IDS, langFromUrl, langQuery } from '$lib/seoConfig.js';

	const PUBLIC_GOATCOUNTER_CODE = env.PUBLIC_GOATCOUNTER_CODE ?? '';

	let { children } = $props();

	onMount(initLang);

	// On the two SEO-managed routes, the URL's ?lang= is authoritative (see
	// $lib/seoConfig.js) — the switcher must update the URL there, not just
	// the shared store, and the active button must reflect the URL too, not
	// the browser/localStorage language, or clicking would appear to do
	// nothing when they briefly disagree.
	let isSeoManagedRoute = $derived(SEO_MANAGED_ROUTE_IDS.includes(page.route.id));
	let activeLangCode = $derived(isSeoManagedRoute ? langFromUrl(page.url) : $lang);

	function selectLang(code) {
		setLang(code);
		if (!isSeoManagedRoute) return;
		const url = new URL(page.url);
		url.search = langQuery(code);
		goto(url.pathname + url.search, { replaceState: true, noScroll: true, keepFocus: true });
	}

	// ── Analytics (GoatCounter, public pages only) ──────────────────────────────
	// Script injected once on first public page; SPA navigations call count() manually.
	// Private routes (/new, /listing/*, /chat/*, /helper, /resume) are never tracked.
	let _gcLoaded = false;

	$effect(() => {
		const pathname = page.url.pathname;
		if (!PUBLIC_GOATCOUNTER_CODE || !isAnalyticsRoute(pathname)) return;

		if (!_gcLoaded) {
			_gcLoaded = true;
			const s = document.createElement('script');
			s.dataset.goatcounter = `https://${PUBLIC_GOATCOUNTER_CODE}.goatcounter.com/count`;
			s.async = true;
			s.src = '//gc.zgo.at/count.js';
			document.head.appendChild(s);
			// GoatCounter auto-counts the initial pageview when the script loads.
		} else if (typeof window?.goatcounter?.count === 'function') {
			// SPA navigation to another public page.
			window.goatcounter.count({ path: pathname });
		}
	});

	const LANG_LABEL = { en: 'EN', ru: 'RU', es: 'ES', ka: 'ქარ' };

	// Fallback <title>/<meta description> shown when a page does not override
	// them via <SeoHead>. Kept accurate (Cannabis, Telegram or Signal) so even
	// noindex pages that inherit this default never show stale marketing copy
	// in the browser tab.
	const META = {
		en: {
			title:       'NA Room — Private Cannabis Peer Support',
			description: 'Privacy-focused peer support for people concerned about cannabis use. No account required. Connect with a peer through Telegram or Signal.',
			locale:      'en_US',
		},
		ru: {
			title:       'NA Room — приватная поддержка при каннабисе',
			description: 'Приватная поддержка для тех, кто обеспокоен употреблением каннабиса. Аккаунт не нужен. Связь через Telegram или Signal.',
			locale:      'ru_RU',
		},
		es: {
			title:       'NA Room — apoyo privado sobre cannabis',
			description: 'Apoyo entre personas para quienes están preocupados por el consumo de cannabis. No se requiere cuenta. Contacto por Telegram o Signal.',
			locale:      'es_ES',
		},
		ka: {
			title:       'NA Room — პირადი მხარდაჭერა კანაფის საკითხში',
			description: 'მხარდაჭერა იმათთვის, ვინც შეშფოთებულია კანაფის მოხმარებით. ანგარიში არ არის საჭირო. კავშირი Telegram-ით ან Signal-ით.',
			locale:      'ka_GE',
		},
	};

	let meta = $derived(META[$lang] ?? META.en);
</script>

<svelte:head>
	<meta name="viewport" content="width=device-width, initial-scale=1" />
	{#if !isSeoManagedRoute}
		<!--
			This fallback title/description/OG/Twitter block is for every route
			EXCEPT the two SEO-managed ones (how-it-works, board/[city]) — those
			render their own complete set via <SeoHead>, and this block must stay
			suppressed there so exactly one <title>/description/OG/Twitter ever
			renders, never two. See $lib/seoConfig.js: SEO_MANAGED_ROUTE_IDS.
		-->
		<title>{meta.title}</title>
		<meta name="description" content={meta.description} />
		<!--
			No global <meta name="robots"> here: every route sets its own via
			<SeoHead> (index,follow + canonical on the 2 public V2 pages;
			noindex,nofollow,noarchive + self-canonical on private V2 pages), so
			exactly one robots directive ever renders per page — never a
			layout-level default that could conflict with a page's own tag.
		-->

		<!-- Open Graph -->
		<meta property="og:type"        content="website" />
		<meta property="og:site_name"   content="NA Room" />
		<meta property="og:title"       content={meta.title} />
		<meta property="og:description" content={meta.description} />
		<meta property="og:locale"      content={meta.locale} />

		<!-- Twitter / X -->
		<meta name="twitter:card"        content="summary" />
		<meta name="twitter:title"       content={meta.title} />
		<meta name="twitter:description" content={meta.description} />
	{/if}
</svelte:head>

{@render children()}

<!-- Language switcher — fixed bottom-right on all pages -->
<div class="lang-bar">
	{#each SUPPORTED_LANGS as code}
		<button
			class="lang-btn"
			class:active={activeLangCode === code}
			onclick={() => selectLang(code)}
		>{LANG_LABEL[code] ?? code.toUpperCase()}</button>
	{/each}
</div>

<style>
	:global(*) {
		box-sizing: border-box;
		margin: 0;
		padding: 0;
	}

	:global(:root) {
		--bg:        #2D2B28;
		--bg-card:   #3A3735;
		--bg-hover:  #444140;
		--text:      #CEC8BF;
		--text-dim:  #8A847C;
		--text-faint:#5A5550;
		--accent:    #7BA68E;
		--danger:    #D4845A;
		--warn:      #C4A35A;
		--border:    #4A4745;

		--urgent:    #D4845A;
		--soon:      #C4A35A;
		--can-wait:  #7BA68E;
	}

	:global(body) {
		background: var(--bg);
		color: var(--text);
		font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
		font-size: 14px;
		line-height: 1.5;
		min-height: 100vh;
		overflow-x: hidden;
		overflow-y: auto;
	}

	:global(a) {
		color: var(--accent);
		text-decoration: none;
	}

	:global(button) {
		cursor: pointer;
		border: none;
		background: none;
		font-family: inherit;
		font-size: inherit;
		color: inherit;
	}

	.lang-bar {
		position: fixed;
		bottom: 14px;
		right: 14px;
		display: flex;
		gap: 2px;
		z-index: 200;
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 3px;
	}

	.lang-btn {
		padding: 4px 8px;
		border-radius: 5px;
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.5px;
		color: var(--text-faint);
		transition: all 0.15s;
	}

	.lang-btn:hover { color: var(--text); }

	.lang-btn.active {
		background: var(--accent);
		color: var(--bg);
	}
</style>
