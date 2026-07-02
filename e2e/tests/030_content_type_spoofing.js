// 030_content_type_spoofing
// Свойство: загрузка изображений валидирует РЕАЛЬНЫЙ тип по содержимому
// (magic bytes / декодирование), а не по расширению или заголовку Content-Type.
//
// Дополняет 005 (там только размер). Векторы:
//   a) полиглот JPEG+HTML: валидный JPEG-заголовок, но внутри <script> —
//      должен либо отклоняться, либо ресэмплиться/перекодироваться так,
//      что HTML-нагрузка не сохраняется и не отдаётся как text/html.
//   b) text/plain с расширением .jpg и Content-Type: image/jpeg — отклонить
//      (magic bytes не совпадают).
//   c) SVG с встроенным <script> — либо отклонить (SVG не в списке разрешённых),
//      либо отдавать с Content-Type: image/... и заголовком, запрещающим
//      исполнение (важно для stored-XSS через <img src> → прямое открытие файла).

import { TestServer } from '../lib/server.js';
import { ApiClient } from '../lib/http.js';

export const name = '030_content_type_spoofing';

// минимальный валидный JPEG (SOI + APP0 + EOI), затем HTML-нагрузка
function polyglotJpegHtml() {
  const jpegHead = Buffer.from([
    0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
    0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9,
  ]);
  const html = Buffer.from('<script>window.__xss=1</script>', 'utf8');
  return Buffer.concat([jpegHead, html]);
}

async function uploadImage(api, { bytes, filename, contentType }) {
  // ADAPT: под реальный upload-эндпоинт. Если multipart — использовать FormData.
  const form = new FormData();
  form.append('file', new Blob([bytes], { type: contentType }), filename);
  return api.postForm('/api/upload', form); // ADAPT: роут + ApiClient.postForm
}

export async function run() {
  const server = new TestServer();
  await server.start();
  const api = new ApiClient(server.url);
  // ADAPT: верификация кошелька, если upload требует сессию

  try {
    // --- (a) полиглот ---
    let res = await uploadImage(api, {
      bytes: polyglotJpegHtml(),
      filename: 'cat.jpg',
      contentType: 'image/jpeg',
    });
    if (res.status >= 200 && res.status < 300) {
      // Если приняли — обязана быть перекодировка. Проверяем, что отданный
      // файл больше не содержит HTML-нагрузку.
      const url = res.body.url; // ADAPT
      const fetched = await fetch(server.url + url);
      const ct = fetched.headers.get('content-type') || '';
      const body = Buffer.from(await fetched.arrayBuffer());
      if (ct.includes('text/html')) {
        throw new Error('SPOOF(a): полиглот отдаётся как text/html — исполнимый XSS');
      }
      if (body.toString('latin1').includes('<script>')) {
        throw new Error('SPOOF(a): HTML-нагрузка сохранена внутри принятого файла (нет перекодировки)');
      }
    }
    // status 4xx = отклонён, это ок

    // --- (b) text/plain как .jpg ---
    res = await uploadImage(api, {
      bytes: Buffer.from('это просто текст, не картинка', 'utf8'),
      filename: 'fake.jpg',
      contentType: 'image/jpeg',
    });
    if (res.status >= 200 && res.status < 300) {
      throw new Error('SPOOF(b): text/plain с .jpg расширением принят как изображение');
    }

    // --- (c) SVG со скриптом ---
    res = await uploadImage(api, {
      bytes: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>', 'utf8'),
      filename: 'x.svg',
      contentType: 'image/svg+xml',
    });
    if (res.status >= 200 && res.status < 300) {
      const url = res.body.url;
      const fetched = await fetch(server.url + url);
      const ct = fetched.headers.get('content-type') || '';
      // SVG допустим только если он не исполняется в контексте сайта:
      // отдан как attachment или image/svg+xml с CSP/Content-Disposition.
      const cd = fetched.headers.get('content-disposition') || '';
      if (ct.includes('svg') && !cd.includes('attachment')) {
        throw new Error('SPOOF(c): SVG со скриптом отдаётся инлайн — stored XSS при прямом открытии');
      }
    }
  } finally {
    await server.stop();
  }
}
