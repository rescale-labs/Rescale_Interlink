#!/bin/bash
#
# release-linux-wails-build.sh - Build Wails GUI for Linux on Rescale
#
# This script builds the Wails-based GUI application for Linux:
# 1. Installs Go 1.24.2, Node.js, Wails CLI, and GTK/WebKitGTK dev packages
# 2. Clones the repository
# 3. Builds the Wails GUI with FIPS 140-3 compliance
# 4. Creates a tarball with the binary
#
# Required environment variables:
#   RESCALE_API_KEY - Rescale API token
#   GITHUB_TOKEN - GitHub PAT with repo scope (for git clone)
#
# Output: rescale-interlink-{version}-linux-amd64.tar.gz containing:
#   - rescale-int (binary)
#   - README.txt (with system dependency instructions)
#
# Usage:
#   ./release-linux-wails-build.sh
#
# Version: 4.0.5
# Date: 2026-01-02

set -euo pipefail

# =============================================================================
# Configuration
# =============================================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESCALE_INT_DIR="$(dirname "$SCRIPT_DIR")"
MAKEFILE="$RESCALE_INT_DIR/Makefile"

# Job configuration - Linux (Rocky/Alma 8)
CORE_TYPE="calcitev2"
CORES_PER_SLOT=4  # More cores for faster builds
SLOTS=1
WALLTIME_SECONDS=7200  # 2 hours

# GitHub repository
REPO_OWNER="rescale-labs"
REPO_NAME="Rescale_Interlink"

# =============================================================================
# Helper Functions
# =============================================================================
cleanup() {
    local exit_code=$?
    rm -f "${TMPDIR:-/tmp}/interlink-wails-linux.sh" 2>/dev/null || true
    if [ $exit_code -ne 0 ]; then
        echo ""
        echo "=============================================="
        echo "BUILD FAILED"
        echo "=============================================="
    fi
}
trap cleanup EXIT

error() {
    echo "ERROR: $1" >&2
    exit 1
}

# =============================================================================
# Pre-flight Checks
# =============================================================================
echo "=============================================="
echo "Rescale Interlink Linux Wails Builder"
echo "=============================================="
echo ""

[ -z "${RESCALE_API_KEY:-}" ] && error "RESCALE_API_KEY environment variable is not set"
[ -z "${GITHUB_TOKEN:-}" ] && error "GITHUB_TOKEN environment variable is not set"

[ ! -f "$MAKEFILE" ] && error "Makefile not found at: $MAKEFILE"

VERSION=$(grep -E '^VERSION\s*:=' "$MAKEFILE" | sed 's/.*:=\s*//' | tr -d '[:space:]')
[ -z "$VERSION" ] && error "Could not extract VERSION from Makefile"

echo "Version: $VERSION"
echo "Repository: ${REPO_OWNER}/${REPO_NAME}"
echo ""

# Find rescale-int binary
RESCALE_INT_BIN="$RESCALE_INT_DIR/bin/${VERSION}/darwin-arm64/rescale-int"
if [ ! -x "$RESCALE_INT_BIN" ]; then
    RESCALE_INT_BIN=$(find "$RESCALE_INT_DIR/bin" -name "rescale-int" -path "*/darwin-arm64/*" -type f 2>/dev/null | sort -V | tail -1)
fi
[ ! -x "$RESCALE_INT_BIN" ] && error "rescale-int binary not found. Please build it first with 'make build'"

echo "Using: $RESCALE_INT_BIN"
echo ""

# =============================================================================
# Generate Build Script
# =============================================================================
echo "Generating Linux Wails build script..."

BUILD_SCRIPT="${TMPDIR:-/tmp}/interlink-wails-linux.sh"
JOB_NAME="Interlink Linux AppImage Build - ${VERSION}"

cat > "$BUILD_SCRIPT" << 'SCRIPT_HEADER'
#!/bin/bash
#
# Rescale job script - Linux Wails build
#
SCRIPT_HEADER

# Add SGE metadata (dynamic values)
cat >> "$BUILD_SCRIPT" << EOF
#RESCALE_NAME ${JOB_NAME}
#RESCALE_ANALYSIS user_included
#RESCALE_ANALYSIS_VERSION 0
#RESCALE_CORES ${CORE_TYPE}
#RESCALE_CORES_PER_SLOT ${CORES_PER_SLOT}
#RESCALE_SLOTS ${SLOTS}
#RESCALE_WALLTIME ${WALLTIME_SECONDS}
#RESCALE_ENV_GITHUB_TOKEN ${GITHUB_TOKEN}
#RESCALE_ENV_RELEASE_TAG ${VERSION}
#RESCALE_ENV_REPO_OWNER ${REPO_OWNER}
#RESCALE_ENV_REPO_NAME ${REPO_NAME}
#RESCALE_COMMAND bash interlink-wails-linux.sh

EOF

# Add the build logic
cat >> "$BUILD_SCRIPT" << 'BUILD_LOGIC'
set -euo pipefail

echo "=============================================="
echo "Rescale Interlink Linux Wails Build (AppImage)"
echo "=============================================="
echo "Release Tag: ${RELEASE_TAG}"
echo "Repository: ${REPO_OWNER}/${REPO_NAME}"
echo "Started: $(date)"
echo ""
echo "v4.0.2: This build creates:"
echo "  - rescale-int-gui.AppImage (double-clickable GUI)"
echo "  - rescale-int (standalone CLI binary)"
echo ""

# Build in /tmp to avoid polluting work directory
WORKDIR="$PWD"
BUILDDIR="/tmp/build"
LOGFILE="$WORKDIR/build.log"
mkdir -p "$BUILDDIR"

exec > >(tee -a "$LOGFILE") 2>&1
echo "Build log: $LOGFILE"

cd "$BUILDDIR"

# =============================================================================
# Step 1: Install Build Dependencies
# =============================================================================
echo ""
echo "[1/8] Installing build dependencies..."

# Rescale compute nodes are Rocky/Alma/CentOS-based
# Enable EPEL for additional packages
sudo yum install -y epel-release

# Install build dependencies + tools needed for linuxdeploy
sudo yum install -y \
    git jq wget curl \
    gcc gcc-c++ make \
    gtk3-devel \
    webkit2gtk3-devel \
    libappindicator-gtk3-devel \
    pango-devel cairo-devel \
    glib2-devel gdk-pixbuf2-devel \
    xdg-utils desktop-file-utils \
    xorg-x11-server-Xvfb \
    file patchelf fuse fuse-libs \
    ImageMagick

# Start Xvfb for headless GUI building (Wails requires a display for binding generation)
echo "Starting Xvfb virtual display..."
Xvfb :99 -screen 0 1024x768x24 &
XVFB_PID=$!
export DISPLAY=:99
echo "DISPLAY set to :99 (PID: $XVFB_PID)"

# Ensure Xvfb is killed when script exits (otherwise job won't terminate)
cleanup_xvfb() {
    if [ -n "${XVFB_PID:-}" ] && kill -0 "$XVFB_PID" 2>/dev/null; then
        echo "Stopping Xvfb (PID: $XVFB_PID)..."
        kill "$XVFB_PID" 2>/dev/null || true
    fi
}
trap cleanup_xvfb EXIT

# Install Node.js (from NodeSource for newer version)
echo "Installing Node.js..."
curl -fsSL https://rpm.nodesource.com/setup_20.x | sudo bash -
sudo yum install -y nodejs

echo "Node.js version:"
node --version
echo "npm version:"
npm --version

# =============================================================================
# Step 2: Install Go 1.24.2
# =============================================================================
echo ""
echo "[2/8] Installing Go 1.24.2..."

wget -q https://go.dev/dl/go1.24.2.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.24.2.linux-amd64.tar.gz
rm -f go1.24.2.linux-amd64.tar.gz

export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
export GOPATH="$HOME/go"

echo "Go version:"
go version

# =============================================================================
# Step 3: Install Wails CLI
# =============================================================================
echo ""
echo "[3/8] Installing Wails CLI..."

go install github.com/wailsapp/wails/v2/cmd/wails@latest

echo "Wails version:"
wails version

# =============================================================================
# Step 4: Download linuxdeploy + GTK Plugin + appimagetool
# =============================================================================
echo ""
echo "[4/9] Downloading linuxdeploy, GTK plugin, and appimagetool..."

cd "$BUILDDIR"

# Download linuxdeploy (use --extract-and-run to avoid FUSE requirement)
wget -q "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage"
chmod +x linuxdeploy-x86_64.AppImage

# Download GTK plugin
wget -q "https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/master/linuxdeploy-plugin-gtk.sh"
chmod +x linuxdeploy-plugin-gtk.sh

# v4.0.2: Download appimagetool to convert AppDir to AppImage
wget -q "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage"
chmod +x appimagetool-x86_64.AppImage

echo "linuxdeploy and appimagetool downloaded"
ls -la linuxdeploy-x86_64.AppImage linuxdeploy-plugin-gtk.sh appimagetool-x86_64.AppImage

# =============================================================================
# Step 5: Clone Repository
# =============================================================================
echo ""
echo "[5/9] Cloning repository..."

git clone --depth 1 --branch "release/${RELEASE_TAG}" \
    "https://x-access-token:${GITHUB_TOKEN}@github.com/${REPO_OWNER}/${REPO_NAME}.git"

echo "Repository cloned successfully"

# =============================================================================
# Step 6: Build Wails Application
# =============================================================================
echo ""
echo "[6/9] Building Wails application with FIPS 140-3..."

cd "${REPO_NAME}"

BUILD_TIME=$(date +%Y-%m-%d)
LDFLAGS="-s -w -X main.Version=${RELEASE_TAG} -X main.BuildTime=${BUILD_TIME}"

echo "Build flags: GOFIPS140=latest GOOS=linux GOARCH=amd64"
echo "LDFLAGS: ${LDFLAGS}"

# Install frontend dependencies
echo "Installing frontend dependencies..."
cd frontend
npm install
cd ..

# Build with Wails
GOFIPS140=latest wails build -platform linux/amd64 -ldflags "$LDFLAGS"

BINARY="build/bin/rescale-int"
if [ ! -f "$BINARY" ]; then
    echo "ERROR: Wails build failed - binary not created"
    exit 1
fi

echo "Binary built successfully"
chmod +x "$BINARY"
ls -la "$BINARY"
file "$BINARY"

# =============================================================================
# Step 7: Create AppDir with Bundled Dependencies
# =============================================================================
echo ""
echo "[7/9] Creating AppDir with bundled GTK/WebKitGTK dependencies..."

cd "$BUILDDIR"

APPDIR="$BUILDDIR/Interlink.AppDir"
REPO_DIR="$BUILDDIR/${REPO_NAME}"

# Ensure desktop file and icon exist
DESKTOP_FILE="$REPO_DIR/packaging/rescale-interlink.desktop"
ICON_FILE="$REPO_DIR/packaging/rescale-interlink.png"

if [ ! -f "$DESKTOP_FILE" ]; then
    echo "Creating desktop file..."
    mkdir -p "$REPO_DIR/packaging"
    cat > "$DESKTOP_FILE" << 'DESKTOP_EOF'
[Desktop Entry]
Name=Rescale Interlink
Comment=Unified CLI and GUI for Rescale HPC platform
Exec=rescale-int-gui
Icon=rescale-interlink
Type=Application
Categories=Development;Science;
Terminal=false
DESKTOP_EOF
fi

# Ensure icon exists and is correct size for linuxdeploy (max 512x512)
if [ -f "$ICON_FILE" ]; then
    # Check if ImageMagick is available
    if command -v convert &> /dev/null; then
        # Get current icon dimensions
        ICON_SIZE=$(identify -format "%wx%h" "$ICON_FILE" 2>/dev/null || echo "unknown")
        echo "Current icon size: $ICON_SIZE"

        # linuxdeploy only accepts up to 512x512, resize if larger
        # IMPORTANT: Keep the same filename! The .desktop file has Icon=rescale-interlink
        # and linuxdeploy matches by filename (without extension).
        if [[ "$ICON_SIZE" == "1024x1024" ]] || [[ "$ICON_SIZE" == *"1024"* ]]; then
            echo "Resizing icon from $ICON_SIZE to 512x512..."
            # Resize in-place (same filename) so linuxdeploy can match Icon entry
            RESIZED_ICON="$REPO_DIR/packaging/rescale-interlink.png"
            convert "$ICON_FILE" -resize 512x512 "$RESIZED_ICON"
            ICON_FILE="$RESIZED_ICON"
            echo "Icon resized successfully to $RESIZED_ICON"
        fi
    else
        echo "Warning: ImageMagick not available, attempting to use icon as-is"
    fi
else
    echo "Warning: Icon file not found, creating placeholder..."
    mkdir -p "$REPO_DIR/packaging"
    # Use ImageMagick if available, otherwise skip
    if command -v convert &> /dev/null; then
        convert -size 256x256 xc:navy -fill white -gravity center -pointsize 48 -annotate 0 'R' "$ICON_FILE"
    else
        echo "Warning: No icon and no ImageMagick - AppDir will have no icon"
        # Create empty placeholder so linuxdeploy doesn't fail
        touch "$ICON_FILE"
    fi
fi

# Run linuxdeploy with GTK plugin to bundle all dependencies
echo "Running linuxdeploy to bundle dependencies..."

# CRITICAL FIX: linuxdeploy uses getpwuid() to find the home directory, NOT $HOME.
# On Rescale VMs, there's an 'azureadmin' user whose home is /home/azureadmin.
# The job runs as a different user (uprod_xxx) that cannot access /home/azureadmin.
# linuxdeploy crashes with "Permission denied [/home/azureadmin/.local/bin]" when
# searching for plugins in that directory.
#
# Solution: Create the directory with open permissions so linuxdeploy can stat() it.
# It won't find any plugins (which is fine), but it won't crash.
echo "Creating linuxdeploy search directories to avoid permission issues..."
sudo mkdir -p /home/azureadmin/.local/bin 2>/dev/null || true
sudo chmod 755 /home/azureadmin 2>/dev/null || true
sudo chmod 755 /home/azureadmin/.local 2>/dev/null || true
sudo chmod 755 /home/azureadmin/.local/bin 2>/dev/null || true

# Also extract the AppImage to avoid FUSE mount issues
echo "Extracting linuxdeploy AppImage..."
./linuxdeploy-x86_64.AppImage --appimage-extract
LINUXDEPLOY_BIN="$BUILDDIR/squashfs-root/AppRun"

if [ ! -f "$LINUXDEPLOY_BIN" ]; then
    echo "ERROR: Failed to extract linuxdeploy AppImage"
    ls -la "$BUILDDIR/squashfs-root/" || true
    exit 1
fi

echo "Running extracted linuxdeploy..."
"$LINUXDEPLOY_BIN" \
    --appdir "$APPDIR" \
    --executable "$REPO_DIR/build/bin/rescale-int" \
    --desktop-file "$DESKTOP_FILE" \
    --icon-file "$ICON_FILE" \
    --plugin gtk

# Verify AppDir was created
if [ ! -d "$APPDIR" ] || [ ! -f "$APPDIR/AppRun" ]; then
    echo "ERROR: linuxdeploy failed to create AppDir"
    exit 1
fi

echo "AppDir created successfully"
echo "Contents:"
ls -la "$APPDIR"
echo ""
echo "Bundled libraries:"
ls -la "$APPDIR/usr/lib/" | head -20 || true

# =============================================================================
# Step 8: Convert to AppImage + Build Standalone CLI
# =============================================================================
echo ""
echo "[8/9] Converting AppDir to AppImage and building standalone CLI..."

cd "$BUILDDIR"

# v4.0.2: Extract appimagetool to avoid FUSE issues
echo "Extracting appimagetool..."
./appimagetool-x86_64.AppImage --appimage-extract
APPIMAGETOOL_BIN="$BUILDDIR/squashfs-root/AppRun"

if [ ! -f "$APPIMAGETOOL_BIN" ]; then
    echo "ERROR: Failed to extract appimagetool"
    # Fallback: try using extracted linuxdeploy's squashfs-root path
    APPIMAGETOOL_BIN="$BUILDDIR/squashfs-root-appimage/AppRun"
    if [ ! -f "$APPIMAGETOOL_BIN" ]; then
        echo "Trying alternate extraction..."
        rm -rf squashfs-root-appimage
        ./appimagetool-x86_64.AppImage --appimage-extract
        mv squashfs-root squashfs-root-appimage
        APPIMAGETOOL_BIN="$BUILDDIR/squashfs-root-appimage/AppRun"
    fi
fi

# Rename squashfs-root to avoid conflict
if [ -d "$BUILDDIR/squashfs-root" ] && [ ! -d "$BUILDDIR/squashfs-root-linuxdeploy" ]; then
    mv "$BUILDDIR/squashfs-root" "$BUILDDIR/squashfs-root-linuxdeploy"
fi

# Re-extract appimagetool
rm -rf "$BUILDDIR/squashfs-root"
./appimagetool-x86_64.AppImage --appimage-extract
APPIMAGETOOL_BIN="$BUILDDIR/squashfs-root/AppRun"

# Convert AppDir to AppImage
echo "Creating AppImage from AppDir..."
"$APPIMAGETOOL_BIN" "$APPDIR" rescale-int-gui.AppImage

if [ ! -f "rescale-int-gui.AppImage" ]; then
    echo "ERROR: appimagetool failed to create AppImage"
    exit 1
fi

chmod +x rescale-int-gui.AppImage
echo "AppImage created: rescale-int-gui.AppImage"
ls -lh rescale-int-gui.AppImage
file rescale-int-gui.AppImage

# v4.0.2: Build standalone CLI binary (no Wails, just Go CLI)
echo ""
echo "Building standalone CLI binary..."
cd "$BUILDDIR/${REPO_NAME}"

GOFIPS140=latest go build -ldflags "-s -w -X main.Version=${RELEASE_TAG} -X main.BuildTime=$(date +%Y-%m-%d)" \
    -o "$BUILDDIR/rescale-int" ./cmd/rescale-int

if [ ! -f "$BUILDDIR/rescale-int" ]; then
    echo "ERROR: CLI build failed"
    exit 1
fi

chmod +x "$BUILDDIR/rescale-int"
echo "CLI binary built: rescale-int"
ls -lh "$BUILDDIR/rescale-int"
file "$BUILDDIR/rescale-int"

# =============================================================================
# Step 9: Create Tarball
# =============================================================================
echo ""
echo "[9/9] Creating tarball..."

cd "$BUILDDIR"

# Create README
cat > README.txt << EOF
Rescale Interlink ${RELEASE_TAG} - Linux (amd64)

CONTENTS
========
rescale-int-gui.AppImage  - GUI application (double-click to run)
rescale-int               - CLI tool (run from terminal)

QUICK START
===========
GUI Mode:
  Double-click rescale-int-gui.AppImage, or:
  ./rescale-int-gui.AppImage

CLI Mode:
  ./rescale-int --help
  ./rescale-int jobs list
  ./rescale-int upload file.txt

The GUI (AppImage) is self-contained with all dependencies bundled.
No manual installation of system packages required.

For convenience, copy both files to a directory in your PATH:
  sudo cp rescale-int rescale-int-gui.AppImage /usr/local/bin/

DOCUMENTATION
=============
https://docs.rescale.com
https://github.com/rescale-labs/Rescale_Interlink

Copyright (c) 2026 Rescale, Inc.
EOF

# Create tarball with both binaries + README
TARBALL="rescale-interlink-${RELEASE_TAG}-linux-amd64.tar.gz"
tar -czvf "$TARBALL" \
    rescale-int-gui.AppImage \
    rescale-int \
    README.txt

echo "Packaged as: $TARBALL"
ls -lh "$TARBALL"

# Generate checksum
sha256sum "$TARBALL" > "${TARBALL}.sha256"
echo "Checksum: $(cat ${TARBALL}.sha256)"

# Copy to work directory for Rescale to save
cp "$TARBALL" "$WORKDIR/"
cp "${TARBALL}.sha256" "$WORKDIR/"
echo "Package copied to: $WORKDIR/$TARBALL"

# =============================================================================
# Done
# =============================================================================
echo ""
echo "=============================================="
echo "SUCCESS: Build complete!"
echo "=============================================="
echo "Release: ${RELEASE_TAG}"
echo "Package: $TARBALL"
echo "Location: $WORKDIR/$TARBALL"
echo ""
echo "v4.0.2 Package contents:"
echo "  rescale-int-gui.AppImage  - Double-click for GUI"
echo "  rescale-int               - CLI tool"
echo ""
echo "Download from job results and upload to GitHub."
echo "Completed: $(date)"
BUILD_LOGIC

chmod +x "$BUILD_SCRIPT"
echo "  Created: $BUILD_SCRIPT"
echo ""

# =============================================================================
# Submit Job
# =============================================================================
echo "Submitting Linux Wails build job to Rescale..."
echo ""

"$RESCALE_INT_BIN" jobs submit --script "$BUILD_SCRIPT" --files "$BUILD_SCRIPT" --end-to-end

echo ""
echo "=============================================="
echo "BUILD COMPLETE"
echo "=============================================="
echo ""
echo "Download the package from job results and upload to GitHub:"
echo "  https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/tag/${VERSION}"
echo ""
