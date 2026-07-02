// 026_analytics_privacy
// Свойство: GoatCounter (gc.zgo.at) НЕ загружается на приватных маршрутах
// (/new, /helper, /chat/*, /listing/*) и загружается на публичных
// (/, /how-it-works, /board/*).
//
// Отличается от 001-025: это браузерный тест, нужен Playwright и запущенный
// фронтенд. Предлагаемая интеграция: selftest.sh поднимает `npm run preview`
// (прод-сборка SvelteKit, НЕ dev — в dev режиме аналитика может быть отключена
// и тест даст ложный PASS) и передаёт FRONTEND_URL.
//
// npm i -D playwright && npx playwright install chromium

import { chromium } from 'playwright';
import { TestServer } from '../lib/server.js';

const ANALYTICS_HOSTS = ['gc.zgo.at', 'goatcounter.com'];

// ADAPT: для /chat/* и /listing/* нужны реальные id — создать через ApiClient
// перед прогоном (как в 001), либо принять, что 404-страница по фиктивному id
// рендерится тем же layout'ом (проверить, что это так!).
const PRIVATE_ROUTES = ['/new', '/helper', '/chat/test-room-id', '/listing/test-listing-id'];
const PUBLIC_ROUTES = ['/', '/how-it-works', '/board/moscow'];

export const name = '026_analytics_privacy';

export async function run() {
  const server = new TestServer();
  await server.start();
  const frontendUrl = process.env.FRONTEND_URL || 'http://localhost:4173'; // vite preview

  const browser = await chromium.launch();
  const failures = [];

  try {
    for (const route of [...PRIVATE_ROUTES, ...PUBLIC_ROUTES]) {
      const isPrivate = PRIVATE_ROUTES.includes(route);
      const context = await browser.newContext(); // чистый контекст на маршрут
      const page = await context.newPage();

      const analyticsRequests = [];
      page.on('request', req => {
        const host = new URL(req.url()).hostname;
        if (ANALYTICS_HOSTS.some(h => host === h || host.endsWith('.' + h))) {
          analyticsRequests.push(req.url());
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
      await context.close();
    }
  } finally {
    await browser.close();
    await server.stop();
  }

  if (failures.length) {
    throw new Error('analytics privacy violations:\n' + failures.join('\n'));
  }
}
