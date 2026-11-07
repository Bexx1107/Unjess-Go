#!/usr/bin/env bash
# Unjess Desktop & GUI Installer Script (Linux & Windows)
set -e
CYAN='\033[0;36m'
GREEN='\033[0;32m'
GRAY='\033[0;90m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${CYAN}==================================================${NC}"
echo -e "  UNJESS — Universal Agentic Coding Harness v3.0.0 "
echo -e "${CYAN}==================================================${NC}"
echo ""


# Check if running under Windows / WSL / Git Bash
if [[ "$OSTYPE" == "msys" || "$OSTYPE" == "cygwin" || "$OSTYPE" == "win32" ]] && command -v powershell.exe >/dev/null 2>&1; then
    echo "Windows environment detected. Launching PowerShell installer..."
    powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "irm https://unjess.si/install.ps1 | iex"
    exit 0
fi

ARCH="$(uname -m)"
if [[ "$ARCH" != "x86_64" && "$ARCH" != "amd64" ]]; then
    echo -e "${RED}Linux GUI desktop is currently optimized for x86_64 (amd64).${NC}"
    echo "For ARM64, install the standalone CLI via:"
    echo "  curl -fsSL https://unjess.si/install-cli.sh | bash"
    exit 1
fi

TARGET_DIR="$HOME/.unjess/bin"
TARGET_EXE="$TARGET_DIR/unjess-gui"
DOWNLOAD_URL="https://github.com/Bexx1107/Unjess-Go/releases/download/v3.0.0/unjess-linux-desktop-amd64"
EXPECTED_HASH="a6a9277da812c104b17d4be8d06b6c050c1368004823a96e5cccc8d94908275c"
TEMP_FILE="$(mktemp -t unjess-desktop.XXXXXX)"

echo -e "${GRAY}Downloading Unjess Desktop GUI for Linux from https://unjess.si...${NC}"
if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$DOWNLOAD_URL" -o "$TEMP_FILE"
elif command -v wget >/dev/null 2>&1; then
    wget -qO "$TEMP_FILE" "$DOWNLOAD_URL"
else
    echo -e "${RED}Error: curl or wget is required.${NC}"
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
mkdir -p "$HOME/.local/bin"
mv "$TEMP_FILE" "$TARGET_EXE"
chmod +x "$TARGET_EXE"
ln -sf "$TARGET_EXE" "$HOME/.local/bin/unjess-gui"

# Also install CLI binary if not present
if [ ! -f "$TARGET_DIR/unjess" ]; then
    echo -e "${GRAY}Installing companion CLI binary...${NC}"
    curl -fsSL https://unjess.si/install-cli.sh | bash >/dev/null 2>&1 || true
fi

echo ""
echo -e "${GREEN}✓ Unjess Desktop GUI installed successfully!${NC}"
echo -e "Location: ${CYAN}$TARGET_EXE${NC}"
echo -e "Symlink:  ${CYAN}$HOME/.local/bin/unjess-gui${NC}"
echo ""
echo "Run GUI:"
echo -e "  ${CYAN}unjess-gui${NC}"
