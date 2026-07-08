#!/usr/bin/env bash
set -euo pipefail

REPO="cnstark/cc-proxy"
BIN_DIR="${HOME}/.cc_proxy/bin"
ENV_FILE="${HOME}/.cc_proxy/env.sh"

# --- Detect OS/Arch ---
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    *)
        echo "Unsupported architecture: $ARCH"
        echo "Supported: x86_64, aarch64"
        exit 1
        ;;
esac

case "$OS" in
    linux)   FORMAT="tar.gz" ;;
    darwin)  FORMAT="tar.gz" ;;
    *)
        echo "Unsupported OS: $OS"
        echo "Supported: linux, darwin"
        exit 1
        ;;
esac

# --- Parse CLI args ---
while [[ $# -gt 0 ]]; do
    case "$1" in
        -d|--dir) BIN_DIR="$2"; shift 2 ;;
        -h|--help)
            echo "Usage: curl -fsSL <url> | bash [-d <install-dir>]"
            echo "Default install dir: ~/.cc_proxy/bin"
            exit 0
            ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# --- Get latest version ---
echo "==> Fetching latest version..."
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
if [ -z "$VERSION" ]; then
    echo "Error: could not determine latest version"
    exit 1
fi
echo "==> Latest version: ${VERSION}"

# --- Download ---
PKG_BASE="cc-proxy_${VERSION}_${OS}_${ARCH}"
PKG_FILE="${PKG_BASE}.${FORMAT}"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${PKG_FILE}"

echo "==> Downloading ${PKG_FILE} ..."
TMP_DIR=$(mktemp -d)
trap "rm -rf ${TMP_DIR}" EXIT

curl -fsSL -o "${TMP_DIR}/${PKG_FILE}" "${DOWNLOAD_URL}"

# --- Extract to install dir ---
echo "==> Installing to ${BIN_DIR} ..."
mkdir -p "${BIN_DIR}"

if [ "$FORMAT" = "tar.gz" ]; then
    tar xzf "${TMP_DIR}/${PKG_FILE}" -C "${TMP_DIR}"
    cp "${TMP_DIR}/${PKG_BASE}/ccp" "${BIN_DIR}/"
    cp "${TMP_DIR}/${PKG_BASE}/ccp-proxy" "${BIN_DIR}/"
else
    unzip -q "${TMP_DIR}/${PKG_FILE}" -d "${TMP_DIR}"
    cp "${TMP_DIR}/${PKG_BASE}/ccp.exe" "${BIN_DIR}/"
    cp "${TMP_DIR}/${PKG_BASE}/ccp-proxy.exe" "${BIN_DIR}/"
fi

chmod +x "${BIN_DIR}/ccp" "${BIN_DIR}/ccp-proxy" 2>/dev/null || true

# --- Generate env.sh ---
cat > "${ENV_FILE}" << 'ENVEOF'
# cc-proxy environment - source this file to add binaries to PATH
export PATH="HOME_PLACEHOLDER/.cc_proxy/bin:$PATH"
ENVEOF
# Replace placeholder with actual HOME value
sed -i "s|HOME_PLACEHOLDER|${HOME}|g" "${ENV_FILE}"

# --- Verify ---
if "${BIN_DIR}/ccp" version >/dev/null 2>&1; then
    INSTALLED_VER=$("${BIN_DIR}/ccp" version | awk '{print $NF}')
    echo ""
    echo "✅ Installation successful! Version: ${INSTALLED_VER}"
else
    echo ""
    echo "✅ Installation successful!"
fi

echo ""
echo "To use now (current terminal):"
echo "  source ${ENV_FILE}"
echo ""
echo "To make permanent (add to shell rc file):"
echo "  echo 'source ${ENV_FILE}' >> ~/.bashrc   # bash users"
echo "  echo 'source ${ENV_FILE}' >> ~/.zshrc    # zsh users"
echo ""
echo "Quick start:"
echo "  ccp help"
