#!/bin/sh
set -eu

version=${1:-}
output_root=${2:-}

case "$version" in
  ''|*[!0-9A-Za-z._-]*)
    echo "Usage: sh deploy/build-client-assets.sh <version> <output-root>" >&2
    exit 2
    ;;
esac
if [ -z "$output_root" ]; then
  echo "Output root is required." >&2
  exit 2
fi

for command_name in go tar zip sha256sum; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "Required command not found: $command_name" >&2
    exit 2
  fi
done

release_dir="$output_root/$version"
if [ -e "$release_dir" ]; then
  echo "Release already exists: $release_dir" >&2
  exit 3
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$release_dir"
frp_dir=$(go list -m -f '{{.Dir}}' github.com/fatedier/frp)

for target in linux-amd64 linux-arm64 windows-amd64 windows-arm64; do
  os=${target%-*}
  arch=${target#*-}
  package="$work/package-$target"
  mkdir -p "$package"

  suffix=""
  if [ "$os" = windows ]; then suffix=.exe; fi
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X github.com/Wy2926/nodelane-tunneld/internal/client.Version=$version" \
    -o "$package/nt$suffix" ./cmd/nt
  cp "$frp_dir/LICENSE" "$package/LICENSE.frp"

  if [ "$os" = windows ]; then
    asset="nt_${version}_${os}_${arch}.zip"
    (cd "$package" && zip -q -r "$release_dir/$asset" .)
  else
    chmod 755 "$package/nt"
    asset="nt_${version}_${os}_${arch}.tar.gz"
    tar -C "$package" -czf "$release_dir/$asset" .
  fi

  (cd "$release_dir" && sha256sum "$asset" > "$asset.sha256")
done

printf '%s\n' "$version" > "$output_root/stable.txt"
chmod 644 "$output_root/stable.txt" "$release_dir"/*
