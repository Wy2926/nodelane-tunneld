#!/bin/sh
set -eu

registry=${1:-}
version=${2:-}
repository=${3:-nodelane/tunneld}
client_version=${4:-}

case "$registry" in
  ''|http://*|https://*)
    echo "Usage: sh deploy/publish-image.sh <registry-host> <version> [repository] [client-version]" >&2
    echo "Do not include http:// or https:// in the registry host." >&2
    exit 2
    ;;
esac
case "$version" in
  ''|*[!0-9A-Za-z._-]*)
    echo "Version may contain only letters, digits, dot, underscore, and dash." >&2
    exit 2
    ;;
esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ -z "$client_version" ]; then
  client_version=$(tr -d '\r\n' < "$repo_root/client-version.txt")
fi
case "$client_version" in
  ''|*[!0-9A-Za-z._-]*)
    echo "Client version may contain only letters, digits, dot, underscore, and dash." >&2
    exit 2
    ;;
esac
image="${registry%/}/${repository#/}"

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg "VERSION=$version" \
  --build-arg "NT_VERSION=$client_version" \
  --label "org.opencontainers.image.revision=$version" \
  --tag "$image:$version" \
  --push \
  "$repo_root"

echo "Published image: $image:$version (nt $client_version)"
