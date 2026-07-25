<script>
	import { browser } from '$app/environment';

	let { data = '' } = $props();

	let svgMarkup = $state('');

	$effect(() => {
		if (!data || !browser) { svgMarkup = ''; return; }
		import('qrcode').then(mod => {
			mod.default.toString(data, { type: 'svg', margin: 1, width: 160 })
				.then(svg => { svgMarkup = svg; })
				.catch(() => { svgMarkup = ''; });
		}).catch(() => { svgMarkup = ''; });
	});
</script>

{#if svgMarkup}
	<div class="v2qr" aria-label="QR code">{@html svgMarkup}</div>
{/if}

<style>
	.v2qr :global(svg) { width: 160px; height: 160px; display: block; }
</style>
