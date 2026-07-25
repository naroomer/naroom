<script>
	import { lang, t as tFn } from '$lib/i18n.js';
	import { CITIES } from '$lib/cities.js';

	let t = $derived((key) => tFn($lang, key));

	const boardUrl = '/v2/board/' + CITIES[0].id;
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
				<li>{t('v2.hiw.client.cost')}</li>
				<li>{t('v2.hiw.client.payment')}</li>
				<li>{t('v2.hiw.client.code')}</li>
				<li>{t('v2.hiw.client.entitlement')}</li>
				<li>{t('v2.hiw.client.visibility')}</li>
				<li>{t('v2.hiw.client.reactivate')}</li>
				<li>{t('v2.hiw.client.reactivate_detail')}</li>
				<li>{t('v2.hiw.client.balance')}</li>
				<li>{t('v2.hiw.client.contact')}</li>
				<li>{t('v2.hiw.client.board')}</li>
			</ul>
		</section>

		<section>
			<h2>{t('v2.hiw.helper.title')}</h2>
			<ul>
				<li>{t('v2.hiw.helper.no_chat')}</li>
				<li>{t('v2.hiw.helper.cost')}</li>
				<li>{t('v2.hiw.helper.multi')}</li>
				<li>{t('v2.hiw.helper.no_refund')}</li>
				<li>{t('v2.hiw.helper.balance')}</li>
				<li>{t('v2.hiw.helper.country')}</li>
				<li>{t('v2.hiw.helper.contact_reveal')}</li>
				<li>{t('v2.hiw.helper.reputation')}</li>
				<li>{t('v2.hiw.helper.review')}</li>
			</ul>
		</section>

		<section>
			<h2>{t('v2.hiw.informer.title')}</h2>
			<ul>
				<li>{t('v2.hiw.informer.desc')}</li>
				<li>{t('v2.hiw.informer.balance')}</li>
				<li>{t('v2.hiw.informer.not_helper')}</li>
			</ul>
		</section>

		<section>
			<h2>{t('v2.hiw.privacy.title')}</h2>
			<ul>
				<li>{t('v2.hiw.privacy.no_accounts')}</li>
				<li>{t('v2.hiw.privacy.wallet_id')}</li>
				<li>{t('v2.hiw.privacy.contact_hide')}</li>
				<li>{t('v2.hiw.privacy.no_chat')}</li>
			</ul>
		</section>

		<div class="cta-row">
			<a href="/v2/new" class="cta-btn">{t('v2.board.i_need_help')}</a>
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
