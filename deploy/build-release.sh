#!/bin/sh
set -eu

version=${1:-}
publish_root=${2:-/srv/nodelane/tunnel/releases}

case "$version" in
  ''|*[!0-9A-Za-z._-]*)
    echo "Usage: sh deploy/build-release.sh <version> [publish-root]" >&2
    exit 2
    ;;
esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
release_dir="$publish_root/$version"
if [ -e "$release_dir" ]; then
  echo "Release already exists: $release_dir" >&2
  echo "Use a new version instead of overwriting an immutable release." >&2
  exit 3
fi

staging_dir="$publish_root/.staging-$version-$$"
mkdir -p "$staging_dir"
trap 'rm -rf "$staging_dir"' EXIT HUP INT TERM

docker run --rm \
  -v "$repo_root:/src:ro" \
  -v "$staging_dir:/publish" \
  -w /src \
  golang:1.27-alpine \
  sh -eu -c '
    apk add --no-cache tar zip >/dev/null
    sh ./deploy/build-client-assets.sh "$1" /publish
  ' -- "$version"

mv "$staging_dir/$version" "$release_dir"
stable_tmp="$publish_root/.stable.txt.tmp"
mv "$staging_dir/stable.txt" "$stable_tmp"
chmod 644 "$stable_tmp"
mv "$stable_tmp" "$publish_root/stable.txt"
rmdir "$staging_dir"
trap - EXIT HUP INT TERM

echo "Published NodeLane Tunnel $version to $release_dir"
echo "Stable channel: $publish_root/stable.txt"
