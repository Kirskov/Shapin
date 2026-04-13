#!/bin/sh
set -e

REPO="Kirskov/Shapin"
BINARY="shapin"
INSTALL_DIR="/usr/local/bin"

# ── Detect OS and arch ───────────────────────────────────────────────────────

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

case "$OS" in
  linux)  ;;
  darwin) ;;
  *)
    echo "Unsupported OS: $OS"
    exit 1
    ;;
esac

# ── Detect package manager and install deps if missing ───────────────────────

install_deps() {
  if command -v curl > /dev/null 2>&1; then
    return
  fi

  echo "curl not found, installing..."

  if command -v apt-get > /dev/null 2>&1; then
    apt-get update -qq && apt-get install -y -qq curl
  elif command -v pacman > /dev/null 2>&1; then
    pacman -Sy --noconfirm curl
  elif command -v apk > /dev/null 2>&1; then
    apk add --no-cache curl
  elif command -v dnf > /dev/null 2>&1; then
    dnf install -y curl
  elif command -v yum > /dev/null 2>&1; then
    yum install -y curl
  else
    echo "Could not install curl: no supported package manager found."
    exit 1
  fi
}

# ── Checksum verification ─────────────────────────────────────────────────────

verify_checksum() {
  file="$1"
  expected="$2"

  if command -v sha256sum > /dev/null 2>&1; then
    actual=$(sha256sum "$file" | cut -d' ' -f1)
  elif command -v shasum > /dev/null 2>&1; then
    actual=$(shasum -a 256 "$file" | cut -d' ' -f1)
  else
    echo "warn: no sha256sum or shasum found, skipping checksum verification"
    return 0
  fi

  echo "  expected: $expected"
  echo "  got:      $actual"

  if [ "$actual" != "$expected" ]; then
    echo "Checksum mismatch!"
    exit 1
  fi

  echo "Checksum OK."
}

# ── Fetch latest release tag ─────────────────────────────────────────────────

latest_version() {
  if [ -n "$GITHUB_TOKEN" ]; then
    curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep '"tag_name"' \
      | sed 's/.*"tag_name": *"\(.*\)".*/\1/'
  else
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep '"tag_name"' \
      | sed 's/.*"tag_name": *"\(.*\)".*/\1/'
  fi
}

# ── Main ─────────────────────────────────────────────────────────────────────

install_deps

if [ -z "$VERSION" ]; then
  VERSION="$(latest_version)"
fi

if [ -z "$VERSION" ]; then
  echo "Could not determine latest version."
  exit 1
fi

ASSET="${BINARY}-${VERSION}-${OS}-${ARCH}"
if [ "$OS" = "windows" ]; then
  ASSET="${ASSET}.exe"
fi

BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
URL="${BASE_URL}/${ASSET}"
CHECKSUMS_URL="${BASE_URL}/checksums.txt"

echo "Installing ${BINARY} ${VERSION} (${OS}/${ARCH})..."
echo "Downloading from: ${URL}"

TMP=$(mktemp)
TMP_CHECKSUMS=$(mktemp)

# Cleanup on exit
trap 'rm -f "$TMP" "$TMP_CHECKSUMS"' EXIT

curl -fsSL "$URL" -o "$TMP"
curl -fsSL "$CHECKSUMS_URL" -o "$TMP_CHECKSUMS"

# Extract expected checksum for this asset from checksums.txt
EXPECTED=$(grep "${ASSET}" "$TMP_CHECKSUMS" | cut -d' ' -f1)
if [ -z "$EXPECTED" ]; then
  echo "warn: could not find checksum for ${ASSET} in checksums.txt, skipping verification"
else
  verify_checksum "$TMP" "$EXPECTED"
fi

chmod +x "$TMP"

# Need root to write to /usr/local/bin
if [ "$(id -u)" -eq 0 ]; then
  mv "$TMP" "${INSTALL_DIR}/${BINARY}"
elif command -v sudo > /dev/null 2>&1; then
  sudo mv "$TMP" "${INSTALL_DIR}/${BINARY}"
else
  echo "Cannot install to ${INSTALL_DIR}: not root and sudo not available."
  echo "Run as root or install manually: cp $TMP ~/bin/${BINARY} && chmod +x ~/bin/${BINARY}"
  exit 1
fi

echo ""
echo "${BINARY} ${VERSION} installed to ${INSTALL_DIR}/${BINARY}"
echo "Run: ${BINARY} --help"
