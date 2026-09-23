#!/bin/bash
#
# build-release.sh - Build the Linux release tarball from the checkout this
# script lives in:
#   rescale-int-gui.AppImage  the Wails GUI, gated by verify-appimage.sh
#   rescale-int               the standalone CLI
#   README.txt                records the ref, the commit and the toolchain
# packed as /tmp/build/rescale-interlink-<id>-linux-amd64.tar.gz with a .sha256
# beside it. <id> is the tag for a tag build and the first 12 characters of the
# commit otherwise.
#
# Runs as root in a fresh AlmaLinux 8 container, as
# .github/workflows/release-linux.yml does: EL8's glibc 2.28 is the floor the
# binaries keep. Every third-party download is pinned by sha256.
#
# Usage: bash build/linux/build-release.sh

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# verify-appimage.sh fails an AppImage whose immodules template leaks
# /tmp/build, so building anywhere else would quietly disarm that check.
BUILDDIR="/tmp/build"
APPDIR="$BUILDDIR/Interlink.AppDir"

# Containers have no FUSE. With this set, linuxdeploy and appimagetool extract
# themselves to a temporary directory instead of mounting.
export APPIMAGE_EXTRACT_AND_RUN=1

# =============================================================================
# Pinned inputs
# =============================================================================
# A release tag's assets can be replaced upstream, so the sha256, not the URL,
# is the pin. Never point these at the mutable 'continuous' releases.
GO_VERSION="1.26.7"
GO_SHA256="ffb5f8de10c62550dfddab66b36b57030721e0a44a3218e9e1181d7b59f121ca"

NODE_VERSION="24.21.0"
NODE_SHA256="6e1db87ef58b8819e5d5402eff1536491b18edd8eb7bee5ef7897876e88dc5ff"

WAILS_VERSION="v2.12.0"

LINUXDEPLOY_URL="https://github.com/linuxdeploy/linuxdeploy/releases/download/1-alpha-20251107-1/linuxdeploy-x86_64.AppImage"
LINUXDEPLOY_SHA256="c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d"

# The gtk plugin has no releases, so it is pinned by commit.
GTK_PLUGIN_URL="https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/7a3fbc31a9e5075073ff8790f26effbac5f84453/linuxdeploy-plugin-gtk.sh"
GTK_PLUGIN_SHA256="b0f4cbc684a0103a9651f0955b635eaea0096b3a66c0f5a2c2aa337960375171"

APPIMAGETOOL_URL="https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage"
APPIMAGETOOL_SHA256="ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0"

# Given to appimagetool as --runtime-file. Without it, appimagetool downloads
# the runtime from the 'continuous' release at build time, unverified.
RUNTIME_URL="https://github.com/AppImage/type2-runtime/releases/download/20251108/runtime-x86_64"
RUNTIME_SHA256="2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d"

die() {
    echo "ERROR: $*" >&2
    exit 1
}

# fetch URL SHA256 FILE downloads URL to FILE and refuses anything but the
# pinned bytes.
fetch() {
    local url="$1" want="$2" file="$3" got
    curl -fsSL --retry 3 -o "$file" "$url"
    got="$(sha256sum "$file" | awk '{print $1}')"
    [ "$got" = "$want" ] || die "sha256 mismatch for $file
  url:      $url
  expected: $want
  actual:   $got
The download is corrupt or upstream replaced the file. Check the new file by
hand and re-pin it deliberately; never paste the new sum in to go green."
    echo "  sha256 OK: $file"
}

[ ! -e "$BUILDDIR" ] || die "$BUILDDIR already exists; run this in a fresh container"
mkdir -p "$BUILDDIR"
cd "$BUILDDIR"

# =============================================================================
# Step 1: packages
# =============================================================================
echo "[1/6] Installing build packages..."

# patchelf and ImageMagick come from EPEL, which relies on PowerTools. The last
# line is not needed to compile: the gtk plugin copies input-method and GTK
# modules, pixbuf loaders and GSettings schemas from this host into the
# AppImage, and these packages supply the ones the hand-built AppImage carried.
dnf -y install epel-release dnf-plugins-core
dnf config-manager --set-enabled powertools
dnf -y install \
    gcc gcc-c++ make binutils python3 file findutils tar gzip \
    gtk3-devel webkit2gtk3-devel glib2-devel gdk-pixbuf2-devel pango-devel cairo-devel \
    desktop-file-utils xorg-x11-server-Xvfb patchelf ImageMagick \
    ibus-gtk3 libcanberra-gtk3 gsettings-desktop-schemas gdk-pixbuf2-modules librsvg2

# Wails runs the application to generate its bindings, and the gate's runtime
# check launches the AppImage. Both use this display.
Xvfb :99 -screen 0 1024x768x24 >/dev/null 2>&1 &
XVFB_PID=$!
trap 'kill "$XVFB_PID" 2>/dev/null || true' EXIT
export DISPLAY=:99

# =============================================================================
# Step 2: toolchains and packaging tools
# =============================================================================
echo "[2/6] Installing Go, Node, Wails and the AppImage tools..."

fetch "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" "$GO_SHA256" go.tar.gz
tar -C /usr/local -xzf go.tar.gz
fetch "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.gz" "$NODE_SHA256" node.tar.gz
mkdir -p /usr/local/node
tar -C /usr/local/node --strip-components=1 -xzf node.tar.gz
export PATH="/usr/local/go/bin:/usr/local/node/bin:$PATH"
go install "github.com/wailsapp/wails/v2/cmd/wails@${WAILS_VERSION}"
PATH="$(go env GOPATH)/bin:$PATH"

fetch "$LINUXDEPLOY_URL" "$LINUXDEPLOY_SHA256" linuxdeploy-x86_64.AppImage
fetch "$GTK_PLUGIN_URL" "$GTK_PLUGIN_SHA256" linuxdeploy-plugin-gtk.sh
fetch "$APPIMAGETOOL_URL" "$APPIMAGETOOL_SHA256" appimagetool-x86_64.AppImage
fetch "$RUNTIME_URL" "$RUNTIME_SHA256" runtime-x86_64
chmod +x linuxdeploy-x86_64.AppImage linuxdeploy-plugin-gtk.sh appimagetool-x86_64.AppImage

go version
echo "node $(node --version), npm $(npm --version)"
wails version

# =============================================================================
# Step 3: what is being built
# =============================================================================
echo "[3/6] Identifying the source..."
cd "$REPO_DIR"

# actions/checkout leaves the workspace owned by another uid than this
# container's root, and git - which 'go build' also runs, to stamp VCS details -
# refuses such a repository unless it is declared safe.
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0="$REPO_DIR"

COMMIT="$(git rev-parse HEAD)"
SRC_VERSION="$(sed -n 's/^var Version = "\(.*\)"$/\1/p' internal/version/version.go)"
[ -n "$SRC_VERSION" ] || die "could not read the version from internal/version/version.go"

# A tag build is attached to release.yml's draft, so its tag must match
# version.go by the rule release.yml applies, and its binaries report the tag
# as release.yml's do. Any other build only validates: it is named for its
# commit and reports version.go's version.
if [ "${GITHUB_REF_TYPE:-}" = "tag" ]; then
    case "$GITHUB_REF_NAME" in
        "$SRC_VERSION" | "$SRC_VERSION"-?*) ARTIFACT_ID="$GITHUB_REF_NAME" BIN_VERSION="$GITHUB_REF_NAME" ;;
        *) die "tag $GITHUB_REF_NAME does not match internal/version/version.go ($SRC_VERSION)" ;;
    esac
else
    ARTIFACT_ID="${COMMIT:0:12}" BIN_VERSION="$SRC_VERSION"
fi
REF_NAME="${GITHUB_REF_NAME:-$COMMIT}"
BUILD_LOG="(not built by GitHub Actions)"
if [ -n "${GITHUB_RUN_ID:-}" ]; then
    BUILD_LOG="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID"
fi

echo "  ref:      $REF_NAME"
echo "  commit:   $COMMIT ($(git log -1 --pretty=%s))"
echo "  version:  $BIN_VERSION (stamped into the binaries)"
echo "  artifact: $ARTIFACT_ID"

# =============================================================================
# Step 4: CLI, GUI, AppDir and WebKit helpers
# =============================================================================
echo "[4/6] Building the CLI, the GUI and its AppDir..."

LDFLAGS="-s -w -X github.com/rescale/rescale-int/internal/version.Version=${BIN_VERSION} -X github.com/rescale/rescale-int/internal/version.BuildTime=$(date +%Y-%m-%d)"

# The CLI goes first: 'wails build' rewrites the tracked frontend/wailsjs
# bindings, and a CLI linked after that is stamped vcs.modified=true.
GOFIPS140=certified go build -tags fips -ldflags "$LDFLAGS" -o "$BUILDDIR/rescale-int" ./cmd/rescale-int

# wails.json's frontend:install is 'npm ci', so the frontend gets exactly what
# package-lock.json pins.
GOFIPS140=certified wails build -tags fips -platform linux/amd64 -ldflags "$LDFLAGS"

cd "$BUILDDIR"

# linuxdeploy takes icons up to 512x512 and matches the file name against the
# desktop file's Icon= entry, so the smaller copy keeps the name.
convert "$REPO_DIR/packaging/rescale-interlink.png" -resize '512x512>' rescale-interlink.png

# linuxdeploy finds linuxdeploy-plugin-gtk.sh here, beside its own AppImage.
./linuxdeploy-x86_64.AppImage \
    --appdir "$APPDIR" \
    --executable "$REPO_DIR/build/bin/rescale-int-gui" \
    --desktop-file "$REPO_DIR/packaging/rescale-interlink.desktop" \
    --icon-file "$BUILDDIR/rescale-interlink.png" \
    --plugin gtk

bash "$REPO_DIR/build/linux/bundle-webkit.sh" "$APPDIR"

# patchelf has rewritten these executables and every library they load, and
# the gate's ldd listing never looks a symbol up, so a damaged symbol table
# would pass the gate and crash at launch. 'ldd -r' performs every relocation.
for exe in "$APPDIR/usr/bin/rescale-int-gui" "$APPDIR"/usr/libexec/webkit2gtk-4.0/WebKit*Process; do
    out="$(env -u LD_LIBRARY_PATH ldd -r "$exe" 2>&1)" || die "ldd -r failed on $exe:
$out"
    if grep -E 'undefined symbol|not found' <<<"$out"; then
        die "unresolved symbols in $exe (listed above)"
    fi
done
echo "  every relocation resolves for the GUI and the WebKit helpers"

# =============================================================================
# Step 5: AppImage and the gate
# =============================================================================
echo "[5/6] Packaging the AppImage and running the gate..."

./appimagetool-x86_64.AppImage --runtime-file "$BUILDDIR/runtime-x86_64" \
    "$APPDIR" "$BUILDDIR/rescale-int-gui.AppImage"

# Before the tarball, so an AppImage that fails the gate is never packaged.
bash "$REPO_DIR/build/linux/verify-appimage.sh" "$BUILDDIR/rescale-int-gui.AppImage"

# =============================================================================
# Step 6: GLIBC floor and tarball
# =============================================================================
echo "[6/6] Checking the GLIBC floor and packaging..."

# Nothing shipped may need a newer glibc symbol than EL8's, or it would not
# start on the oldest distributions this build supports. One objdump per file:
# a file it cannot read must not cut the others short.
GLIBC_FLOOR="2.28"
GLIBC_MAX="$(find "$APPDIR" rescale-int -type f -exec objdump -T {} \; 2>/dev/null \
    | grep -o 'GLIBC_[0-9][0-9.]*' | sed 's/^GLIBC_//' | sort -uV | tail -1)" || true
[ -n "$GLIBC_MAX" ] || die "objdump found no GLIBC symbol versions to check"
[ "$(printf '%s\n' "$GLIBC_FLOOR" "$GLIBC_MAX" | sort -V | tail -1)" = "$GLIBC_FLOOR" ] \
    || die "the shipped binaries need GLIBC_$GLIBC_MAX, newer than the $GLIBC_FLOOR floor"
echo "  highest GLIBC version required: $GLIBC_MAX (floor $GLIBC_FLOOR)"

cat > README.txt <<EOF
Rescale Interlink ${ARTIFACT_ID} - Linux (amd64)

BUILD PROVENANCE
================
Built from ref:    ${REF_NAME}
Resolved commit:   ${COMMIT}
Binary version:    ${BIN_VERSION}
Built on:          $(date -u +%Y-%m-%dT%H:%M:%SZ)
Build log:         ${BUILD_LOG}
Go:                ${GO_VERSION}
Node:              ${NODE_VERSION}

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

The GUI (AppImage) is self-contained with all dependencies bundled, but
running it requires FUSE (fusermount), which most desktop distributions
provide by default. On a minimal or locked-down host without FUSE, run:
  ./rescale-int-gui.AppImage --appimage-extract-and-run

For convenience, copy both files to a directory in your PATH:
  sudo cp rescale-int rescale-int-gui.AppImage /usr/local/bin/

LINKS AND SUPPORT
=================
Documentation: https://docs.rescale.com
Source/issues: https://github.com/rescale-labs/Rescale_Interlink
Support:       support@rescale.com

Copyright (c) 2026 Rescale, Inc.
EOF

# tar records the modes the files have, so set them rather than trust the umask.
chmod 755 rescale-int-gui.AppImage rescale-int
chmod 644 README.txt

TARBALL="rescale-interlink-${ARTIFACT_ID}-linux-amd64.tar.gz"
tar -czvf "$TARBALL" rescale-int-gui.AppImage rescale-int README.txt
sha256sum "$TARBALL" > "$TARBALL.sha256"
cat "$TARBALL.sha256"
file rescale-int-gui.AppImage rescale-int
echo "Built $BUILDDIR/$TARBALL"
