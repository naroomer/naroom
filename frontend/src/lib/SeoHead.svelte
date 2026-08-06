<script>
	// SeoHead — shared <svelte:head> block for a single V2 page. Used instead of
	// duplicating title/description/robots/canonical/og/twitter/hreflang/JSON-LD
	// markup on every route. Renders head-only tags; never affects visible layout.
	//
	// On the two SEO-managed routes (how-it-works, board/[city]) this is the
	// ONLY source of <title>/<meta description>/OG/Twitter — +layout.svelte
	// suppresses its own fallback block on those routes (see
	// $lib/seoConfig.js: SEO_MANAGED_ROUTE_IDS) so exactly one of each tag is
	// ever rendered, never two.
	//
	// `robots` and `canonicalUrl` must always be passed explicitly by the
	// caller (no default) so a page can never accidentally fall back to an
	// unindended indexing state. `title`/`description` are optional — private
	// pages that only need the robots/canonical override can omit them and
	// keep the layout's default <title>/<meta description> visible in the tab.
	import { OG_LOCALE } from './seoConfig.js';

	let {
		title = null,
		description = null,
		robots,
		canonicalUrl,
		lang = 'en',
		ogType = 'website',
		ogImageUrl = null,
		hreflangs = [],
		jsonLd = null,
	} = $props();

	let jsonLdBlocks = $derived(
		jsonLd == null ? [] : Array.isArray(jsonLd) ? jsonLd : [jsonLd]
	);
	let ogLocale = $derived(OG_LOCALE[lang] ?? OG_LOCALE.en);
</script>

<svelte:head>
	{#if title}<title>{title}</title>{/if}
	{#if description}<meta name="description" content={description} />{/if}
	<meta name="robots" content={robots} />
	<link rel="canonical" href={canonicalUrl} />

	{#each hreflangs as h (h.lang)}
		<link rel="alternate" hreflang={h.lang} href={h.href} />
	{/each}

	{#if title && description}
		<!-- Open Graph -->
		<meta property="og:type" content={ogType} />
		<meta property="og:site_name" content="NA Room" />
		<meta property="og:title" content={title} />
		<meta property="og:description" content={description} />
		<meta property="og:url" content={canonicalUrl} />
		<meta property="og:locale" content={ogLocale} />
		{#if ogImageUrl}
			<meta property="og:image" content={ogImageUrl} />
		{/if}

		<!-- Twitter / X -->
		<meta name="twitter:card" content={ogImageUrl ? 'summary_large_image' : 'summary'} />
		<meta name="twitter:title" content={title} />
		<meta name="twitter:description" content={description} />
		{#if ogImageUrl}
			<meta name="twitter:image" content={ogImageUrl} />
		{/if}
	{/if}

	{#each jsonLdBlocks as block, i (i)}
		{@html `<script type="application/ld+json">${JSON.stringify(block).replace(/</g, '\\u003c')}</script>`}
	{/each}
</svelte:head>
