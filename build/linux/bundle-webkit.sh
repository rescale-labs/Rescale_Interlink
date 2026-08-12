#!/bin/bash
#
# bundle-webkit.sh - Bundle WebKitGTK helper processes into a linuxdeploy AppDir.
#
# THE BUG THIS FIXES: libwebkit2gtk forks its helper executables
# (WebKitWebProcess, WebKitNetworkProcess) from a directory compiled into the
# library (PKGLIBEXECDIR, normally /usr/libexec/webkit2gtk-4.0), and linuxdeploy
# bundles the library but never those helpers - so a shipped AppImage forks the
# HOST's helpers, and when the host WebKit is a different build the IPC
# handshake fails ("received invalid message:
# WebPage_GetWebArchiveOfFrameWithFileNameReply"), leaving a window frame that
# paints but never renders content.
#
# WHAT THIS DOES, on the build node, after linuxdeploy and its gtk plugin have
# populated the AppDir:
#
#   1. Copies the build host's WebKit helpers - and the injected-bundle
#      directory the library loads into each web process - into
#      $APPDIR/usr/libexec/, preserving modes.
#
#   2. Sets an $ORIGIN-relative RPATH on every copied executable and library.
#      This is not optional: linuxdeploy only rewrites the RPATH of ELFs it
#      deployed itself, and neither its AppRun wrapper nor its gtk plugin ever
#      sets LD_LIBRARY_PATH. A copied helper with no RPATH would resolve
#      libwebkit2gtk, libjavascriptcoregtk, libgtk-3 and libsoup through
#      ld.so.cache - that is, from the HOST - which recreates exactly the
#      version-mismatch crash this script exists to prevent, while looking
#      fine on a build machine that happens to have a matching WebKit.
#
#   3. Makes the BUNDLED library resolve the helpers from inside the bundle.
#      Which mechanism works depends on how the distro compiled WebKit, so this
#      is decided empirically here and echoed:
#        (a) WEBKIT_EXEC_PATH environment override, if the bundled library
#            actually reads it. Upstream gates that read behind
#            ENABLE(DEVELOPER_MODE), so most distro builds do NOT have it.
#        (b) Otherwise an in-place, byte-for-byte same-length patch of the
#            compiled-in path, pointing it at a fixed short path under /tmp
#            that the AppRun hook symlinks to this bundle at launch.
#        (c) Otherwise: fail the build loudly rather than ship a GUI that
#            cannot render.
#
#   4. Regenerates the GTK immodules cache. The gtk plugin strips module paths
#      down to bare build-host filenames ("im-ibus.so"), which do not resolve
#      at runtime and produce ibus warnings; this writes a template whose paths
#      are filled in with the real mount point at launch.
#
# Everything it decided is recorded in
# $APPDIR/usr/share/rescale-int/webkit-bundle.manifest, and
# build/linux/verify-appimage.sh re-checks those claims against the finished
# AppImage. That verify script - not this one - is the release gate.
#
# Hooks appended here are sourced by linuxdeploy's AppRun wrapper, which runs
# with 'set -e'. A single failing statement in a hook therefore aborts the
# application launch, so every appended statement is written to always succeed.
#
# Usage: bash build/linux/bundle-webkit.sh <APPDIR>
#
# RS_WEBKIT_LIBEXEC_DIR overrides where the host's helpers are looked for. It
# covers builders that install WebKit somewhere unusual, and it is what makes
# this script exercisable outside a real build node.

set -euo pipefail

# =============================================================================
# Constants
# =============================================================================

# Helpers that must end up in the bundle. WebKitWebProcess renders pages and
# WebKitNetworkProcess performs loads; both are mandatory. The rest are
# copied when present but never required.
REQUIRED_HELPERS="WebKitWebProcess WebKitNetworkProcess"
OPTIONAL_HELPERS="WebKitPluginProcess WebKitWebDriver WebKitGPUProcess"

# The path compiled into the library, and the equal-length path rung (b)
# rewrites it to. Byte lengths are asserted below - they MUST match, because a
# longer replacement would overwrite neighbouring data in .rodata.
EMBEDDED_LIBEXEC="/usr/libexec/webkit2gtk-4.0"
PATCH_LINK_DIR="/tmp/.rswkit"
PATCH_LIBEXEC="${PATCH_LINK_DIR}/webkit2gtk-4.0"

# Where the helpers live inside the AppDir (relative to $APPDIR).
LIBEXEC_REL="usr/libexec/webkit2gtk-4.0"
INJECTED_REL="usr/libexec/injected-bundle"

MANIFEST_REL="usr/share/rescale-int/webkit-bundle.manifest"
HOOK_REL="apprun-hooks/linuxdeploy-plugin-gtk.sh"

# Written as the first line of whatever we append to the hook. Its presence is
# how a re-run recognises an AppDir it has already processed.
HOOK_MARKER="# rescale-int-webkit-bundle: v1"

# RPATH stamped onto every copied ELF. Helpers live at
# usr/libexec/webkit2gtk-4.0/<helper> and the injected bundle at
# usr/libexec/injected-bundle/<lib>, so both are exactly two levels below usr/
# and both reach the bundled libraries through ../../lib.
RPATH_VALUE='$ORIGIN/../../lib'

# Sonames that MUST come from inside the bundle. Everything else (glibc,
# ld-linux, libGL, libX11/xcb, libdrm and friends) is on linuxdeploy's
# deliberate excludelist and is expected to come from the host.
CRITICAL_SONAMES="libwebkit2gtk-4.0 libjavascriptcoregtk libsoup libgtk-3"

# Placeholder written into the shipped immodules cache template and replaced
# with the real AppDir at launch.
APPDIR_TOKEN="@RS_APPDIR@"

# =============================================================================
# Helpers
# =============================================================================

die() {
    echo ""
    echo "=============================================="
    echo "ERROR: bundle-webkit.sh: $*"
    echo "=============================================="
    exit 1
}

step() {
    echo ""
    echo "--- $* ---"
}

# =============================================================================
# Arguments and environment
# =============================================================================

APPDIR="${1:-}"
[ -n "$APPDIR" ] || die "usage: bundle-webkit.sh <APPDIR>"
[ -d "$APPDIR" ] || die "AppDir does not exist: $APPDIR"
APPDIR="$(cd "$APPDIR" && pwd)"

HOOKFILE="$APPDIR/$HOOK_REL"
[ -f "$HOOKFILE" ] || die "gtk plugin AppRun hook not found at $HOOKFILE.
Without it linuxdeploy does not deploy the AppRun wrapper, so nothing we export
would ever run. Check that linuxdeploy was invoked with '--plugin gtk'."

command -v strings >/dev/null 2>&1 || die "'strings' not found (install binutils) - needed to inspect the bundled library"

echo "=============================================="
echo "Bundling WebKitGTK helpers into AppDir"
echo "=============================================="
echo "AppDir:   $APPDIR"
echo "Hook:     $HOOKFILE"
echo "Host:     $(uname -srm)"
echo "Started:  $(date)"

# Hook additions are accumulated here and appended in one shot at the end, so a
# mid-script failure cannot leave a half-written hook behind.
HOOK_ADD="$(mktemp "${TMPDIR:-/tmp}/rs-webkit-hook.XXXXXX")"
SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/rs-webkit-scratch.XXXXXX")"
cleanup() {
    rm -f "$HOOK_ADD" 2>/dev/null || true
    rm -rf "$SCRATCH" 2>/dev/null || true
}
trap cleanup EXIT

# =============================================================================
# Step 1: Locate the bundled libwebkit2gtk
# =============================================================================
step "Step 1: locating bundled libwebkit2gtk"

BUNDLED_LIBS=""
for candidate in "$APPDIR/usr/lib"/libwebkit2gtk-4.0.so*; do
    # Skip the unexpanded glob and the symlinks pointing at the real file.
    if [ -f "$candidate" ] && [ ! -L "$candidate" ]; then
        BUNDLED_LIBS="$BUNDLED_LIBS $candidate"
        echo "  found: $candidate ($(stat -c %s "$candidate" 2>/dev/null || echo '?') bytes)"
    fi
done
BUNDLED_LIBS="${BUNDLED_LIBS# }"

[ -n "$BUNDLED_LIBS" ] || die "no libwebkit2gtk-4.0.so* regular file in $APPDIR/usr/lib.
linuxdeploy did not bundle WebKit, so the GUI would use whatever the host has.
Contents of $APPDIR/usr/lib:
$(ls -1 "$APPDIR/usr/lib" 2>/dev/null | head -40)"

# The rung decision is made from the first (normally only) real library.
PROBE_LIB="${BUNDLED_LIBS%% *}"
echo "  probing: $PROBE_LIB"

# ---------------------------------------------------------------------------
# Already done? Then stop here.
# ---------------------------------------------------------------------------
# A second run over the same AppDir must not append a duplicate hook block, and
# must not misread its own earlier work: once the embedded path has been
# patched, the original literal is gone, which would otherwise send the ladder
# to rung (c) and fail the build with a misleading "no usable mechanism".
ALREADY=""
if grep -q -F -- "$HOOK_MARKER" "$HOOKFILE" 2>/dev/null; then
    ALREADY="hook already carries $HOOK_MARKER"
elif grep -q -a -F -- "$PATCH_LIBEXEC" "$PROBE_LIB" 2>/dev/null; then
    ALREADY="library already patched to $PATCH_LIBEXEC"
fi

if [ -n "$ALREADY" ]; then
    echo ""
    echo "=============================================="
    echo "Already bundled - nothing to do"
    echo "=============================================="
    echo "reason: $ALREADY"
    if [ -f "$APPDIR/$MANIFEST_REL" ]; then
        echo "existing manifest:"
        sed 's/^/    /' "$APPDIR/$MANIFEST_REL"
    else
        echo "note: no manifest found, so a previous run did not finish."
        echo "note: delete the AppDir and rebuild if this was not expected."
    fi
    exit 0
fi

# =============================================================================
# Step 2: Copy the host's helper executables
# =============================================================================
step "Step 2: copying WebKit helper executables"

HOST_LIBEXEC=""
for candidate in \
    ${RS_WEBKIT_LIBEXEC_DIR:-} \
    "$EMBEDDED_LIBEXEC" \
    /usr/lib64/webkit2gtk-4.0 \
    /usr/lib/webkit2gtk-4.0 \
    /usr/lib/x86_64-linux-gnu/webkit2gtk-4.0
do
    if [ -x "$candidate/WebKitWebProcess" ]; then
        HOST_LIBEXEC="$candidate"
        break
    fi
done

[ -n "$HOST_LIBEXEC" ] || die "could not find WebKitWebProcess on the build host.
Searched: $EMBEDDED_LIBEXEC /usr/lib64/webkit2gtk-4.0 /usr/lib/webkit2gtk-4.0 /usr/lib/x86_64-linux-gnu/webkit2gtk-4.0
Install webkit2gtk3 (RHEL/Rocky) or libwebkit2gtk-4.0-37 (Debian) on the builder."

echo "  host helper directory: $HOST_LIBEXEC"
ls -la "$HOST_LIBEXEC"

mkdir -p "$APPDIR/$LIBEXEC_REL"

# cp -a preserves the executable bits and any symlinks inside the directory.
COPIED_HELPERS=""
for helper in $REQUIRED_HELPERS; do
    [ -f "$HOST_LIBEXEC/$helper" ] || die "required helper missing on build host: $HOST_LIBEXEC/$helper"
    cp -a "$HOST_LIBEXEC/$helper" "$APPDIR/$LIBEXEC_REL/"
    COPIED_HELPERS="$COPIED_HELPERS $helper"
    echo "  copied (required): $helper"
done
for helper in $OPTIONAL_HELPERS; do
    if [ -f "$HOST_LIBEXEC/$helper" ]; then
        cp -a "$HOST_LIBEXEC/$helper" "$APPDIR/$LIBEXEC_REL/"
        COPIED_HELPERS="$COPIED_HELPERS $helper"
        echo "  copied (optional): $helper"
    fi
done

# Anything else shipped alongside the helpers (jsc shells, resource blobs).
for extra in "$HOST_LIBEXEC"/*; do
    if [ -f "$extra" ]; then
        name="$(basename "$extra")"
        if [ ! -e "$APPDIR/$LIBEXEC_REL/$name" ]; then
            cp -a "$extra" "$APPDIR/$LIBEXEC_REL/"
            echo "  copied (extra): $name"
        fi
    fi
done

for helper in $REQUIRED_HELPERS; do
    [ -x "$APPDIR/$LIBEXEC_REL/$helper" ] || die "helper is not executable after copy: $APPDIR/$LIBEXEC_REL/$helper"
done

echo "  bundled helper directory:"
ls -la "$APPDIR/$LIBEXEC_REL"

# =============================================================================
# Step 3: Copy the injected bundle
# =============================================================================
# Each web process dlopens libwebkit2gtkinjectedbundle.so from a directory the
# UI process hands it. That directory is also compiled in, and it is NOT under
# libexec, so bundling the helpers alone still leaves it pointing at the host.
step "Step 3: copying WebKit injected bundle"

HOST_INJECTED=""
for candidate in \
    /usr/lib64/webkit2gtk-4.0/injected-bundle \
    /usr/lib/webkit2gtk-4.0/injected-bundle \
    /usr/lib/x86_64-linux-gnu/webkit2gtk-4.0/injected-bundle \
    "$HOST_LIBEXEC/injected-bundle"
do
    if [ -d "$candidate" ]; then
        HOST_INJECTED="$candidate"
        break
    fi
done

INJECTED_BUNDLED="no"
if [ -n "$HOST_INJECTED" ]; then
    mkdir -p "$APPDIR/$INJECTED_REL"
    cp -a "$HOST_INJECTED"/. "$APPDIR/$INJECTED_REL/"
    INJECTED_BUNDLED="yes"
    echo "  host injected bundle: $HOST_INJECTED"
    ls -la "$APPDIR/$INJECTED_REL"
else
    echo "  WARNING: no injected-bundle directory found on the build host."
    echo "  WARNING: web processes will look for it wherever the library was"
    echo "  WARNING: compiled to look, which may not exist on a user's machine."
fi

# =============================================================================
# Step 4: Stamp an $ORIGIN-relative RPATH on everything copied
# =============================================================================
# THE POINT OF THIS STEP: nothing else makes the copied binaries use the
# bundled libraries. linuxdeploy rewrites RPATHs only for ELFs it deployed
# itself, its AppRun wrapper only sources apprun-hooks and execs the real
# binary, and the gtk plugin exports GTK_*/GI_*/XDG_* but never
# LD_LIBRARY_PATH. So without an RPATH here, a helper started by WebKit
# resolves libwebkit2gtk and friends from ld.so.cache - the host's copies -
# and we are back to the mismatch that renders a blank window. It looks
# healthy on a build node precisely because the build node has a matching
# WebKit installed.
step "Step 4: setting \$ORIGIN-relative RPATH on copied binaries"

command -v patchelf >/dev/null 2>&1 || die "'patchelf' not found.
It is required to point the copied WebKit binaries at the bundled libraries.
Install patchelf on the build node (the release wrapper yum-installs it)."

echo "  RPATH to set: $RPATH_VALUE"

# Returns 0 if patchelf handled the file, 1 if it is not a patchable ELF.
set_rpath() {
    local target="$1" required="$2" actual

    # --print-rpath fails on anything that is not a dynamic ELF, which is how
    # resource blobs and shell wrappers are skipped.
    if ! patchelf --print-rpath "$target" >/dev/null 2>&1; then
        if [ "$required" = "required" ]; then
            die "patchelf cannot read $target as an ELF, but it is a required helper"
        fi
        echo "    skipped (not a dynamic ELF): $(basename "$target")"
        return 1
    fi

    if ! patchelf --set-rpath "$RPATH_VALUE" "$target" 2>"$SCRATCH/patchelf.err"; then
        sed 's/^/      /' "$SCRATCH/patchelf.err" 2>/dev/null || true
        if [ "$required" = "required" ]; then
            die "patchelf --set-rpath failed on required helper $target"
        fi
        echo "    WARNING: patchelf failed on $(basename "$target"), left unchanged"
        return 1
    fi

    actual="$(patchelf --print-rpath "$target" 2>/dev/null || printf '')"
    if [ "$actual" != "$RPATH_VALUE" ]; then
        die "RPATH did not stick on $target
  wanted: $RPATH_VALUE
  got:    ${actual:-<empty>}"
    fi
    echo "    RPATH set: $(basename "$target") -> $actual"
    return 0
}

RPATH_COUNT=0
for helper in $REQUIRED_HELPERS; do
    set_rpath "$APPDIR/$LIBEXEC_REL/$helper" "required"
    RPATH_COUNT=$((RPATH_COUNT + 1))
done

# Everything else in the helper directory, best effort.
for extra in "$APPDIR/$LIBEXEC_REL"/*; do
    if [ -f "$extra" ]; then
        case " $REQUIRED_HELPERS " in
            *" $(basename "$extra") "*) continue ;;
        esac
        if set_rpath "$extra" "optional"; then
            RPATH_COUNT=$((RPATH_COUNT + 1))
        fi
    fi
done

# The injected bundle is dlopened into each web process and links the same
# libraries, so it needs the same treatment.
if [ "$INJECTED_BUNDLED" = "yes" ]; then
    for lib in "$APPDIR/$INJECTED_REL"/*.so*; do
        if [ -f "$lib" ]; then
            if set_rpath "$lib" "optional"; then
                RPATH_COUNT=$((RPATH_COUNT + 1))
            fi
        fi
    done
fi

echo "  RPATH stamped on $RPATH_COUNT file(s)"

# ---------------------------------------------------------------------------
# Prove it, the same way the loader will: no LD_LIBRARY_PATH.
# ---------------------------------------------------------------------------
# build/linux/verify-appimage.sh re-checks this against the packaged AppImage.
# Checking here too means a failure is reported where the context is, instead
# of after appimagetool has run.
if command -v ldd >/dev/null 2>&1; then
    for helper in $REQUIRED_HELPERS; do
        helper_path="$APPDIR/$LIBEXEC_REL/$helper"
        echo ""
        echo "  ldd $helper (LD_LIBRARY_PATH deliberately unset):"
        env -u LD_LIBRARY_PATH ldd "$helper_path" > "$SCRATCH/ldd.helper" 2>&1 || true
        sed 's/^/      /' "$SCRATCH/ldd.helper"

        if grep -q 'not found' "$SCRATCH/ldd.helper"; then
            die "$helper has unresolved libraries without LD_LIBRARY_PATH:
$(grep 'not found' "$SCRATCH/ldd.helper")
The RPATH is not doing its job. Nothing sets LD_LIBRARY_PATH at runtime, so
this would fail on a user's machine."
        fi

        for soname in $CRITICAL_SONAMES; do
            resolved="$(grep -F -- "$soname" "$SCRATCH/ldd.helper" | grep -F '=>' | head -1 || true)"
            if [ -z "$resolved" ]; then
                continue
            fi
            case "$resolved" in
                *"=> $APPDIR/"*) : ;;
                *)
                    die "$helper resolves $soname OUTSIDE the bundle:
  $resolved
That is the host's copy. A mismatched host WebKit is exactly the failure this
script exists to prevent."
                    ;;
            esac
        done
        echo "      critical sonames resolve inside the AppDir (ok)"
    done
else
    echo "  WARNING: 'ldd' not available, cannot confirm resolution here."
    echo "  WARNING: build/linux/verify-appimage.sh still gates this."
fi

# =============================================================================
# Step 5: Probe the bundled library
# =============================================================================
step "Step 5: probing the bundled library for path-resolution support"

strings -a "$PROBE_LIB" > "$SCRATCH/lib.strings"

# 'grep -c' prints 0 AND exits 1 when nothing matches, so the exit status is
# swallowed rather than substituted - '|| echo 0' would yield two lines and
# break every integer test below.
count_fixed() {
    local n
    n="$(grep -c -F -- "$1" "$SCRATCH/lib.strings" 2>/dev/null || true)"
    [ -n "$n" ] || n=0
    printf '%s\n' "$n"
}

count_fixed_exact() {
    local n
    n="$(grep -c -F -x -- "$1" "$SCRATCH/lib.strings" 2>/dev/null || true)"
    [ -n "$n" ] || n=0
    printf '%s\n' "$n"
}

N_EXEC_ENV="$(count_fixed 'WEBKIT_EXEC_PATH')"
N_INJECTED_ENV="$(count_fixed 'WEBKIT_INJECTED_BUNDLE_PATH')"
N_EMBEDDED="$(count_fixed_exact "$EMBEDDED_LIBEXEC")"
N_EMBEDDED_ANY="$(count_fixed "$EMBEDDED_LIBEXEC")"

echo "  WEBKIT_EXEC_PATH literal ............ $N_EXEC_ENV occurrence(s)"
echo "  WEBKIT_INJECTED_BUNDLE_PATH literal . $N_INJECTED_ENV occurrence(s)"
echo "  '$EMBEDDED_LIBEXEC' exact ........... $N_EMBEDDED occurrence(s)"
echo "  '$EMBEDDED_LIBEXEC' as substring .... $N_EMBEDDED_ANY occurrence(s)"
echo "  strings containing 'webkit2gtk-4.0':"
grep -F -- 'webkit2gtk-4.0' "$SCRATCH/lib.strings" 2>/dev/null | sort -u | head -20 || true

# =============================================================================
# Step 6: Apply the path-resolution ladder
# =============================================================================
step "Step 6: choosing a helper-path strategy"

# Byte-length equality is the whole safety argument for rung (b). Assert it
# before anything else so a bad edit to the constants above cannot slip through.
LEN_OLD="${#EMBEDDED_LIBEXEC}"
LEN_NEW="${#PATCH_LIBEXEC}"
if [ "$LEN_OLD" -ne "$LEN_NEW" ]; then
    die "internal error: replacement path length mismatch.
  '$EMBEDDED_LIBEXEC' is $LEN_OLD bytes
  '$PATCH_LIBEXEC' is $LEN_NEW bytes
These must be equal. Adjust PATCH_LINK_DIR so the full path matches byte for byte."
fi
echo "  replacement path length check: $LEN_OLD == $LEN_NEW bytes (ok)"

# Binary-safe, in-place literal replacement. Prefers python3, falls back to
# perl. Prints the number of occurrences replaced on stdout.
replace_literal() {
    local file="$1" old="$2" new="$3"
    if command -v python3 >/dev/null 2>&1; then
        python3 - "$file" "$old" "$new" <<'PY'
import sys
path, old, new = sys.argv[1], sys.argv[2].encode(), sys.argv[3].encode()
if len(new) > len(old):
    sys.stderr.write("replacement longer than original\n")
    sys.exit(1)
with open(path, 'rb') as fh:
    data = fh.read()
count = data.count(old)
if count:
    # Pad with NULs so the replacement keeps the original byte length; the
    # first NUL terminates the C string, the rest is unreachable filler.
    data = data.replace(old, new + b'\0' * (len(old) - len(new)))
    with open(path, 'wb') as fh:
        fh.write(data)
print(count)
PY
    elif command -v perl >/dev/null 2>&1; then
        RS_OLD="$old" RS_NEW="$new" perl -e '
            my ($path) = @ARGV;
            my $old = $ENV{RS_OLD}; my $new = $ENV{RS_NEW};
            die "replacement longer than original\n" if length($new) > length($old);
            $new .= "\0" x (length($old) - length($new));
            open(my $in, "<", $path) or die "open $path: $!\n";
            binmode $in; local $/; my $data = <$in>; close $in;
            my $count = ($data =~ s/\Q$old\E/$new/g);
            if ($count) {
                open(my $out, ">", $path) or die "write $path: $!\n";
                binmode $out; print $out $data; close $out;
            }
            print "$count\n";
        ' "$file"
    else
        echo "NOINTERP"
    fi
}

PATH_STRATEGY=""
PATCHED_COUNT=0

if [ "$N_EXEC_ENV" -gt 0 ]; then
    # ---------------------------------------------------------------- rung (a)
    PATH_STRATEGY="env"
    echo "  RUNG (a): the bundled library reads WEBKIT_EXEC_PATH."
    echo "  Exporting it from the AppRun hook - no binary patching needed."

    cat >> "$HOOK_ADD" <<EOF

${HOOK_MARKER}
# --- rescale-int: bundled WebKit helper processes (WEBKIT_EXEC_PATH) -------
# Sourced by linuxdeploy's AppRun wrapper, which runs with 'set -e', so every
# statement here must succeed. \$APPDIR is exported earlier in this same hook.
export WEBKIT_EXEC_PATH="\$APPDIR/${LIBEXEC_REL}"
EOF
else
    # ---------------------------------------------------------------- rung (b)
    echo "  RUNG (a) unavailable: no WEBKIT_EXEC_PATH support in the bundled library."
    echo "  RUNG (b): patching the compiled-in helper path in place."

    if [ "$N_EMBEDDED_ANY" -eq 0 ]; then
        # ------------------------------------------------------------ rung (c)
        #
        # TODO(human): neither path-resolution rung is available on this
        # WebKit build. The documented fallback is an LD_PRELOAD shim that
        # intercepts the helper spawn - interpose execv/execvp/posix_spawn (or
        # g_subprocess_launcher_spawnv), rewrite an argv[0] of
        # /usr/libexec/webkit2gtk-4.0/WebKit*Process to the bundled copy under
        # $APPDIR/usr/libexec/webkit2gtk-4.0, ship the shim as
        # $APPDIR/usr/lib/librswk-shim.so, and export LD_PRELOAD from the
        # AppRun hook. That is deliberately NOT auto-generated here: shipping
        # compiled C emitted by a build script, into every customer's process,
        # needs a human to write and review it.
        #
        die "no usable WebKit helper-path mechanism on this build.
  * WEBKIT_EXEC_PATH is not compiled into $PROBE_LIB
  * the literal '$EMBEDDED_LIBEXEC' does not appear in $PROBE_LIB either,
    so there is no embedded path to patch

The library probably composes the helper path at runtime, or this is a
differently packaged WebKit. Diagnostics:

  library:            $PROBE_LIB
  size:               $(stat -c %s "$PROBE_LIB" 2>/dev/null || echo '?') bytes
  webkit2gtk strings: $(count_fixed 'webkit2gtk')
  libexec strings:    $(count_fixed '/libexec/')

  candidate paths seen in the library:
$(grep -E -- '^/usr/(lib|lib64|libexec)/' "$SCRATCH/lib.strings" 2>/dev/null | sort -u | head -20 || true)

Next step is the LD_PRELOAD shim described in the TODO in this script."
    fi

    PATH_STRATEGY="patch"
    for lib in $BUNDLED_LIBS; do
        result="$(replace_literal "$lib" "$EMBEDDED_LIBEXEC" "$PATCH_LIBEXEC")"
        if [ "$result" = "NOINTERP" ]; then
            die "neither python3 nor perl is available to patch $lib.
Install python3 (or perl) on the build node - rung (b) needs a binary-safe
string replacement, and sed cannot do that without corrupting the ELF."
        fi
        echo "  patched $lib: $result occurrence(s) of '$EMBEDDED_LIBEXEC' -> '$PATCH_LIBEXEC'"
        PATCHED_COUNT=$((PATCHED_COUNT + result))
    done

    if [ "$PATCHED_COUNT" -eq 0 ]; then
        die "no occurrences replaced despite $N_EMBEDDED_ANY string match(es) - refusing to continue"
    fi

    # A same-length .rodata edit cannot move anything, but ldd is the cheap
    # proof that the ELF is still loadable and its deps still resolve.
    echo "  re-checking the patched library with ldd:"
    if LD_LIBRARY_PATH="$APPDIR/usr/lib" ldd "$PROBE_LIB" > "$SCRATCH/ldd.out" 2>&1; then
        if grep -q 'not found' "$SCRATCH/ldd.out"; then
            echo "  unresolved dependencies after patching:"
            grep 'not found' "$SCRATCH/ldd.out" || true
            die "patched library has unresolved dependencies (see above)"
        fi
        echo "  ldd: all dependencies resolve inside the AppDir (ok)"
    else
        cat "$SCRATCH/ldd.out" || true
        die "ldd failed on the patched library - the patch may have corrupted it"
    fi

    cat >> "$HOOK_ADD" <<EOF

${HOOK_MARKER}
# --- rescale-int: bundled WebKit helper processes (embedded-path patch) ----
# The bundled libwebkit2gtk has its compiled-in helper directory rewritten to
# ${PATCH_LIBEXEC}, so point that fixed path at this bundle's libexec.
#
# ${PATCH_LINK_DIR} is a fixed name in a world-writable directory, and WebKit
# will exec whatever it resolves to. So this does not merely try to create the
# link - it refuses to start unless the link is ours and points where we
# expect. A stale link from an old mount, or one planted by another user, would
# otherwise silently redirect helper execution. Sourced by linuxdeploy's AppRun
# wrapper under 'set -e', so 'exit 1' here stops the launch loudly.
_rs_wk_link="${PATCH_LINK_DIR}"
_rs_wk_want="\$APPDIR/usr/libexec"
if [ "\$(readlink "\$_rs_wk_link" 2>/dev/null || printf '')" != "\$_rs_wk_want" ]; then
    ln -sfn "\$_rs_wk_want" "\$_rs_wk_link" 2>/dev/null || true
fi
# stat without -L reports the symlink itself, not its target.
_rs_wk_have="\$(readlink "\$_rs_wk_link" 2>/dev/null || printf '')"
_rs_wk_owner="\$(stat -c %u "\$_rs_wk_link" 2>/dev/null || printf '')"
_rs_wk_me="\$(id -u 2>/dev/null || printf 'unknown')"
if [ "\$_rs_wk_have" != "\$_rs_wk_want" ] || [ "\$_rs_wk_owner" != "\$_rs_wk_me" ]; then
    echo "rescale-int: FATAL: cannot claim \$_rs_wk_link for this session." >&2
    echo "rescale-int:   expected link -> \$_rs_wk_want" >&2
    echo "rescale-int:   found link    -> \${_rs_wk_have:-<none>}" >&2
    echo "rescale-int:   link owner uid \${_rs_wk_owner:-<none>}, this process is uid \$_rs_wk_me" >&2
    echo "rescale-int: WebKit launches its helper processes through that path, so" >&2
    echo "rescale-int: starting now could execute a binary we do not control." >&2
    echo "rescale-int: Remove or correct \$_rs_wk_link, then relaunch." >&2
    exit 1
fi
EOF
fi

# ---------------------------------------------------------------------------
# Injected-bundle wiring, using whichever mechanism this build supports.
# ---------------------------------------------------------------------------
INJECTED_STRATEGY="none"
if [ "$INJECTED_BUNDLED" = "yes" ]; then
    if [ "$N_INJECTED_ENV" -gt 0 ]; then
        INJECTED_STRATEGY="env"
        echo "  injected bundle: library reads WEBKIT_INJECTED_BUNDLE_PATH, exporting it."
        cat >> "$HOOK_ADD" <<EOF
export WEBKIT_INJECTED_BUNDLE_PATH="\$APPDIR/${INJECTED_REL}"
EOF
    elif [ "$PATH_STRATEGY" = "patch" ]; then
        # Same trick as the helpers: rewrite the compiled-in directory to sit
        # under the /tmp symlink. Shorter than the original, so it is
        # NUL-padded back to the original length.
        for embedded_injected in \
            /usr/lib64/webkit2gtk-4.0/injected-bundle \
            /usr/lib/webkit2gtk-4.0/injected-bundle \
            /usr/lib/x86_64-linux-gnu/webkit2gtk-4.0/injected-bundle
        do
            if grep -q -F -- "$embedded_injected" "$SCRATCH/lib.strings" 2>/dev/null; then
                for lib in $BUNDLED_LIBS; do
                    result="$(replace_literal "$lib" "$embedded_injected" "${PATCH_LINK_DIR}/injected-bundle")"
                    echo "  injected bundle: patched $lib, $result occurrence(s) of '$embedded_injected'"
                done
                INJECTED_STRATEGY="patch"
                break
            fi
        done
        if [ "$INJECTED_STRATEGY" = "none" ]; then
            echo "  WARNING: injected bundle is bundled but its compiled-in path was not"
            echo "  WARNING: found in the library, so it stays pointed at the host."
        fi
    else
        echo "  WARNING: injected bundle is bundled but this library supports neither"
        echo "  WARNING: WEBKIT_INJECTED_BUNDLE_PATH nor a patchable path; it stays"
        echo "  WARNING: pointed at the host's copy."
    fi
fi

# =============================================================================
# Step 7: Regenerate the GTK immodules cache
# =============================================================================
step "Step 7: regenerating the GTK immodules cache"

IM_DIR=""
for candidate in "$APPDIR/usr/lib/gtk-3.0"/*/immodules; do
    if [ -d "$candidate" ]; then
        IM_DIR="$candidate"
        break
    fi
done

IM_STRATEGY="skipped"
IM_TMPL_REL=""
if [ -z "$IM_DIR" ]; then
    echo "  no immodules directory under $APPDIR/usr/lib/gtk-3.0 - nothing to do"
else
    echo "  immodules directory: $IM_DIR"

    IM_QUERY=""
    for candidate in \
        gtk-query-immodules-3.0 \
        /usr/bin/gtk-query-immodules-3.0 \
        /usr/bin/gtk-query-immodules-3.0-64 \
        /usr/bin/gtk-query-immodules-3.0-32 \
        /usr/lib64/libgtk-3-0/gtk-query-immodules-3.0 \
        /usr/lib/x86_64-linux-gnu/libgtk-3-0/gtk-query-immodules-3.0
    do
        if command -v "$candidate" >/dev/null 2>&1; then
            IM_QUERY="$(command -v "$candidate")"
            break
        fi
    done

    # Only the modules actually inside the AppDir belong in the cache.
    IM_MODULES=""
    for module in "$IM_DIR"/*.so; do
        if [ -f "$module" ]; then
            IM_MODULES="$IM_MODULES $module"
        fi
    done
    IM_MODULES="${IM_MODULES# }"

    IM_CACHE="$(dirname "$IM_DIR")/immodules.cache"
    IM_TEMPLATE="${IM_CACHE}.rescale-template"
    IM_TMPL_REL="${IM_TEMPLATE#$APPDIR/}"

    if [ -z "$IM_QUERY" ]; then
        echo "  WARNING: gtk-query-immodules-3.0 not found on the build host; leaving"
        echo "  WARNING: the plugin's cache as-is (harmless ibus warnings at runtime)."
    elif [ -z "$IM_MODULES" ]; then
        echo "  no bundled *.so input modules - leaving the plugin's cache as-is"
    else
        echo "  query tool: $IM_QUERY"
        echo "  bundled input modules:"
        for module in $IM_MODULES; do
            echo "    $(basename "$module")"
        done

        # Querying the AppDir copies makes the tool emit build-time absolute
        # paths inside $APPDIR; swap $APPDIR for a token the AppRun hook fills
        # in with the real mount point, which differs on every launch.
        # shellcheck disable=SC2086  # deliberate word splitting: one arg per module
        if "$IM_QUERY" $IM_MODULES > "$SCRATCH/immodules.raw" 2>"$SCRATCH/immodules.err"; then
            sed "s|$APPDIR|$APPDIR_TOKEN|g" "$SCRATCH/immodules.raw" > "$IM_TEMPLATE"
            IM_STRATEGY="template"
            echo "  wrote template: $IM_TEMPLATE"
            echo "  template contents:"
            sed 's/^/    /' "$IM_TEMPLATE" | head -30

            cat >> "$HOOK_ADD" <<EOF

# --- rescale-int: bundle-relative GTK immodules cache ----------------------
# The gtk plugin's immodules.cache names bare build-host filenames, which do
# not resolve and produce ibus warnings. Materialise one holding real paths
# inside the running bundle. This export wins because it comes after the
# plugin's own GTK_IM_MODULE_FILE. Sourced under 'set -e' - never fail.
_rs_im_tmpl="\$APPDIR/${IM_TMPL_REL}"
if [ -r "\$_rs_im_tmpl" ]; then
    _rs_im_tag="\$(printf '%s' "\$APPDIR" | cksum 2>/dev/null | cut -d' ' -f1 || printf 'x')"
    _rs_im_out="\${TMPDIR:-/tmp}/.rescale-int-immodules-\$(id -u 2>/dev/null || printf 'u').\${_rs_im_tag}.cache"
    if sed "s|${APPDIR_TOKEN}|\$APPDIR|g" "\$_rs_im_tmpl" > "\$_rs_im_out" 2>/dev/null; then
        export GTK_IM_MODULE_FILE="\$_rs_im_out"
    fi
fi
EOF
        else
            echo "  WARNING: $IM_QUERY failed; leaving the plugin's cache as-is:"
            sed 's/^/    /' "$SCRATCH/immodules.err" | head -10 || true
        fi
    fi
fi

# =============================================================================
# Step 8: Install the hook additions and the manifest
# =============================================================================
step "Step 8: installing AppRun hook additions and manifest"

if [ -s "$HOOK_ADD" ]; then
    # Append, never overwrite: the gtk plugin's own exports must keep running,
    # and ours must come after them.
    cat "$HOOK_ADD" >> "$HOOKFILE"
    echo "  appended $(wc -l < "$HOOK_ADD" | tr -d ' ') line(s) to $HOOK_REL"
else
    die "no hook additions were generated - refusing to ship an unwired bundle"
fi

echo "  hook now ends with:"
tail -30 "$HOOKFILE" | sed 's/^/    /'

# Verified by build/linux/verify-appimage.sh against the finished AppImage, and
# useful on its own when diagnosing a field report.
mkdir -p "$(dirname "$APPDIR/$MANIFEST_REL")"
cat > "$APPDIR/$MANIFEST_REL" <<EOF
# Generated by build/linux/bundle-webkit.sh - do not edit.
generated=$(date -u +%Y-%m-%dT%H:%M:%SZ)
build_host=$(uname -srm)
bundled_libs=$BUNDLED_LIBS
probe_lib=$PROBE_LIB
host_libexec=$HOST_LIBEXEC
helpers=${COPIED_HELPERS# }
libexec_rel=$LIBEXEC_REL
rpath=$RPATH_VALUE
rpath_files=$RPATH_COUNT
critical_sonames=$CRITICAL_SONAMES
path_strategy=$PATH_STRATEGY
patched_occurrences=$PATCHED_COUNT
embedded_libexec=$EMBEDDED_LIBEXEC
patched_libexec=$PATCH_LIBEXEC
patch_link_dir=$PATCH_LINK_DIR
injected_bundled=$INJECTED_BUNDLED
injected_strategy=$INJECTED_STRATEGY
injected_rel=$INJECTED_REL
immodules_strategy=$IM_STRATEGY
immodules_template=$IM_TMPL_REL
EOF

echo "  manifest: $APPDIR/$MANIFEST_REL"
sed 's/^/    /' "$APPDIR/$MANIFEST_REL"

# =============================================================================
# Summary
# =============================================================================
echo ""
echo "=============================================="
echo "WebKit bundling complete"
echo "=============================================="
echo "helper path strategy .... $PATH_STRATEGY"
echo "helpers bundled ......... ${COPIED_HELPERS# }"
echo "RPATH stamped ........... $RPATH_VALUE on $RPATH_COUNT file(s)"
echo "injected bundle ......... $INJECTED_BUNDLED (wiring: $INJECTED_STRATEGY)"
echo "immodules cache ......... $IM_STRATEGY"
echo "Finished: $(date)"
exit 0
