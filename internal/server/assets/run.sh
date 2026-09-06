#!/bin/sh
# This script is piped directly to POSIX sh; keep its published bytes LF-only.
set -eu

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  cyan='\033[1;36m'
  green='\033[1;32m'
  dim='\033[2m'
  reset='\033[0m'
else
  cyan=''
  green=''
  dim=''
  reset=''
fi

step() {
  printf '%b==>%b %s\n' "$cyan" "$reset" "$1"
}

fail() {
  printf '%s\n' "$1" >&2
  exit "${2:-1}"
}

for command_name in curl tar sha256sum mktemp; do
  command -v "$command_name" >/dev/null 2>&1 || fail "Required command not found: $command_name" 2
done

case "$(uname -s)" in
  Linux) os=linux ;;
  *) fail "This installer currently supports Linux only." 3 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "Unsupported CPU architecture: $(uname -m)" 3 ;;
esac

if [ -z "${HOME:-}" ]; then
  fail "HOME is not set; unable to choose a user installation directory." 3
fi

release_base=${NT_RELEASE_BASE:-https://tunnel.nodelane.net/releases}
data_root=${XDG_DATA_HOME:-"$HOME/.local/share"}/nodelane/tunnel
versions_dir="$data_root/versions"
current_file="$data_root/current"
bin_dir=${NT_BIN_DIR:-"$HOME/.local/bin"}
launcher="$bin_dir/nt"

step "Checking the latest NodeLane Tunnel client..."
version=$(curl -fsSL "$release_base/stable.txt")
case "$version" in
  *[!0-9A-Za-z._-]*|'') fail "Invalid release version returned by server." 4 ;;
esac
case "$version" in
  [0-9]*) ;;
  *) fail "Invalid release version returned by server." 4 ;;
esac
if [ "${#version}" -gt 64 ]; then
  fail "Invalid release version returned by server." 4
fi

asset="nt_${version}_${os}_${arch}.tar.gz"
install_dir="$versions_dir/$version/$os-$arch"
client="$install_dir/nt"
previous_client=''
if [ -f "$current_file" ]; then
  IFS= read -r previous_client < "$current_file" || previous_client=''
fi

mkdir -p "$versions_dir" "$bin_dir"
installed_version=''
if [ -x "$client" ]; then
  installed_version=$("$client" --version 2>/dev/null || true)
fi
if [ "$installed_version" != "$version" ]; then
  temp_dir=$(mktemp -d "$versions_dir/.install-$version.XXXXXX")
  trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM
  archive="$temp_dir/$asset"
  package_dir="$temp_dir/package"
  mkdir -p "$package_dir"

  step "Downloading NodeLane Tunnel $version ($os/$arch)..."
  curl --fail --location --progress-bar "$release_base/$version/$asset" -o "$archive"
  step "Verifying package integrity..."
  expected=$(curl -fsSL "$release_base/$version/$asset.sha256" | awk '{print tolower($1)}')
  case "$expected" in
    *[!0-9A-Fa-f]*|'') fail "Invalid SHA-256 checksum returned by server." 5 ;;
  esac
  if [ "${#expected}" -ne 64 ]; then
    fail "Invalid SHA-256 checksum returned by server." 5
  fi
  actual=$(sha256sum "$archive" | awk '{print $1}')
  if [ "$expected" != "$actual" ]; then
    fail "NodeLane Tunnel package checksum verification failed." 5
  fi

  tar -xzf "$archive" -C "$package_dir"
  if [ ! -f "$package_dir/nt" ]; then
    fail "The downloaded package did not contain nt." 5
  fi
  chmod 700 "$package_dir/nt"
  downloaded_version=$("$package_dir/nt" --version)
  if [ "$downloaded_version" != "$version" ]; then
    fail "Downloaded client version $downloaded_version does not match $version." 5
  fi

  if [ -e "$install_dir" ]; then
    rm -rf "$install_dir"
  fi
  mkdir -p "$(dirname "$install_dir")"
  mv "$package_dir" "$install_dir"
  rm -rf "$temp_dir"
  trap - EXIT HUP INT TERM
  printf '%bOK%b NodeLane Tunnel %s is installed.\n' "$green" "$reset" "$version"
else
  printf '%b==>%b Using installed client %b%s%b.\n' "$cyan" "$reset" "$dim" "$version" "$reset"
fi

current_temp="$data_root/.current.$$"
printf '%s\n' "$client" > "$current_temp"
chmod 600 "$current_temp"
mv -f "$current_temp" "$current_file"

launcher_temp="$bin_dir/.nt.$$"
cat > "$launcher_temp" <<'LAUNCHER'
#!/bin/sh
set -eu
data_root=${XDG_DATA_HOME:-"$HOME/.local/share"}/nodelane/tunnel
current_file="$data_root/current"
if [ ! -f "$current_file" ]; then
  echo "NodeLane Tunnel is not installed; run https://tunnel.nodelane.net/run.sh again." >&2
  exit 1
fi
IFS= read -r client < "$current_file"
if [ ! -x "$client" ]; then
  echo "The installed NodeLane Tunnel client is unavailable; run the installer again." >&2
  exit 1
fi
exec "$client" "$@"
LAUNCHER
chmod 755 "$launcher_temp"
mv -f "$launcher_temp" "$launcher"

if [ "$bin_dir" = "$HOME/.local/bin" ]; then
  shell_name=${SHELL##*/}
  case "$shell_name" in
    bash) profile="$HOME/.bashrc" ;;
    zsh) profile="$HOME/.zshrc" ;;
    *) profile="$HOME/.profile" ;;
  esac
  path_line='export PATH="$HOME/.local/bin:$PATH"'
  if [ ! -f "$profile" ] || ! grep -F "$path_line" "$profile" >/dev/null 2>&1; then
    printf '\n# NodeLane Tunnel\n%s\n' "$path_line" >> "$profile"
  fi
fi

case ":${PATH:-}:" in
  *":$bin_dir:"*) ;;
  *)
    if [ "$bin_dir" = "$HOME/.local/bin" ]; then
      printf '%b==>%b Added %s to PATH in %s; new terminals can run %bnt%b directly.\n' \
        "$cyan" "$reset" "$bin_dir" "$profile" "$green" "$reset"
    else
      printf '%b==>%b Add %s to PATH to run %bnt%b directly.\n' \
        "$cyan" "$reset" "$bin_dir" "$green" "$reset"
    fi
    ;;
esac

# Keep the immediately previous version for rollback and remove older versions.
previous_dir=''
if [ -n "$previous_client" ]; then
  previous_dir=$(dirname "$previous_client")
fi
for old_dir in "$versions_dir"/*/*; do
  [ -d "$old_dir" ] || continue
  if [ "$old_dir" != "$install_dir" ] && [ "$old_dir" != "$previous_dir" ]; then
    rm -rf "$old_dir"
  fi
done

exec "$client" "$@"
