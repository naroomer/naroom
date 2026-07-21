<script>
	import QRCode from 'qrcode';

	let { data = '' } = $props();

	let svgMarkup = $state('');

	$effect(() => {
		if (!data) { svgMarkup = ''; return; }
		QRCode.toString(data, { type: 'svg', margin: 1, width: 160 })
			.then(svg => { svgMarkup = svg; })
			.catch(() => { svgMarkup = ''; });
	});
</script>

{#if svgMarkup}
	<div class="v2qr" aria-label="QR code">{@html svgMarkup}</div>
{/if}

<style>
	.v2qr :global(svg) { width: 160px; height: 160px; display: block; }
</style>
