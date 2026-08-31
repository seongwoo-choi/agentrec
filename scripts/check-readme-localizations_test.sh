#!/bin/sh
set -eu
umask 077

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/agentrec-readme-test.XXXXXX")
cleanup() {
	rm -rf -- "$tmp"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

seed() {
	cp "$repo_root/README.md" "$repo_root/README.ko.md" \
		"$repo_root/README.ja.md" "$repo_root/README.zh-CN.md" "$tmp/"
}

expect_rejected() {
	expected=$1
	if python3 "$repo_root/scripts/check-readme-localizations.py" "$tmp" >"$tmp/stdout" 2>"$tmp/stderr"; then
		echo "README localization checker accepted $expected" >&2
		exit 1
	fi
	if ! grep -Fq "README.ko.md $expected differ" "$tmp/stderr"; then
		echo "README localization checker did not explain $expected" >&2
		cat "$tmp/stderr" >&2
		exit 1
	fi
}

seed
python3 "$repo_root/scripts/check-readme-localizations.py" "$tmp"

seed
python3 - "$tmp/README.ko.md" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
needle = "agentrec version"
if needle not in text:
    raise SystemExit("test fixture does not contain command")
path.write_text(text.replace(needle, "agentrec not-version", 1), encoding="utf-8")
PY
expect_rejected "executable code blocks"

seed
python3 - "$tmp/README.ko.md" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
needle = "go install ./cmd/agentrec"
if needle not in text:
    raise SystemExit("test fixture does not contain unreleased source install")
path.write_text(text.replace(needle, "go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.1.0", 1), encoding="utf-8")
PY
expect_rejected "executable code blocks"

seed
python3 - "$tmp/README.ko.md" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
needle = "## 네 가지 증거 계층"
if needle not in text:
    raise SystemExit("test fixture does not contain heading")
path.write_text(text.replace(needle, "#### 네 가지 증거 계층", 1), encoding="utf-8")
PY
expect_rejected "heading levels"

seed
python3 - "$tmp/README.ko.md" <<'PY'
from pathlib import Path
import re
import sys

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
changed, count = re.subn(r"https://[^)]+", "https://example.invalid/", text, count=1)
if count != 1:
    raise SystemExit("test fixture does not contain external link")
path.write_text(changed, encoding="utf-8")
PY
expect_rejected "external links"

echo "README localization checks passed"
