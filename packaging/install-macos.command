#!/bin/bash
#
# Rescale Interlink macOS Installer Helper
#
# This script:
# 1. Removes macOS quarantine flags from the app (avoiding Gatekeeper prompts)
# 2. Moves the app to ~/Applications (user-writable, no admin needed)
# 3. Launches the app
#
# Why this is needed:
# - Browsers set com.apple.quarantine on downloaded files
# - Without code signing (requires $99/year Apple Developer Program),
#   macOS Gatekeeper will warn/block the app
# - Downloading via curl+tar avoids this, but browser downloads need this helper
#
# Usage:
# - Double-click this file after extracting the tarball
# - Or: chmod +x install-macos.command && ./install-macos.command

set -euo pipefail

# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_NAME="Rescale Interlink.app"
APP_PATH="$SCRIPT_DIR/$APP_NAME"

# Check if app exists
if [ ! -d "$APP_PATH" ]; then
    # Try alternate name (wails output may vary)
    APP_NAME="rescale-int.app"
    APP_PATH="$SCRIPT_DIR/$APP_NAME"
fi

if [ ! -d "$APP_PATH" ]; then
    echo "ERROR: Cannot find Rescale Interlink.app or rescale-int.app in:"
    echo "  $SCRIPT_DIR"
    echo ""
    echo "Please ensure this script is in the same directory as the app."
    read -p "Press Enter to exit..."
    exit 1
fi

echo "=============================================="
echo " Rescale Interlink macOS Installer"
echo "=============================================="
echo ""
echo "App found: $APP_PATH"
echo ""

# Step 1: Remove quarantine attributes
echo "[1/3] Removing quarantine flags..."
xattr -r -d com.apple.quarantine "$APP_PATH" 2>/dev/null || true
echo "      Done."
echo ""

# Step 2: Copy to ~/Applications (or use in place)
DEST_DIR="$HOME/Applications"
DEST_PATH="$DEST_DIR/$(basename "$APP_PATH")"

read -p "[2/3] Install to ~/Applications? (Y/n): " INSTALL_CHOICE
INSTALL_CHOICE="${INSTALL_CHOICE:-Y}"

if [[ "$INSTALL_CHOICE" =~ ^[Yy]$ ]]; then
    mkdir -p "$DEST_DIR"

    # Check if already exists
    if [ -d "$DEST_PATH" ]; then
        read -p "      App already exists. Replace? (y/N): " REPLACE_CHOICE
        if [[ "$REPLACE_CHOICE" =~ ^[Yy]$ ]]; then
            rm -rf "$DEST_PATH"
        else
            echo "      Skipping installation."
            DEST_PATH="$APP_PATH"
        fi
    fi

    if [ "$DEST_PATH" != "$APP_PATH" ]; then
        cp -R "$APP_PATH" "$DEST_PATH"
        echo "      Installed to: $DEST_PATH"
    fi
else
    echo "      Skipping installation (app will run from current location)."
    DEST_PATH="$APP_PATH"
fi
echo ""

# Step 3: Launch the app
read -p "[3/3] Launch Rescale Interlink now? (Y/n): " LAUNCH_CHOICE
LAUNCH_CHOICE="${LAUNCH_CHOICE:-Y}"

if [[ "$LAUNCH_CHOICE" =~ ^[Yy]$ ]]; then
    echo "      Launching..."
    open "$DEST_PATH"
    echo ""
    echo "Rescale Interlink is now running."
else
    echo "      To launch later, double-click the app or run:"
    echo "        open \"$DEST_PATH\""
fi

echo ""
echo "=============================================="
echo " Installation complete!"
echo "=============================================="
echo ""
echo "You can delete this installer folder after installation."
echo "For command-line use, the CLI is available at:"
echo "  $DEST_PATH/Contents/MacOS/rescale-int"
echo ""
