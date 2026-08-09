// 026_analytics_privacy
// Свойство: GoatCounter (gc.zgo.at / *.goatcounter.com) НЕ загружается на
// приватных маршрутах и загружается — с реальным pageview-запросом на
// https://{code}.goatcounter.com/count — на публичных.
//
// Публичные (V1 legacy + V2 current production): /, /how-it-works, /board/moscow,
// /v2/how-it-works, /v2/board/tbilisi.
// Приватные (V1 legacy + V2 current production): /new, /helper, /chat/*, /listing/*,
// /v2/new, /v2/restore, /v2/informer, /v2/listing/*, /v2/helper/*.
//
// Отличается от 001-025: это браузерный тест, нужен Playwright и запущенный
// фронтенд. Предлагаемая интеграция: selftest.sh поднимает `npm run preview`
// (прод-сборка SvelteKit, НЕ dev — в dev режиме аналитика может быть отключена
// и тест даст ложный PASS) с непустым PUBLIC_GOATCOUNTER_CODE и передаёт
// FRONTEND_URL.
//
// npm i -D playwright && npx playwright install chromium

import { chromium } from 'playwright';

const ANALYTICS_HOSTS = ['gc.zgo.at', 'goatcounter.com'];

// count.js itself refuses to send the /count pageview beacon when
// location.hostname matches its own built-in "local" regex (localhost,
// 127.*, 10.*, 172.16-31.*, 192.168.*), unless allow_local is set — see
// https://gc.zgo.at/count.js. FRONTEND_URL in CI/local runs is
// http://localhost:4173, so the beacon call is unreachable here by design,
// not by a bug in our whitelist. This test still proves the whitelist gates
// the script correctly (loaded vs. not loaded); the actual /count network
// call against the real naroom.net domain is verified separately as part of
// the mandatory production check after deploy.
const GOATCOUNTER_LOCAL_HOST_RE = /(localhost$|^127\.|^10\.|^172\.(1[6-9]|2[0-9]|3[0-1])\.|^192\.168\.|^0\.0\.0\.0$)/;

const PRIVATE_ROUTES = [
  // V1 (legacy — do not remove, still live)
  '/new',
  '/helper',
  '/chat/test-room-id',
  '/listing/test-listing-id',
  // V2 (current production private routes — wallet/session/chat/payment state)
  '/v2/new',
  '/v2/restore',
  '/v2/informer',
  '/v2/listing/sample_cannabis?city=tbilisi',
  '/v2/helper/purchase',
];

const PUBLIC_ROUTES = [
  // V1 (legacy — do not remove, still live)
  '/',
  '/how-it-works',
  '/board/moscow',
  // V2 (current production public routes)
  '/v2/how-it-works',
  '/v2/board/tbilisi',
];

export const name = '026_analytics_privacy';

export async function run() {
  // No backend needed: every tested route either has no server-side data
  // dependency, or (v2/board/[city]) falls back to an empty state when the
  // backend is unreachable during SSR — see +page.server.js. This keeps the
  // check isolated to the analytics whitelist, matching the change's scope.
  const frontendUrl = process.env.FRONTEND_URL || 'http://localhost:4173'; // vite preview
  const isLocalTarget = GOATCOUNTER_LOCAL_HOST_RE.test(new URL(frontendUrl).hostname);

  const browser = await chromium.launch();
  const failures = [];

  try {
    for (const route of [...PRIVATE_ROUTES, ...PUBLIC_ROUTES]) {
      const isPrivate = PRIVATE_ROUTES.includes(route);
      const context = await browser.newContext(); // чистый контекст на маршрут
      const page = await context.newPage();

      const analyticsRequests = [];
      let pageviewRequestUrl = null;
      page.on('request', req => {
        const url = new URL(req.url());
        const host = url.hostname;
        if (ANALYTICS_HOSTS.some(h => host === h || host.endsWith('.' + h))) {
          analyticsRequests.push(req.url());
          // The actual pageview beacon — distinct from the count.js script
          // fetch — is a request to /count on a *.goatcounter.com host.
          if (host.endsWith('.goatcounter.com') && url.pathname === '/count') {
            pageviewRequestUrl = req.url();
          }
        }
      });

      await page.goto(frontendUrl + route, { waitUntil: 'networkidle' });
      // GoatCounter может стрелять с задержкой — добираем окно
      await page.waitForTimeout(1500);

      if (isPrivate && analyticsRequests.length > 0) {
        failures.push(`${route}: аналитика УТЕКЛА (${analyticsRequests.join(', ')})`);
      }
      if (!isPrivate && analyticsRequests.length === 0) {
        failures.push(`${route}: аналитика НЕ загрузилась на публичной странице`);
      }
      if (!isPrivate && !pageviewRequestUrl && !isLocalTarget) {
        failures.push(`${route}: скрипт count.js виден, но нет фактического pageview-запроса на .../count`);
      }
      if (!isPrivate) {
        const note = pageviewRequestUrl
          ? pageviewRequestUrl
          : (isLocalTarget ? '(none — count.js suppresses beacons on localhost by design)' : 'MISSING');
        console.log(`  ${route} → pageview: ${note}`);
      }
      await context.close();
    }
  } finally {
    await browser.close();
  }

  if (failures.length) {
    throw new Error('analytics privacy violations:\n' + failures.join('\n'));
  }
}

run().then(() => process.exit(0)).catch(e => { console.error(e); process.exit(1); });
