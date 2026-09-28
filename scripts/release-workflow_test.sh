#!/bin/sh
set -eu

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
workflow="$repo_root/.github/workflows/release.yml"

require() {
	if ! grep -Fq -- "$1" "$workflow"; then
		printf 'release workflow is missing required exact-head gate: %s\n' "$1" >&2
		exit 1
	fi
}

require 'uses: actions/setup-node@2028fbc5c25fe9cf00d9f06a71cc4710d4507903 # v6.0.0'
require 'node-version: 24.15.0'
require 'run: npm ci --include=dev'
require 'run: npm run test:ui'
require 'python3 scripts/check-readme-localizations.py'
require 'sh scripts/check-readme-localizations_test.sh'

printf 'release workflow boundary tests passed\n'
