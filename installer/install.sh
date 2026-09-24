#!/usr/bin/env bash
set -euo pipefail
umask 077

version='@VERSION@'
release="https://github.com/justinswe/fast-cli/releases/download/v${version}"

fail() {
  printf 'fast installer: %s\n' "$1" >&2
  exit 1
}

install=true
if [[ "${1:-}" == '--no-install' ]]; then
  install=false
  shift
fi
if [[ "${1:-}" == '--' ]]; then
  shift
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail 'unsupported operating system' ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail 'unsupported CPU architecture' ;;
esac

asset="fast-v${version}-${os}-${arch}"
if [[ "$install" == true ]]; then
  : "${HOME:?HOME must be set}"
  install_dir="${HOME}/.local/bin"
  mkdir -p "$install_dir"
  workdir=$(mktemp -d "${install_dir}/.fast.XXXXXXXX")
else
  workdir=$(mktemp -d "${TMPDIR:-/tmp}/fast.XXXXXXXX")
fi

cleanup() {
  status=$?
  trap - EXIT
  rm -rf "$workdir"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

curl -fsSL --retry 2 --connect-timeout 10 --proto '=https' --proto-redir '=https' \
  --output "${workdir}/${asset}" "${release}/${asset}"
curl -fsSL --retry 2 --connect-timeout 10 --proto '=https' --proto-redir '=https' \
  --output "${workdir}/SHA256SUMS" "${release}/SHA256SUMS"

expected=$(awk -v name="$asset" '$2 == name {print $1}' "${workdir}/SHA256SUMS")
[[ "$expected" =~ ^[0-9a-f]{64}$ ]] || fail 'missing or invalid release checksum'
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "${workdir}/${asset}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "${workdir}/${asset}" | awk '{print $1}')
else
  fail 'sha256sum or shasum is required'
fi
[[ "$actual" == "$expected" ]] || fail 'checksum mismatch'

chmod 700 "${workdir}/${asset}"
if [[ "$install" == true ]]; then
  mv -f "${workdir}/${asset}" "${install_dir}/fast"
  binary="${install_dir}/fast"
  printf 'Installed fast %s at %s\n' "$version" "$binary" >&2
  if [[ ":${PATH}:" != *":${install_dir}:"* ]]; then
    printf 'Add %s to PATH to run fast directly.\n' "$install_dir" >&2
  fi
else
  binary="${workdir}/${asset}"
fi

"$binary" "$@"
