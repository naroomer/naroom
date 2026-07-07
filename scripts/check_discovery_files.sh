#!/usr/bin/env bash
# check_discovery_files.sh — fail if production-facing discovery files contain stale content.
# Run before deploy or as part of selftest.

set -euo pipefail

FRONTEND_STATIC="${1:-$(dirname "$0")/../frontend/static}"
ERRORS=0

check_absent() {
  local file="$1" pattern="$2" label="$3"
  if grep -qF "$pattern" "$file" 2>/dev/null; then
    echo "FAIL [$label] $file contains stale pattern: $pattern"
    ERRORS=$((ERRORS + 1))
  fi
}

check_present() {
  local file="$1" pattern="$2" label="$3"
  if ! grep -qF "$pattern" "$file" 2>/dev/null; then
    echo "FAIL [$label] $file missing expected pattern: $pattern"
    ERRORS=$((ERRORS + 1))
  fi
}

ROBOTS="$FRONTEND_STATIC/robots.txt"
SITEMAP="$FRONTEND_STATIC/sitemap.xml"
LLMS="$FRONTEND_STATIC/llms.txt"

# robots.txt checks
check_absent  "$ROBOTS" "naroom.io"           "robots:no-stale-domain"
check_present "$ROBOTS" "naroom.net/sitemap"  "robots:sitemap-url"
check_present "$ROBOTS" "Disallow: /new"      "robots:disallow-new"
check_present "$ROBOTS" "Disallow: /api/"     "robots:disallow-api"

# sitemap.xml checks
check_absent  "$SITEMAP" "naroom.io"          "sitemap:no-stale-domain"
check_absent  "$SITEMAP" "/new"               "sitemap:no-new"
check_absent  "$SITEMAP" "/helper"            "sitemap:no-helper"
check_present "$SITEMAP" "naroom.net"         "sitemap:correct-domain"
check_present "$SITEMAP" "buenos_aires"       "sitemap:has-buenos-aires"
check_present "$SITEMAP" "sao_paulo"          "sitemap:has-sao-paulo"

# llms.txt checks
check_absent  "$LLMS" "github.com/naroom\""  "llms:no-stale-github"
# Specifically reject old URL: github.com/naroom not followed by 'er' (i.e. not naroomer)
if grep -qE 'github\.com/naroom([^e]|$)' "$LLMS" 2>/dev/null; then
  echo "FAIL [llms:no-stale-github-bare] $LLMS contains stale github.com/naroom (not naroomer)"
  ERRORS=$((ERRORS + 1))
fi
check_present "$LLMS" "github.com/naroomer/naroom" "llms:correct-github"

if [ "$ERRORS" -gt 0 ]; then
  echo ""
  echo "discovery file check: $ERRORS error(s) — fix before deploy"
  exit 1
fi

echo "discovery file check: OK"
