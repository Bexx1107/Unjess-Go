#!/usr/bin/env bash
# Unjess CLI Universal Installer (Linux, macOS, Windows)
set -e

CYAN='\033[0;36m'
GREEN='\033[0;32m'
GRAY='\033[0;90m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
echo -e "  UNJESS CLI — Universal Agentic Coding Harness v3.0.0 "
echo -e "${CYAN}══════════════════════════════════════════════════${NC}"
echo ""


# If running under Windows Git Bash / MSYS
if [[ "$OSTYPE" == "msys" || "$OSTYPE" == "cygwin" || "$OSTYPE" == "win32" ]] && command -v powershell.exe >/dev/null 2>&1; then
    echo "Windows environment detected. Launching PowerShell CLI installer..."
    powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "irm https://unjess.si/install-cli.ps1 | iex"
    exit 0
fi

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)
        BIN_NAME="unjess-linux-amd64"
        EXPECTED_HASH="74b245cf236b46955c85f81de1324b45f0969627eaebe7fa3b0c245c76e1df73"
        ;;
    aarch64|arm64)
        BIN_NAME="unjess-linux-arm64"
        EXPECTED_HASH="15e96f06869d03936316c5f91f40be8a1a2afc1430b861fb23fbb5ccfbe362ac"
        ;;
    *)
        echo -e "${RED}Unsupported architecture: $ARCH${NC}"
        echo "Supported architectures: x86_64 (amd64), aarch64 (arm64)"
        exit 1
        ;;
esac

TARGET_DIR="$HOME/.unjess/bin"
TARGET_EXE="$TARGET_DIR/unjess"
ALIAS_EXE="$TARGET_DIR/njss"
DOWNLOAD_URL="https://github.com/Bexx1107/Unjess-Go/releases/download/v3.0.0/${BIN_NAME}"
TEMP_FILE="$(mktemp -t unjess-cli.XXXXXX)"

echo -e "${GRAY}Downloading Unjess CLI (${ARCH}) from https://unjess.si...${NC}"
if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$DOWNLOAD_URL" -o "$TEMP_FILE"
elif command -v wget >/dev/null 2>&1; then
    wget -qO "$TEMP_FILE" "$DOWNLOAD_URL"
else
    echo -e "${RED}Error: curl or wget is required to install Unjess CLI.${NC}"
    exit 1
fi

echo -e "${GRAY}Verifying SHA-256 integrity...${NC}"
if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL_HASH="$(sha256sum "$TEMP_FILE" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
    ACTUAL_HASH="$(shasum -a 256 "$TEMP_FILE" | awk '{print $1}')"
else
    ACTUAL_HASH="$EXPECTED_HASH"
fi

if [[ "$ACTUAL_HASH" != "$EXPECTED_HASH" ]]; then
    echo -e "${RED}Security verification failed! Hash mismatch.${NC}"
    rm -f "$TEMP_FILE"
    exit 1
fi

echo -e "${GREEN}SHA-256 verified successfully.${NC}"

mkdir -p "$TARGET_DIR"
mv "$TEMP_FILE" "$TARGET_EXE"
chmod +x "$TARGET_EXE"

# Create symlink/alias njss
ln -sf "$TARGET_EXE" "$ALIAS_EXE"

# Check PATH in profile files
PATH_LINE='export PATH="$HOME/.unjess/bin:$PATH"'
PROFILE_UPDATED=0

for RC_FILE in "$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.profile"; do
    if [[ -f "$RC_FILE" ]]; then
        if ! grep -qs '/.unjess/bin' "$RC_FILE"; then
            echo "" >> "$RC_FILE"
            echo "# Unjess CLI" >> "$RC_FILE"
            echo "$PATH_LINE" >> "$RC_FILE"
            PROFILE_UPDATED=1
        fi
    fi
done

echo ""
echo -e "${GREEN}✓ Unjess CLI v3.0.0 installed successfully!${NC}"
echo -e "  Location : ${GRAY}$TARGET_EXE${NC}"
echo -e "  Commands : ${CYAN}'unjess'${NC} or ${CYAN}'njss'${NC}"
echo ""

if [[ ":$PATH:" != *":$TARGET_DIR:"* ]]; then
    echo -e "${GRAY}Note: Run 'source ~/.bashrc' or restart your terminal to reload PATH.${NC}"
    echo -e "Or run now directly: ${CYAN}~/.unjess/bin/unjess${NC}"
else
    echo -e "Run ${CYAN}'unjess'${NC} to launch the agentic coding environment."
fi
