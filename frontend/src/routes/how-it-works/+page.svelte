<script>
	import { lang, t as tFn } from '$lib/i18n.js';
	let t = $derived((key, params) => tFn($lang, key, params));

	let role = $state('client'); // 'client' | 'peer'

	// JSON-LD FAQ schema (English — for search engine rich results)
	const faqSchema = JSON.stringify({
		'@context': 'https://schema.org',
		'@type': 'FAQPage',
		mainEntity: [
			{
				'@type': 'Question',
				name: 'Why do I need crypto?',
				acceptedAnswer: {
					'@type': 'Answer',
					text: 'Crypto is the only way to verify you are a real person without an account or personal data. Clients need $150+ balance, peers need $1,000+. No funds are ever moved — only the balance is checked.'
				}
			},
			{
				'@type': 'Question',
				name: 'Who are the peers?',
				acceptedAnswer: {
					'@type': 'Answer',
					text: 'People in recovery themselves, or those with lived experience helping others. They are anonymous too — verified only by their wallet balance and track record on the platform.'
				}
			},
			{
				'@type': 'Question',
				name: 'What if a peer was unhelpful or behaved badly?',
				acceptedAnswer: {
					'@type': 'Answer',
					text: 'Leave a thumbs-down rating after the session. Every peer\'s rating is visible to everyone — a low rating means they stop getting chosen. There is no strict moderation: the platform is anonymous and does not read chats.'
				}
			},
			{
				'@type': 'Question',
				name: 'Is this a crisis service?',
				acceptedAnswer: {
					'@type': 'Answer',
					text: 'No. If you are in immediate danger, please call local emergency services. NA Room is peer support — not professional medical or emergency care.'
				}
			},
			{
				'@type': 'Question',
				name: 'How do I know this works as described?',
				acceptedAnswer: {
					'@type': 'Answer',
					text: 'The full source code is open and available at https://github.com/naroomer/naroom — anyone can verify how encryption works, that messages are not stored, and that there are no hidden functions.'
				}
			},
		]
	});

	const CLIENT_STEPS = [
		['hiw.client_s1_title', 'hiw.client_s1_desc'],
		['hiw.client_s2_title', 'hiw.client_s2_desc'],
		['hiw.client_s3_title', 'hiw.client_s3_desc'],
		['hiw.client_s4_title', 'hiw.client_s4_desc'],
		['hiw.client_s5_title', 'hiw.client_s5_desc'],
		['hiw.client_s6_title', 'hiw.client_s6_desc'],
	];

	const PEER_STEPS = [
		['hiw.peer_s1_title', 'hiw.peer_s1_desc'],
		['hiw.peer_s2_title', 'hiw.peer_s2_desc'],
		['hiw.peer_s3_title', 'hiw.peer_s3_desc'],
		['hiw.peer_s4_title', 'hiw.peer_s4_desc'],
		['hiw.peer_s5_title', 'hiw.peer_s5_desc'],
		['hiw.peer_s6_title', 'hiw.peer_s6_desc'],
	];

	let steps    = $derived(role === 'client' ? CLIENT_STEPS : PEER_STEPS);
	let ctaKey   = $derived(role === 'client' ? 'hiw.client_cta' : 'hiw.peer_cta');
	let ctaHref  = $derived(role === 'client' ? '/new' : '/board/tbilisi');
</script>

<svelte:head>
	<link rel="canonical" href="https://naroom.net/how-it-works" />
	<meta property="og:url" content="https://naroom.net/how-it-works" />
	{@html `<script type="application/ld+json">${faqSchema}<\/script>`}
</svelte:head>

<div class="page">
	<header>
		<a href="/board/tbilisi" class="back">{t('back_to_board')}</a>
	</header>

	<h1>{t('hiw.title')}</h1>
	<p class="sub">{t('hiw.subtitle')}</p>

	<!-- Role switcher -->
	<div class="tabs">
		<button class="tab" class:active={role === 'client'} onclick={() => role = 'client'}>
			{t('hiw.tab_client')}
		</button>
		<button class="tab" class:active={role === 'peer'} onclick={() => role = 'peer'}>
			{t('hiw.tab_peer')}
		</button>
	</div>

	<!-- Steps -->
	<div class="steps">
		{#each steps as [titleKey, descKey], i}
			<div class="step">
				<div class="step-num">{i + 1}</div>
				<div>
					<div class="step-title">{t(titleKey)}</div>
					<div class="step-desc">{t(descKey)}</div>
				</div>
			</div>
		{/each}
	</div>

	<a href={ctaHref} class="btn-cta">{t(ctaKey)}</a>

	<!-- What we know about you -->
	<div class="privacy-table">
		<div class="faq-title">{t('hiw.privacy_title')}</div>
		<table>
			<thead>
				<tr>
					<th>{t('hiw.privacy_col_data')}</th>
					<th>{t('hiw.privacy_col_stored')}</th>
					<th>{t('hiw.privacy_col_deleted')}</th>
				</tr>
			</thead>
			<tbody>
				<tr>
					<td>{t('hiw.privacy_ip')}</td>
					<td class="no">{t('hiw.privacy_never')}</td>
					<td class="dim">—</td>
				</tr>
				<tr>
					<td>{t('hiw.privacy_identity')}</td>
					<td class="no">{t('hiw.privacy_never')}</td>
					<td class="dim">—</td>
				</tr>
				<tr>
					<td>{t('hiw.privacy_analytics')}</td>
					<td class="no">{t('hiw.privacy_never')}</td>
					<td class="dim">—</td>
				</tr>
				<tr>
					<td>{t('hiw.privacy_wallet')}</td>
					<td class="partial">{t('hiw.privacy_hash_only')}</td>
					<td class="dim">{t('hiw.privacy_on_expire')}</td>
				</tr>
				<tr>
					<td>{t('hiw.privacy_messages')}</td>
					<td class="partial">{t('hiw.privacy_e2e_only')}</td>
					<td class="dim">{t('hiw.privacy_on_close')}</td>
				</tr>
				<tr>
					<td>{t('hiw.privacy_session')}</td>
					<td class="partial">{t('hiw.privacy_hash_only')}</td>
					<td class="dim">{t('hiw.privacy_24h')}</td>
				</tr>
				<tr>
					<td>{t('hiw.privacy_listing_meta')}</td>
					<td class="warn">{t('hiw.privacy_yes_plain')}</td>
					<td class="dim">{t('hiw.privacy_on_expire')}</td>
				</tr>
				<tr>
					<td>{t('hiw.privacy_payment')}</td>
					<td class="warn">{t('hiw.privacy_onchain')}</td>
					<td class="dim">{t('hiw.privacy_public_forever')}</td>
				</tr>
			</tbody>
		</table>
		<p class="privacy-note">{t('hiw.privacy_note')}</p>
	</div>

	<!-- FAQ -->
	<div class="faq">
		<div class="faq-title">{t('hiw.faq_title')}</div>
		<div class="qa">
			<div class="q">{t('hiw.q1')}</div>
			<div class="a">{t('hiw.a1')}</div>
		</div>
		<div class="qa">
			<div class="q">{t('hiw.q2')}</div>
			<div class="a">{t('hiw.a2')}</div>
		</div>
		<div class="qa">
			<div class="q">{t('hiw.q3')}</div>
			<div class="a">{t('hiw.a3')}</div>
		</div>
		<div class="qa">
			<div class="q">{t('hiw.q4')}</div>
			<div class="a">{t('hiw.a4')}</div>
		</div>
		<div class="qa">
			<div class="q">{t('hiw.q5')}</div>
			<div class="a">{t('hiw.a5')} <a href="https://github.com/naroomer/naroom" target="_blank" rel="noopener">github.com/naroomer/naroom</a></div>
		</div>
	</div>
</div>

<style>
	.page {
		max-width: 600px;
		margin: 0 auto;
		padding: 0 16px 80px;
	}
	header { padding: 20px 0 24px; }
	.back { color: var(--text-dim); font-size: 13px; }
	.back:hover { color: var(--text); }

	h1 { font-size: 26px; font-weight: 700; color: var(--text); margin-bottom: 10px; }
	.sub { font-size: 15px; color: var(--text-dim); line-height: 1.6; margin-bottom: 28px; }

	/* Role tabs */
	.tabs {
		display: flex;
		gap: 6px;
		margin-bottom: 32px;
		background: var(--bg-card);
		border: 1px solid var(--border);
		border-radius: 12px;
		padding: 4px;
	}
	.tab {
		flex: 1;
		padding: 10px 12px;
		border-radius: 9px;
		font-size: 14px;
		font-weight: 600;
		color: var(--text-dim);
		transition: all 0.15s;
		text-align: center;
	}
	.tab:hover { color: var(--text); }
	.tab.active {
		background: var(--accent);
		color: var(--bg);
	}

	/* Steps */
	.steps { display: flex; flex-direction: column; gap: 20px; margin-bottom: 32px; }
	.step { display: flex; gap: 16px; align-items: flex-start; }
	.step-num {
		width: 28px; height: 28px; border-radius: 50%;
		background: var(--accent); color: var(--bg);
		font-size: 13px; font-weight: 700;
		display: flex; align-items: center; justify-content: center;
		flex-shrink: 0; margin-top: 1px;
	}
	.step-title { font-size: 15px; font-weight: 600; color: var(--text); margin-bottom: 4px; }
	.step-desc { font-size: 14px; color: var(--text-dim); line-height: 1.6; }

	/* CTA */
	.btn-cta {
		display: block; text-align: center; padding: 14px 24px;
		background: var(--accent); color: var(--bg); border-radius: 12px;
		font-size: 15px; font-weight: 600; transition: opacity 0.15s;
		margin-bottom: 40px;
	}
	.btn-cta:hover { opacity: 0.85; }

	/* Privacy table */
	.privacy-table { margin-bottom: 40px; border-top: 1px solid var(--border); padding-top: 28px; }
	table { width: 100%; border-collapse: collapse; font-size: 13px; }
	th {
		text-align: left; font-size: 11px; font-weight: 600; text-transform: uppercase;
		letter-spacing: 0.6px; color: var(--text-faint); padding: 0 8px 10px 0;
		border-bottom: 1px solid var(--border);
	}
	td {
		padding: 9px 8px 9px 0; border-bottom: 1px solid var(--border);
		color: var(--text-dim); vertical-align: top; line-height: 1.4;
	}
	td:first-child { color: var(--text); font-weight: 500; }
	td.no   { color: #4caf7d; font-weight: 600; }
	td.partial { color: var(--accent); font-weight: 500; }
	td.warn { color: #e08a2e; font-weight: 500; }
	td.dim  { color: var(--text-faint); font-size: 12px; }
	.privacy-note {
		font-size: 12px; color: var(--text-faint); line-height: 1.5;
		margin-top: 12px; padding: 10px 12px;
		background: var(--bg-card); border-radius: 8px;
		border: 1px solid var(--border);
	}

	/* FAQ */
	.faq { border-top: 1px solid var(--border); padding-top: 28px; }
	.faq-title { font-size: 12px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.8px; color: var(--text-faint); margin-bottom: 16px; }
	.qa { margin-bottom: 18px; }
	.q { font-size: 14px; font-weight: 600; color: var(--text); margin-bottom: 4px; }
	.a { font-size: 14px; color: var(--text-dim); line-height: 1.6; }
</style>
