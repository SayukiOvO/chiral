#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: $0 <version> <destination>" >&2
	exit 2
fi

version=$1
destination=$2
case "$version" in
	"")
		echo "Xray version must not be empty" >&2
		exit 2
		;;
	v*) ;;
	*) version="v$version" ;;
esac

case "$(uname -m)" in
	x86_64 | amd64) xray_arch=64 ;;
	aarch64 | arm64) xray_arch=arm64-v8a ;;
	*)
		echo "unsupported runner architecture: $(uname -m)" >&2
		exit 1
		;;
esac

tmp_base=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
work=$(mktemp -d "${tmp_base%/}/chiral-xray.XXXXXX")
trap 'rm -rf -- "$work"' EXIT HUP INT TERM

asset="Xray-linux-${xray_arch}.zip"
base="https://github.com/XTLS/Xray-core/releases/download/${version}"
archive="$work/$asset"
digest="$work/$asset.dgst"

download() {
	curl --fail --show-error --silent --location \
		--retry 3 --retry-all-errors \
		--output "$2" "$1"
}

download "$base/$asset" "$archive"
download "$base/$asset.dgst" "$digest"

# Xray release digest files use `SHA2-256= ...`; accept the older spelling
# too, matching core/internal/release.ParseDigest.
sum=$(awk -F= '
	/^[[:space:]]*(SHA2-256|SHA256)[[:space:]]*=/ {
		gsub(/[[:space:]]/, "", $2)
		print tolower($2)
		exit
	}' "$digest")
if ! printf '%s\n' "$sum" | grep -Eq '^[0-9a-f]{64}$'; then
	echo "release digest contains no valid SHA-256 for $asset" >&2
	exit 1
fi

(cd "$work" && printf '%s  %s\n' "$sum" "$asset" | sha256sum -c -)

mkdir -p "$destination"
unzip -oq "$archive" xray geoip.dat geosite.dat -d "$destination"
chmod 0755 "$destination/xray"
for file in xray geoip.dat geosite.dat; do
	if [ ! -s "$destination/$file" ]; then
		echo "Xray release is missing $file" >&2
		exit 1
	fi
done

reported=$("$destination/xray" version | awk 'NR == 1 { print $2; exit }')
expected=${version#v}
if [ "$reported" != "$expected" ]; then
	echo "downloaded Xray reports $reported, expected $expected" >&2
	exit 1
fi
echo "installed Xray $reported with geo assets in $destination"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	printf '%s\n' "- Verified archive SHA-256: \`$sum\`" >> "$GITHUB_STEP_SUMMARY"
fi
