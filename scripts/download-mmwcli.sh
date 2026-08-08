#!/bin/sh

set -eu

repository="AIoT-Laboratory/mmwcli"
api_base="${MMWCLI_RELEASE_API_BASE:-https://api.github.com/repos/$repository}"
download_base="${MMWCLI_RELEASE_DOWNLOAD_BASE:-https://github.com/$repository/releases/download}"
requested_version=""
install_dir=""

usage() {
    cat <<'EOF'
Usage: download-mmwcli.sh [--version RELEASE_TAG] [--install-dir DIRECTORY]

Install the latest mmwcli Linux release into $HOME/.local/bin by default.
RELEASE_TAG is an exact GitHub release tag, for example 0.2.
EOF
}

fail() {
    printf 'mmwcli installer: %s\n' "$*" >&2
    exit 1
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || fail "--version requires a release tag"
            requested_version=$2
            shift 2
            ;;
        --install-dir)
            [ "$#" -ge 2 ] || fail "--install-dir requires a directory"
            install_dir=$2
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            fail "unknown argument: $1"
            ;;
    esac
done

[ "$(uname -s)" = "Linux" ] || fail "this script supports Linux only"
case "$(uname -m)" in
    x86_64|amd64) architecture=amd64 ;;
    aarch64|arm64) architecture=arm64 ;;
    *) fail "unsupported Linux architecture: $(uname -m)" ;;
esac

if [ -z "$install_dir" ]; then
    [ -n "${HOME:-}" ] || fail "HOME is unset; pass --install-dir DIRECTORY"
    install_dir=$HOME/.local/bin
fi

validate_tag() {
    case "$1" in
        ""|*[!A-Za-z0-9._-]*) fail "invalid release tag: $1" ;;
    esac
}

if [ -n "$requested_version" ]; then
    validate_tag "$requested_version"
    release_url="$api_base/releases/tags/$requested_version"
else
    release_url="$api_base/releases/latest"
fi

curl_get() {
    url=$1
    output=${2:-}
    if [ -n "$output" ]; then
        if [ -n "${GITHUB_TOKEN:-}" ]; then
            curl -fsSL -H 'Accept: application/vnd.github+json' \
                -H "Authorization: Bearer $GITHUB_TOKEN" -o "$output" "$url"
        else
            curl -fsSL -H 'Accept: application/vnd.github+json' -o "$output" "$url"
        fi
    elif [ -n "${GITHUB_TOKEN:-}" ]; then
        curl -fsSL -H 'Accept: application/vnd.github+json' \
            -H "Authorization: Bearer $GITHUB_TOKEN" "$url"
    else
        curl -fsSL -H 'Accept: application/vnd.github+json' "$url"
    fi
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
release_json=$(curl_get "$release_url") || fail "could not read GitHub release metadata from $release_url"
compact_json=$(printf '%s' "$release_json" | tr -d '[:space:]')
tag=$(printf '%s' "$compact_json" | sed -n 's/.*"tag_name":"\([^"]*\)".*/\1/p')
validate_tag "$tag"
[ -z "$requested_version" ] || [ "$tag" = "$requested_version" ] || \
    fail "GitHub returned release tag $tag for requested tag $requested_version"

asset="mmwcli-$tag-linux-$architecture"
checksums_asset="SHA256SUMS"
case "$compact_json" in *"\"name\":\"$asset\""*) ;; *) fail "release $tag has no asset named $asset" ;; esac
case "$compact_json" in *"\"name\":\"$checksums_asset\""*) ;; *) fail "release $tag has no SHA256SUMS asset" ;; esac

temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/mmwcli-install.XXXXXX") || fail "could not create a temporary directory"
stage=""
cleanup() {
    [ -z "$stage" ] || rm -f "$stage"
    rm -rf "$temporary_dir"
}
trap cleanup EXIT HUP INT TERM

binary_path="$temporary_dir/$asset"
checksums_path="$temporary_dir/$checksums_asset"
curl_get "$download_base/$tag/$asset" "$binary_path" || fail "could not download release asset $asset"
curl_get "$download_base/$tag/$checksums_asset" "$checksums_path" || fail "could not download SHA256SUMS"

expected=$(awk -v name="$asset" '
    length($1) == 64 && $1 ~ /^[0-9A-Fa-f]+$/ {
        candidate = $2
        sub(/^\*/, "", candidate)
        if (candidate == name) { count++; digest = $1 }
    }
    END { if (count == 1) print digest }
' "$checksums_path")
[ -n "$expected" ] || fail "SHA256SUMS does not contain exactly one checksum for $asset"

if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$binary_path" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$binary_path" | awk '{print $1}')
else
    fail "sha256sum or shasum is required"
fi
expected=$(printf '%s' "$expected" | tr 'A-F' 'a-f')
actual=$(printf '%s' "$actual" | tr 'A-F' 'a-f')
[ "$actual" = "$expected" ] || fail "SHA-256 mismatch for $asset"

mkdir -p "$install_dir" || fail "could not create install directory $install_dir"
[ -d "$install_dir" ] || fail "install path is not a directory: $install_dir"
stage="$install_dir/.mmwcli.install.$$"
cp "$binary_path" "$stage" || fail "could not stage mmwcli in $install_dir"
chmod 0755 "$stage" || fail "could not make the installed binary executable"
mv -f "$stage" "$install_dir/mmwcli" || fail "could not install mmwcli into $install_dir"
stage=""

printf 'Installed mmwcli %s (linux/%s) to %s\n' "$tag" "$architecture" "$install_dir/mmwcli"
case ":${PATH:-}:" in
    *":$install_dir:"*) ;;
    *) printf 'Add %s to PATH to run mmwcli directly.\n' "$install_dir" ;;
esac
