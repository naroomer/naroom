// 033_devmode_prod_failsafe
// Свойство (приоритет #1 из аудита): прод-сборка НЕ МОЖЕТ работать в DEV_MODE.
// Env-переменная не должна быть достаточной для включения dev-послаблений
// (автоподтверждение инвойсов, пропуск проверки кошелька/баланса).
//
// Отличается от 020: 020 проверяет, что dev-ЗАГОЛОВКИ не утекают в ответах.
// 033 проверяет, что сам dev-РЕЖИМ физически недоступен без dev build tag.
// Это защита на уровне компиляции, а не рантайма.
//
// РЕКОМЕНДУЕМАЯ РЕАЛИЗАЦИЯ (Go build tags):
//   internal/config/devmode_dev.go   //go:build dev      → DevModeAllowed = true
//   internal/config/devmode_prod.go  //go:build !dev     → DevModeAllowed = false
//   При старте: if os.Getenv("DEV_MODE")=="true" && !DevModeAllowed {
//                 log.Fatal("DEV_MODE requested but binary built without -tags dev")
//               }
//   Прод-Makefile собирает `go build` (без -tags dev). Dev/тесты: `go build -tags dev`.
//
// Этот тест — на уровне сборки, а не ApiClient. Он компилирует бэкенд ДВАЖДЫ
// и наблюдает поведение. Запускать в selftest.sh отдельной секцией (нужен go).

import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

export const name = '033_devmode_prod_failsafe';

const REPO = process.env.NAROOM_REPO || path.resolve('.'); // ADAPT: корень репо
const MAIN_PKG = './cmd/naroom';                            // ADAPT: путь к main

function build(outPath, tags) {
  const args = ['build', '-o', outPath];
  if (tags) args.push('-tags', tags);
  args.push(MAIN_PKG);
  execFileSync('go', args, { cwd: REPO, stdio: 'pipe' });
}

// Запускает бинарник с DEV_MODE=true и возвращает { exited, code, stdout, stderr, devActive }.
async function probe(binPath) {
  return new Promise((resolve) => {
    const proc = spawn(binPath, [], {
      cwd: REPO,
      env: { ...process.env, DEV_MODE: 'true', PORT: '0' },
    });
    let stdout = '', stderr = '';
    proc.stdout.on('data', d => (stdout += d));
    proc.stderr.on('data', d => (stderr += d));

    // Если процесс сразу упал (log.Fatal) — это ожидаемое прод-поведение.
    const timer = setTimeout(() => {
      // выжил > 2с — считаем, что стартовал; проверим, активен ли dev-режим
      proc.kill('SIGKILL');
      resolve({ exited: false, code: null, stdout, stderr });
    }, 2000);

    proc.on('exit', (code) => {
      clearTimeout(timer);
      resolve({ exited: true, code, stdout, stderr });
    });
  });
}

export async function run() {
  const dir = mkdtempSync(path.join(tmpdir(), 'naroom-build-'));
  const prodBin = path.join(dir, 'naroom-prod');
  const devBin = path.join(dir, 'naroom-dev');

  try {
    // --- прод-сборка (без -tags dev) ---
    build(prodBin, null);
    const prod = await probe(prodBin);

    // Прод-бинарник с DEV_MODE=true обязан ЛИБО упасть с ненулевым кодом,
    // ЛИБО стартовать, но с ВЫКЛЮЧЕННЫМ dev-режимом.
    const prodRefused =
      (prod.exited && prod.code !== 0) ||
      /built without .*dev|DEV_MODE.*(ignored|not allowed|disallowed)/i.test(prod.stderr + prod.stdout);

    if (!prodRefused) {
      // Стартовал молча — надо доказать, что dev-послабления НЕ активны.
      // Признак активного dev-режима: в логах есть маркер, либо (сильнее)
      // инвойс автоподтверждается. Здесь используем лог-маркер как быстрый
      // сигнал; полная проверка — что реальная оплата всё ещё требуется —
      // делается связкой с 028 на прод-бинарнике.
      const devMarker = /DEV_MODE\s*(=|:)?\s*(true|on|enabled|active)/i.test(prod.stdout + prod.stderr);
      if (devMarker) {
        throw new Error(
          'FAIL-SAFE ПРОБИТ: прод-сборка приняла DEV_MODE=true и включила dev-режим ' +
          '(env-переменной достаточно). Нужен build tag / компиляционный гейт.'
        );
      }
      // Нет маркера и не упал — приемлемо, но требует ручной сверки, что
      // послабления действительно off. Помечаем предупреждением в выводе.
      console.warn('[033] прод-бинарник стартовал с DEV_MODE=true без явного маркера; ' +
                   'проверить связкой с 028, что оплата реально требуется.');
    }

    // --- dev-сборка (с -tags dev) — контрольная группа ---
    // Убеждаемся, что dev-режим ВООБЩЕ достижим — иначе тест дал бы ложный
    // PASS даже если dev-код просто удалён (регрессия удобства разработки).
    let devReachable = false;
    try {
      build(devBin, 'dev');
      const dev = await probe(devBin);
      devReachable =
        !(dev.exited && dev.code !== 0) &&
        /DEV_MODE\s*(=|:)?\s*(true|on|enabled|active)/i.test(dev.stdout + dev.stderr);
    } catch (e) {
      // Если -tags dev не собирается — это отдельная проблема сборки, сообщаем.
      throw new Error(`dev-сборка (-tags dev) не компилируется: ${e.message}`);
    }
    if (!devReachable) {
      throw new Error(
        'КОНТРОЛЬ: dev-сборка НЕ включает dev-режим при DEV_MODE=true. ' +
        'Либо build tag настроен неверно, либо dev-путь сломан.'
      );
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}
