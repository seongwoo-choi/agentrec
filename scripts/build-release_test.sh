#!/bin/sh
set -eu
umask 077

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/agentrec-release-test.XXXXXX")
cleanup() {
	rm -rf -- "$tmp"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

mkdir "$tmp/bin" "$tmp/output"
printf 'keep\n' >"$tmp/output/sentinel"
cat >"$tmp/bin/go" <<'EOF'
#!/bin/sh
: >"$AGENTREC_TEST_GO_CALLED"
exit 99
EOF
chmod +x "$tmp/bin/go"

stdout="$tmp/stdout"
stderr="$tmp/stderr"
if AGENTREC_TEST_GO_CALLED="$tmp/go-called" PATH="$tmp/bin:$PATH" \
	"$repo_root/scripts/build-release.sh" \
	v0.1.0 \
	0123456789abcdef0123456789abcdef01234567 \
	2026-07-28T00:00:00Z \
	"$tmp/output" >"$stdout" 2>"$stderr"; then
	echo "build-release.sh accepted an existing output directory" >&2
	exit 1
fi

if ! grep -Fq "output directory already exists" "$stderr"; then
	echo "build-release.sh did not explain why it refused the output directory" >&2
	cat "$stderr" >&2
	exit 1
fi
if [ -e "$tmp/go-called" ]; then
	echo "build-release.sh ran go before refusing the existing output directory" >&2
	exit 1
fi
if [ "$(cat "$tmp/output/sentinel")" != "keep" ]; then
	echo "build-release.sh changed existing output content" >&2
	exit 1
fi

rm -f "$tmp/go-called"
if AGENTREC_TEST_GO_CALLED="$tmp/go-called" PATH="$tmp/bin:$PATH" \
	"$repo_root/scripts/build-release.sh" \
	v01.1.0 \
	0123456789abcdef0123456789abcdef01234567 \
	2026-07-28T00:00:00Z \
	"$tmp/leading-zero" >"$stdout" 2>"$stderr"; then
	echo "build-release.sh accepted a semver component with a leading zero" >&2
	exit 1
fi
if ! grep -Fq "invalid version" "$stderr" || [ -e "$tmp/go-called" ]; then
	echo "build-release.sh did not reject the version before building" >&2
	exit 1
fi

rm -f "$tmp/go-called"
if AGENTREC_TEST_GO_CALLED="$tmp/go-called" PATH="$tmp/bin:$PATH" \
	"$repo_root/scripts/build-release.sh" \
	v0.1.0 \
	0123456789abcdef0123456789abcdef01234567 \
	2026-02-31T00:00:00Z \
	"$tmp/invalid-date" >"$stdout" 2>"$stderr"; then
	echo "build-release.sh accepted a nonexistent calendar date" >&2
	exit 1
fi
if ! grep -Fq "invalid build date" "$stderr" || [ -e "$tmp/go-called" ]; then
	echo "build-release.sh did not reject the date before building" >&2
	exit 1
fi

escape=$(printf '\033')
rm -f "$tmp/go-called"
if AGENTREC_TEST_GO_CALLED="$tmp/go-called" PATH="$tmp/bin:$PATH" \
	"$repo_root/scripts/build-release.sh" \
	"v0.1.0${escape}[31m" \
	0123456789abcdef0123456789abcdef01234567 \
	2026-07-28T00:00:00Z \
	"$tmp/control-character" >"$stdout" 2>"$stderr"; then
	echo "build-release.sh accepted a control character" >&2
	exit 1
fi
case $(cat "$stderr") in
*"$escape"*)
	echo "build-release.sh reflected a control character to stderr" >&2
	exit 1
	;;
esac
if [ -e "$tmp/go-called" ]; then
	echo "build-release.sh ran go before rejecting a control character" >&2
	exit 1
fi

rm -f "$tmp/go-called"
unsafe_output="${escape}[31mevil"
if (cd "$tmp" && \
	AGENTREC_TEST_GO_CALLED="$tmp/go-called" PATH="$tmp/bin:$PATH" \
		"$repo_root/scripts/build-release.sh" \
		v0.1.0 \
		0123456789abcdef0123456789abcdef01234567 \
		2026-07-28T00:00:00Z \
		"$unsafe_output") >"$stdout" 2>"$stderr"; then
	echo "build-release.sh accepted a leading control character in the output path" >&2
	exit 1
fi
case $(cat "$stdout" "$stderr") in
*"$escape"*)
	echo "build-release.sh reflected an output-path control character" >&2
	exit 1
	;;
esac
if [ -e "$tmp/$unsafe_output" ] || [ -e "$tmp/go-called" ]; then
	echo "build-release.sh acted before rejecting the leading output-path control character" >&2
	exit 1
fi

cat >"$tmp/bin/go" <<'EOF'
#!/bin/sh
saw_buildvcs=false
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-buildvcs=false" ]; then
		saw_buildvcs=true
	elif [ "$1" = "-o" ]; then
		if [ "$saw_buildvcs" != true ]; then
			exit 97
		fi
		shift
		printf '#!/bin/sh\nexit 0\n' >"$1"
		chmod +x "$1"
		xattr -w com.apple.ResourceFork resource "$1" 2>/dev/null || true
		exit 0
	fi
	shift
done
exit 98
EOF
clean_output="$tmp/clean-archives"
PATH="$tmp/bin:$PATH" "$repo_root/scripts/build-release.sh" \
	v0.1.0 \
	0123456789abcdef0123456789abcdef01234567 \
	2026-07-28T00:00:00Z \
	"$clean_output" >"$stdout" 2>"$stderr"
for archive in "$clean_output"/*.tar.gz; do
	if python3 -c 'import sys, tarfile; a=tarfile.open(sys.argv[1]); raise SystemExit(not any(any(part.startswith("._") for part in member.name.split("/")) or any(key.startswith("LIBARCHIVE.xattr.") or key.startswith("SCHILY.xattr.") for key in member.pax_headers) or (member.mode & 0o777) != (0o755 if member.isdir() or member.name.endswith("/agentrec") else 0o644) for member in a.getmembers()))' "$archive"; then
		echo "$(basename "$archive") contains non-portable metadata or modes" >&2
		exit 1
	fi
done

printf 'build-release boundary tests passed\n'
