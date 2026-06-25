<script>
	import { onMount } from 'svelte';
	import { lang, initLang, setLang, SUPPORTED_LANGS } from '$lib/i18n.js';
	import { page } from '$app/state';
	import { env } from '$env/dynamic/public';
	const PUBLIC_GOATCOUNTER_CODE = env.PUBLIC_GOATCOUNTER_CODE ?? '';
	import { isAnalyticsRoute } from '$lib/analytics.js';

	let { children } = $props();

	onMount(initLang);

	const LANG_LABEL = { en: 'EN', ru: 'RU', es: 'ES', ka: 'ქარ' };

	const META = {
		en: {
			title:       'NA Room — Anonymous Peer Support for Addiction',
			description: 'Anonymous peer support for people dealing with addiction. No accounts, no identity. End-to-end encrypted chat. Works on Tor.',
			locale:      'en_US',
		},
		ru: {
			title:       'NA Room — Анонимная поддержка при зависимости',
			description: 'Анонимная поддержка для людей с зависимостью. Без аккаунтов, без личных данных. Зашифрованный чат. Работает через Tor.',
			locale:      'ru_RU',
		},
		es: {
			title:       'NA Room — Peer Support Anónimo para Adicciones',
			description: 'Peer support anónimo para personas con adicciones. Sin cuentas, sin identidad. Chat cifrado de extremo a extremo. Funciona en Tor.',
			locale:      'es_ES',
		},
		ka: {
			title:       'NA Room — ანონიმური Peer Support დამოკიდებულებისთვის',
			description: 'ანონიმური Peer support დამოკიდებულებასთან მებრძოლი ადამიანებისთვის. ანგარიშების გარეშე. დაშიფრული ჩატი. მუშაობს Tor-ზე.',
			locale:      'ka_GE',
		},
	};

	let meta = $derived(META[$lang] ?? META.en);

	// ── Analytics (GoatCounter, public pages only) ──────────────────────────────
	// Script is injected once on the first public page visit; subsequent SPA
	// navigations to public routes call goatcounter.count() explicitly.
	// Private routes (/new, /listing/*, /chat/*, /helper, ...) are never tracked.
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
</script>

<svelte:head>
	<title>{meta.title}</title>
	<meta name="viewport" content="width=device-width, initial-scale=1" />
	<meta name="description" content={meta.description} />
	<meta name="robots" content="index, follow" />

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
</svelte:head>

{@render children()}

<!-- Language switcher — fixed bottom-right on all pages -->
<div class="lang-bar">
	{#each SUPPORTED_LANGS as code}
		<button
			class="lang-btn"
			class:active={$lang === code}
			onclick={() => setLang(code)}
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
