#!/bin/sh
# Builds the agentrec release archives and their checksum file. Nothing is
# published: the script only writes into the output directory it is given.
#
#   scripts/build-release.sh <vX.Y.Z> <40-hex-commit> <RFC3339-UTC-date> <output-dir>
#
# Every argument arrives from a workflow expression or a shell, so each one is
# validated before it reaches a build command. No argument is ever re-parsed by
# a shell: there is no eval, and every expansion is quoted.
set -eu

program=$(basename -- "$0")
usage="usage: ${program} <vX.Y.Z> <40-hex-commit> <RFC3339-UTC-date> <output-dir>"

newline='
'

fail() {
	printf '%s: %s\n' "$program" "$1" >&2
	exit 1
}

# check_arg rejects an argument that is not exactly one line matching the
# extended regular expression. The newline test comes first because grep works
# a line at a time and would otherwise accept a value with a valid line in it.
check_arg() {
	label=$1
	value=$2
	pattern=$3

	case $value in
	*"$newline"*) fail "invalid ${label}: must be a single line" ;;
	esac
	if ! printf '%s\n' "$value" | LC_ALL=C grep -Eq "^${pattern}\$"; then
		fail "invalid ${label}"
	fi
}

if [ "$#" -ne 4 ]; then
	printf '%s\n' "$usage" >&2
	exit 2
fi

version=$1
commit=$2
built=$3
output=$4

check_arg "version" "$version" 'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
check_arg "commit" "$commit" '[0-9a-f]{40}'
check_arg "build date" "$built" '[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z'
if normalized=$(date -j -u -f '%Y-%m-%dT%H:%M:%SZ' "$built" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null); then
	:
elif normalized=$(date -u -d "$built" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null); then
	:
else
	fail "invalid build date"
fi
if [ "$normalized" != "$built" ]; then
	fail "invalid build date"
fi
# A path, not an option and not a shell fragment: leading dashes and the
# characters a careless caller could smuggle a second word in with are refused.
case $output in
-*) fail "invalid output directory" ;;
esac
check_arg "output directory" "$output" '[^[:cntrl:]$`|;&<>*?"'"'"']+'

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
package=github.com/seongwoo-choi/agentrec/internal/cli
semver=${version#v}

# Checksums must be portable between the macOS and Linux toolchains.
if command -v sha256sum >/dev/null 2>&1; then
	checksum() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
	checksum() { shasum -a 256 "$@"; }
else
	fail "no sha256sum or shasum available"
fi

if [ -e "$output" ] || [ -L "$output" ]; then
	fail "output directory already exists: ${output}"
fi
if ! mkdir -- "$output"; then
	fail "could not create output directory: ${output}"
fi
output=$(CDPATH='' cd -- "$output" && pwd)

# Staging is private to this invocation. Existing caller-owned paths are never
# removed to make room for a build.
stage_root=$(mktemp -d "${TMPDIR:-/tmp}/agentrec-release.XXXXXX")
cleanup() {
	rm -rf -- "$stage_root"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

for platform in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
	goos=${platform%/*}
	goarch=${platform#*/}
	base="agentrec_${semver}_${goos}_${goarch}"
	stage="${stage_root}/${base}"

	mkdir -p -- "$stage/third_party/licenses"

	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
		-C "$repo_root" \
		-trimpath \
		-buildvcs=false \
		-ldflags "-X ${package}.version=${version} -X ${package}.commit=${commit} -X ${package}.built=${built}" \
		-o "${stage}/agentrec" \
		./cmd/agentrec

	cp -- "${repo_root}/LICENSE" "${stage}/LICENSE"
	cp -- "${repo_root}/THIRD_PARTY_NOTICES.md" "${stage}/THIRD_PARTY_NOTICES.md"
	cp -- "${repo_root}/third_party/licenses/Apache-2.0.txt" "${stage}/third_party/licenses/Apache-2.0.txt"
	chmod 0755 \
		"${stage}" \
		"${stage}/agentrec" \
		"${stage}/third_party" \
		"${stage}/third_party/licenses"
	chmod 0644 \
		"${stage}/LICENSE" \
		"${stage}/THIRD_PARTY_NOTICES.md" \
		"${stage}/third_party/licenses/Apache-2.0.txt"

	COPYFILE_DISABLE=1 tar --no-xattrs -czf "${output}/${base}.tar.gz" -C "$stage_root" "$base"

	printf 'built %s.tar.gz\n' "$base"
done

# Relative names keep the file usable with `sha256sum -c` from the directory it
# describes.
(
	CDPATH='' cd -- "$output"
	checksum "agentrec_${semver}"_*.tar.gz >SHA256SUMS
)

printf 'wrote %s/SHA256SUMS\n' "$output"
