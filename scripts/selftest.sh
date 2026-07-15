#!/usr/bin/env bash
# scripts/selftest.sh — full self-test: build + unit tests + frontend check/build + E2E suite
# Usage: ./scripts/selftest.sh [--unit-only | --e2e-only]
# Exit code: 0 = all pass, 1 = any failure

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FRONTEND_DIR="$REPO_ROOT/frontend"
E2E_DIR="$REPO_ROOT/e2e"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

pass()  { echo -e "  ${GREEN}✓${NC} $1"; }
fail()  { echo -e "  ${RED}✗${NC} $1"; }
info()  { echo -e "\n${BOLD}$1${NC}"; }

UNIT_ONLY=false
E2E_ONLY=false

for arg in "$@"; do
  case "$arg" in
    --unit-only) UNIT_ONLY=true ;;
    --e2e-only)  E2E_ONLY=true ;;
  esac
done

# ── Hard dependency checks ─────────────────────────────────────────────────────
if ! $UNIT_ONLY; then
  if ! command -v node &>/dev/null; then
    echo -e "${RED}ERROR: node not found. Install Node.js 18+ before running E2E tests.${NC}" >&2
    exit 1
  fi
fi

FAIL=0
FRONTEND_OK=0
START_TIME=$(date +%s)

# ── 1. Build ──────────────────────────────────────────────────────────────────
if ! $E2E_ONLY; then
  info "[1/4] Build"
  cd "$REPO_ROOT"
  if go build ./... 2>/tmp/naroom-build.log; then
    pass "go build ./..."
  else
    fail "go build ./..."
    cat /tmp/naroom-build.log | sed 's/^/    /'
    exit 1   # build failure is fatal — nothing else makes sense
  fi
fi

# ── 2. Unit tests ─────────────────────────────────────────────────────────────
if ! $E2E_ONLY; then
  info "[2/4] Unit tests  (go test ./...)"
  cd "$REPO_ROOT"
  if go test -v -count=1 -timeout 120s ./... > /tmp/naroom-unit.log 2>&1; then
    PASS_COUNT=$(grep -c "^--- PASS:" /tmp/naroom-unit.log 2>/dev/null; true)
    SKIP_COUNT=$(grep -c "^--- SKIP:" /tmp/naroom-unit.log 2>/dev/null; true)
    OK_PKGS=$(grep -c "^ok " /tmp/naroom-unit.log 2>/dev/null; true)
    pass "all packages  (${PASS_COUNT%$'\n'*} tests, ${SKIP_COUNT%$'\n'*} skipped, ${OK_PKGS%$'\n'*} pkg)"
  else
    fail "go test ./..."
    cat /tmp/naroom-unit.log | sed 's/^/    /'
    FAIL=$((FAIL + 1))
  fi
fi

# ── 3. Frontend check + build ─────────────────────────────────────────────────
if ! $UNIT_ONLY && ! $E2E_ONLY; then
  info "[3/4] Frontend"

  # Discovery file guard — fail fast if static files contain stale content
  if bash "$REPO_ROOT/scripts/check_discovery_files.sh" "$FRONTEND_DIR/static" > /tmp/naroom-discovery.log 2>&1; then
    pass "discovery files (robots/sitemap/llms)"
  else
    fail "discovery files (robots/sitemap/llms)"
    cat /tmp/naroom-discovery.log | sed 's/^/    /'
    FAIL=$((FAIL + 1))
  fi

  if [ ! -d "$FRONTEND_DIR" ]; then
    fail "frontend directory not found: $FRONTEND_DIR"
    FAIL=$((FAIL + 1))
  else
    cd "$FRONTEND_DIR"

    if npm run check > /tmp/naroom-frontend-check.log 2>&1; then
      COMPLETED=$(grep "COMPLETED" /tmp/naroom-frontend-check.log 2>/dev/null | tail -1 || echo "")
      WARN_COUNT=$(echo "$COMPLETED" | grep -oE "[0-9]+ WARNINGS" | grep -oE "[0-9]+" || echo "?")
      ERR_COUNT=$(echo "$COMPLETED" | grep -oE "[0-9]+ ERRORS" | grep -oE "[0-9]+" || echo "0")
      pass "npm run check  (${ERR_COUNT} errors, ${WARN_COUNT} warnings)"
    else
      fail "npm run check"
      grep "ERROR\|COMPLETED" /tmp/naroom-frontend-check.log | sed 's/^/    /'
      FAIL=$((FAIL + 1))
    fi

    if npm run build > /tmp/naroom-frontend-build.log 2>&1; then
      pass "npm run build"
      FRONTEND_OK=1
    else
      fail "npm run build"
      tail -30 /tmp/naroom-frontend-build.log | sed 's/^/    /'
      FAIL=$((FAIL + 1))
    fi
  fi
fi

# ── 4. E2E tests ──────────────────────────────────────────────────────────────
if ! $UNIT_ONLY; then
  info "[4/4] E2E tests"

  cd "$E2E_DIR"

  # Hard fail if no test files found
  # 026, 043, and 047 are Playwright browser tests — require running frontend + `npm i playwright`.
  # Run them separately:
  #   FRONTEND_URL=http://localhost:4173 node e2e/tests/026_analytics_privacy.js
  #   node e2e/tests/043_browser_renewal.js   (starts its own backend + vite dev)
  #   node e2e/tests/047_telegram_reconnect.js (starts its own backend + vite dev)
  E2E_FILES=( tests/0*.js )
  E2E_FILES=( "${E2E_FILES[@]/tests\/026_analytics_privacy.js}" )
  E2E_FILES=( "${E2E_FILES[@]/tests\/043_browser_renewal.js}" )
  E2E_FILES=( "${E2E_FILES[@]/tests\/047_telegram_reconnect.js}" )
  E2E_FILES=( "${E2E_FILES[@]}" )  # re-index
  # Filter out empty entries
  E2E_FILES=( $(printf '%s\n' "${E2E_FILES[@]}" | grep -v '^$') )
  if [ ${#E2E_FILES[@]} -eq 0 ]; then
    echo -e "${RED}ERROR: no E2E test files found in $E2E_DIR/tests/${NC}" >&2
    exit 1
  fi

  E2E_PASS=0
  E2E_FAIL=0

  for f in "${E2E_FILES[@]}"; do
    name="$(basename "$f" .js)"
    if node "$f" > /tmp/naroom-e2e-${name}.log 2>&1; then
      E2E_PASS=$((E2E_PASS + 1))
      pass "$name"
    else
      E2E_FAIL=$((E2E_FAIL + 1))
      fail "$name"
      tail -25 /tmp/naroom-e2e-${name}.log | sed 's/^/    /'
    fi
  done

  FAIL=$((FAIL + E2E_FAIL))
fi

# ── Summary ───────────────────────────────────────────────────────────────────
END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo ""
echo "════════════════════════════════════════"
if ! $E2E_ONLY; then
  if grep -q "^FAIL" /tmp/naroom-unit.log 2>/dev/null; then
    echo -e "  Unit:      ${RED}FAIL${NC}"
  else
    echo -e "  Unit:      ${GREEN}PASS${NC}"
  fi
  if ! $UNIT_ONLY; then
    if [ "$FRONTEND_OK" -eq 1 ]; then
      echo -e "  Frontend:  ${GREEN}PASS (build)${NC}"
    elif [ -f /tmp/naroom-frontend-build.log ]; then
      echo -e "  Frontend:  ${RED}FAIL (build)${NC}"
    fi
  fi
fi
if ! $UNIT_ONLY; then
  TOTAL_E2E=$((E2E_PASS + E2E_FAIL))
  if [ "$E2E_FAIL" -eq 0 ]; then
    echo -e "  E2E:       ${GREEN}${E2E_PASS}/${TOTAL_E2E} PASS${NC}"
  else
    echo -e "  E2E:       ${RED}${E2E_PASS}/${TOTAL_E2E} PASS  (${E2E_FAIL} FAIL)${NC}"
  fi
fi
echo "  Time:      ${ELAPSED}s"
echo "════════════════════════════════════════"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
