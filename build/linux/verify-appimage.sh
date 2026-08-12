#!/bin/bash
#
# verify-appimage.sh - Release gate for the Interlink Linux AppImage.
#
# THE BUG THIS GUARDS AGAINST: an AppImage that bundles libwebkit2gtk but none
# of the WebKit helper executables it forks (WebKitWebProcess,
# WebKitNetworkProcess) falls back to the HOST's helpers, and a mismatched host
# WebKit then fails the IPC handshake ("received invalid message:
# WebPage_GetWebArchiveOfFrameWithFileNameReply") so the window paints but the
# page never renders. That shipped once; this script exists so it cannot ship
# again.
#
# WHAT IT CHECKS, against the finished AppImage rather than the AppDir:
#   1. The helpers are present inside the image and executable.
#   2. THE IMPORTANT ONE: with LD_LIBRARY_PATH deliberately UNSET - because
#      nothing sets it at runtime, neither linuxdeploy's AppRun wrapper nor its
#      gtk plugin - every helper resolves all of its libraries, and resolves
#      libwebkit2gtk/libjavascriptcoregtk/libsoup/libgtk-3 to files INSIDE the
#      extracted bundle. Setting LD_LIBRARY_PATH here would hide a missing
#      RPATH, which is exactly how the original bug reached customers: on a
#      build machine with a matching host WebKit, host resolution looks fine.
#      Libraries on linuxdeploy's deliberate excludelist (glibc, ld-linux,
#      libGL, libX11/xcb, libdrm and friends) are expected from the host.
#   3. build/linux/bundle-webkit.sh's manifest claims match reality - the env
#      export it says it wrote is really in the AppRun hook, or the binary
#      patch it says it applied is really in the library.
#   4. The GTK immodules cache holds no build-host absolute paths.
#   5. Best effort, and never fatal on its own: launch the thing and confirm a
#      real WebKitWebProcess starts from inside the extracted bundle.
#
# Hard assertion failures exit non-zero, which fails the build job. The runtime
# check reports RUNTIME-VERIFIED or RUNTIME-CHECK-INCONCLUSIVE and never fails
# the gate by itself - a headless build node is not a reliable GUI host.
#
# Extraction happens in its own mktemp directory, deliberately NOT in the build
# directory: the build job juggles several 'squashfs-root' directories of its
# own and this must not disturb them.
#
# Note: the runtime check really does start the application, so it may create a
# user config directory on the build node. Build nodes are disposable.
#
# Usage: bash build/linux/verify-appimage.sh <path-to-AppImage>

set -euo pipefail

FAILURES=0
TMPD=""

fail() {
    echo "  FAIL  $*"
    FAILURES=$((FAILURES + 1))
}

pass() {
    echo "  ok    $*"
}

note() {
    echo "  --    $*"
}

section() {
    echo ""
    echo "--- $* ---"
}

cleanup() {
    if [ -n "$TMPD" ] && [ -d "$TMPD" ]; then
        if [ "$FAILURES" -eq 0 ]; then
            rm -rf "$TMPD" 2>/dev/null || true
        else
            echo ""
            echo "Extraction kept for inspection: $TMPD"
        fi
    fi
}
trap cleanup EXIT

# =============================================================================
# Arguments
# =============================================================================

APPIMAGE="${1:-}"
if [ -z "$APPIMAGE" ]; then
    echo "usage: verify-appimage.sh <path-to-AppImage>" >&2
    exit 2
fi
if [ ! -f "$APPIMAGE" ]; then
    echo "ERROR: no such file: $APPIMAGE" >&2
    exit 2
fi
APPIMAGE="$(cd "$(dirname "$APPIMAGE")" && pwd)/$(basename "$APPIMAGE")"
if [ ! -x "$APPIMAGE" ]; then
    echo "ERROR: not executable, cannot self-extract: $APPIMAGE" >&2
    exit 2
fi

echo "=============================================="
echo "AppImage verification gate"
echo "=============================================="
echo "AppImage: $APPIMAGE"
echo "Size:     $(du -h "$APPIMAGE" | awk '{print $1}')"
echo "Started:  $(date)"

# =============================================================================
# Extract
# =============================================================================
section "Extracting"

TMPD="$(mktemp -d "${TMPDIR:-/tmp}/rs-appimage-verify.XXXXXX")"
cd "$TMPD"

if ! "$APPIMAGE" --appimage-extract > "$TMPD/extract.log" 2>&1; then
    echo "ERROR: --appimage-extract failed:"
    tail -30 "$TMPD/extract.log" || true
    exit 1
fi

ROOT="$TMPD/squashfs-root"
if [ ! -d "$ROOT" ]; then
    echo "ERROR: extraction produced no squashfs-root in $TMPD"
    exit 1
fi
note "extracted to $ROOT"

LIBDIR="$ROOT/usr/lib"
HELPER_DIR="$ROOT/usr/libexec/webkit2gtk-4.0"
MANIFEST="$ROOT/usr/share/rescale-int/webkit-bundle.manifest"
HOOKFILE="$ROOT/apprun-hooks/linuxdeploy-plugin-gtk.sh"

# =============================================================================
# Check 1: helper executables present
# =============================================================================
section "Check 1: WebKit helper executables"

if [ -d "$HELPER_DIR" ]; then
    pass "helper directory exists: usr/libexec/webkit2gtk-4.0"
    ls -la "$HELPER_DIR" | sed 's/^/        /'
else
    fail "no usr/libexec/webkit2gtk-4.0 directory in the AppImage"
    note "usr/libexec listing:"
    ls -la "$ROOT/usr/libexec" 2>/dev/null | sed 's/^/        /' || note "(no usr/libexec at all)"
fi

HELPERS_PRESENT=""
for helper in WebKitWebProcess WebKitNetworkProcess; do
    if [ -f "$HELPER_DIR/$helper" ]; then
        if [ -x "$HELPER_DIR/$helper" ]; then
            pass "$helper present and executable"
            HELPERS_PRESENT="$HELPERS_PRESENT $HELPER_DIR/$helper"
        else
            fail "$helper present but NOT executable"
        fi
    else
        fail "$helper missing - the AppImage would fork the host's helper"
    fi
done

# =============================================================================
# Check 2: dependencies resolve inside the bundle
# =============================================================================
section "Check 2: dynamic dependencies resolve inside the bundle"

BUNDLED_LIB=""
for candidate in "$LIBDIR"/libwebkit2gtk-4.0.so*; do
    if [ -f "$candidate" ] && [ ! -L "$candidate" ]; then
        BUNDLED_LIB="$candidate"
        break
    fi
done

if [ -n "$BUNDLED_LIB" ]; then
    pass "bundled library: ${BUNDLED_LIB#$ROOT/}"
else
    fail "no libwebkit2gtk-4.0.so* regular file in usr/lib"
fi

if ! command -v ldd >/dev/null 2>&1; then
    fail "'ldd' not found - cannot verify that dependencies resolve inside the bundle"
fi

# Sonames that must come from inside the bundle. Anything else may legitimately
# come from the host: linuxdeploy deliberately excludes glibc, the dynamic
# loader, GL, X11/xcb, libdrm and similar, because bundling them breaks harder
# than sharing them.
CRITICAL_SONAMES="libwebkit2gtk-4.0 libjavascriptcoregtk libsoup libgtk-3"

check_ldd() {
    local target="$1"
    local label="$2"
    local name out line soname resolved crit_seen
    name="$(basename "$target")"
    out="$TMPD/ldd.$name.out"

    if ! command -v ldd >/dev/null 2>&1; then
        return
    fi

    # LD_LIBRARY_PATH is UNSET on purpose - this must reproduce what the loader
    # does on a user's machine, where nothing sets it. ldd honours DT_RUNPATH
    # with $ORIGIN expanded, so a correctly stamped RPATH resolves into the
    # bundle and a missing one falls through to the host's ld.so.cache.
    env -u LD_LIBRARY_PATH ldd "$target" > "$out" 2>&1 || true

    echo "        --- ldd $name (LD_LIBRARY_PATH unset) ---"
    sed 's/^/          /' "$out"

    if grep -q 'not a dynamic executable' "$out"; then
        fail "$label $name: ldd says not a dynamic executable"
        return
    fi
    if [ ! -s "$out" ]; then
        fail "$label $name: ldd produced no output"
        return
    fi
    if grep -q 'not found' "$out"; then
        fail "$label $name: unresolved libraries with LD_LIBRARY_PATH unset"
        grep 'not found' "$out" | sed 's/^/        /'
        return
    fi
    pass "$label $name: no unresolved libraries"

    # Every critical soname that appears must resolve under the extraction root.
    crit_seen=0
    for soname in $CRITICAL_SONAMES; do
        line="$(grep -F -- "$soname" "$out" | grep -F '=>' | head -1 || true)"
        if [ -z "$line" ]; then
            continue
        fi
        crit_seen=$((crit_seen + 1))
        resolved="${line#*=> }"
        resolved="${resolved%% (*}"
        case "$resolved" in
            "$ROOT"/*)
                pass "$label $name: $soname -> ${resolved#$ROOT/} (inside bundle)"
                ;;
            *)
                fail "$label $name: $soname resolves to '$resolved' - OUTSIDE the bundle"
                note "this is the host's copy; a mismatched host WebKit is the"
                note "exact defect this gate exists to catch (missing RPATH?)"
                ;;
        esac
    done
    if [ "$crit_seen" -eq 0 ]; then
        note "$label $name links none of: $CRITICAL_SONAMES"
    fi
}

if command -v patchelf >/dev/null 2>&1; then
    note "RPATH on the bundled helpers (patchelf --print-rpath):"
    # shellcheck disable=SC2086  # deliberate word splitting over the helper list
    for target in $HELPERS_PRESENT; do
        echo "        $(basename "$target"): $(patchelf --print-rpath "$target" 2>/dev/null || echo '<none>')"
    done
elif command -v readelf >/dev/null 2>&1; then
    note "RPATH/RUNPATH on the bundled helpers (readelf -d):"
    # shellcheck disable=SC2086  # deliberate word splitting over the helper list
    for target in $HELPERS_PRESENT; do
        echo "        $(basename "$target"): $(readelf -d "$target" 2>/dev/null | grep -E 'RUNPATH|RPATH' | head -1 || echo '<none>')"
    done
else
    note "neither patchelf nor readelf available; relying on ldd resolution below"
fi

# shellcheck disable=SC2086  # deliberate word splitting over the helper list
for target in $HELPERS_PRESENT; do
    check_ldd "$target" "helper"
done
if [ -n "$BUNDLED_LIB" ]; then
    check_ldd "$BUNDLED_LIB" "library"
fi

# The injected bundle is dlopened into every web process, so it has the same
# requirement as the helpers.
for candidate in "$ROOT"/usr/libexec/*/injected-bundle/*.so* "$ROOT"/usr/libexec/injected-bundle/*.so*; do
    if [ -f "$candidate" ]; then
        check_ldd "$candidate" "injected-bundle"
    fi
done

# =============================================================================
# Check 3: the bundling manifest matches reality
# =============================================================================
section "Check 3: helper-path wiring matches the bundling manifest"

PATH_STRATEGY=""
if [ -f "$MANIFEST" ]; then
    pass "manifest present: ${MANIFEST#$ROOT/}"
    sed 's/^/        /' "$MANIFEST"
    PATH_STRATEGY="$(sed -n 's/^path_strategy=//p' "$MANIFEST" | head -1)"
    PATCH_LIBEXEC="$(sed -n 's/^patched_libexec=//p' "$MANIFEST" | head -1)"
    EMBEDDED_LIBEXEC="$(sed -n 's/^embedded_libexec=//p' "$MANIFEST" | head -1)"
    PATCH_LINK_DIR="$(sed -n 's/^patch_link_dir=//p' "$MANIFEST" | head -1)"
else
    fail "no webkit-bundle.manifest - bundle-webkit.sh did not run on this AppDir"
    PATCH_LIBEXEC=""
    EMBEDDED_LIBEXEC="/usr/libexec/webkit2gtk-4.0"
    PATCH_LINK_DIR=""
fi

if [ -f "$HOOKFILE" ]; then
    pass "AppRun hook present: ${HOOKFILE#$ROOT/}"
else
    fail "no apprun-hooks/linuxdeploy-plugin-gtk.sh - nothing we exported can run"
fi

case "$PATH_STRATEGY" in
    env)
        note "manifest claims the WEBKIT_EXEC_PATH env override"
        if [ -f "$HOOKFILE" ] && grep -q 'export WEBKIT_EXEC_PATH=' "$HOOKFILE"; then
            pass "hook exports WEBKIT_EXEC_PATH"
            grep -n 'WEBKIT_EXEC_PATH' "$HOOKFILE" | sed 's/^/        /'
        else
            fail "manifest says path_strategy=env but the hook has no WEBKIT_EXEC_PATH export"
        fi
        ;;
    patch)
        note "manifest claims an embedded-path patch to '$PATCH_LIBEXEC'"
        if [ -n "$BUNDLED_LIB" ] && [ -n "$PATCH_LIBEXEC" ]; then
            if grep -q -a -F -- "$PATCH_LIBEXEC" "$BUNDLED_LIB"; then
                pass "bundled library contains the patched path"
            else
                fail "bundled library does NOT contain the patched path '$PATCH_LIBEXEC'"
            fi
            if grep -q -a -F -- "$EMBEDDED_LIBEXEC" "$BUNDLED_LIB"; then
                fail "bundled library STILL contains '$EMBEDDED_LIBEXEC' - it would fork the host's helpers"
            else
                pass "no unpatched '$EMBEDDED_LIBEXEC' left in the bundled library"
            fi
        else
            fail "cannot check the patch: missing library or manifest field"
        fi
        # Match the actual commands, not the comment block that mentions the
        # path - a comment would satisfy a naive substring search while the
        # symlink never gets created.
        if [ -f "$HOOKFILE" ] && [ -n "$PATCH_LINK_DIR" ]; then
            if grep -q -F -- '_rs_wk_link="'"$PATCH_LINK_DIR"'"' "$HOOKFILE"; then
                pass "hook binds the link path: _rs_wk_link=\"$PATCH_LINK_DIR\""
            else
                fail "hook never assigns _rs_wk_link=\"$PATCH_LINK_DIR\" - the patched path would dangle"
            fi
            if grep -q -F -- 'ln -sfn "$_rs_wk_want" "$_rs_wk_link"' "$HOOKFILE"; then
                pass "hook runs the ln -sfn that creates the link at launch"
            else
                fail "hook has no 'ln -sfn \"\$_rs_wk_want\" \"\$_rs_wk_link\"' command"
            fi
            # The hook must refuse to launch rather than exec through a link it
            # could not claim.
            if grep -q -F -- 'cannot claim' "$HOOKFILE" && grep -q -E '^[[:space:]]*exit 1$' "$HOOKFILE"; then
                pass "hook refuses to launch if the link is not ours (ownership + target checked)"
            else
                fail "hook does not bail out when '$PATCH_LINK_DIR' cannot be claimed"
            fi
        else
            fail "cannot check the link wiring: missing hook or manifest field"
        fi
        ;;
    "")
        fail "no path_strategy in the manifest"
        ;;
    *)
        fail "unknown path_strategy '$PATH_STRATEGY' in the manifest"
        ;;
esac

# =============================================================================
# Check 4: immodules cache holds no build-host paths
# =============================================================================
section "Check 4: GTK immodules cache"

IM_CACHES=""
for candidate in "$LIBDIR"/gtk-3.0/*/immodules.cache "$LIBDIR"/gtk-3.0/*/immodules.cache.rescale-template; do
    if [ -f "$candidate" ]; then
        IM_CACHES="$IM_CACHES $candidate"
    fi
done

# An AppDir with no input-method modules has no cache to check, and the bundler
# records that as immodules_strategy=skipped. Both signals are read: the
# manifest's claim and the directory's actual absence. Only a bundle that HAS
# input modules is required to have a sane cache.
IM_DIR_PRESENT="no"
for candidate in "$LIBDIR"/gtk-3.0/*/immodules; do
    if [ -d "$candidate" ]; then
        IM_DIR_PRESENT="yes"
        break
    fi
done

IM_STRATEGY=""
if [ -f "$MANIFEST" ]; then
    IM_STRATEGY="$(sed -n 's/^immodules_strategy=//p' "$MANIFEST" | head -1)"
fi

if [ "$IM_DIR_PRESENT" = "no" ]; then
    note "no usr/lib/gtk-3.0/*/immodules directory in this bundle"
    note "manifest immodules_strategy=${IM_STRATEGY:-<unset>}"
    if [ "$IM_STRATEGY" = "skipped" ] || [ -z "$IM_STRATEGY" ]; then
        pass "Check 4 skipped: nothing to cache, and the bundler agrees"
    else
        fail "manifest claims immodules_strategy=$IM_STRATEGY but there is no immodules directory"
    fi
elif [ -z "$IM_CACHES" ]; then
    fail "immodules directory present but no immodules.cache under usr/lib/gtk-3.0"
else
    # Anchor to the start of a line or of a quoted field. An unanchored
    # '/usr/lib/' also matches our own '@RS_APPDIR@/usr/lib/...' template
    # entries, which are correct and must not fail the gate.
    HOST_PATH_RE='(^|")/usr/(lib64|lib)/'
    # shellcheck disable=SC2086  # deliberate word splitting over the cache list
    for cache in $IM_CACHES; do
        rel="${cache#$ROOT/}"
        pass "present: $rel ($(wc -l < "$cache" | tr -d ' ') lines)"
        if grep -q -E "$HOST_PATH_RE" "$cache"; then
            fail "$rel contains build-host absolute paths:"
            grep -n -E "$HOST_PATH_RE" "$cache" | head -5 | sed 's/^/        /'
        else
            pass "$rel has no absolute /usr/lib64 or /usr/lib module paths"
        fi
    done
fi

TEMPLATE=""
for candidate in "$LIBDIR"/gtk-3.0/*/immodules.cache.rescale-template; do
    if [ -f "$candidate" ]; then
        TEMPLATE="$candidate"
        break
    fi
done
if [ -n "$TEMPLATE" ]; then
    if grep -q -F '@RS_APPDIR@' "$TEMPLATE"; then
        pass "template uses the @RS_APPDIR@ placeholder, resolved at launch"
    else
        note "template has no @RS_APPDIR@ placeholder (no input modules bundled?)"
    fi
    if grep -q -F '/tmp/build' "$TEMPLATE"; then
        fail "template leaks the build directory /tmp/build"
    else
        pass "template leaks no build-time directory"
    fi
else
    note "no immodules template - bundle-webkit.sh skipped the cache regeneration"
fi

# =============================================================================
# Check 5: best-effort runtime check (never fatal on its own)
# =============================================================================
section "Check 5: runtime check (best effort)"

runtime_check() {
    local disp="${DISPLAY:-}"
    local xvfb_pid=""

    if [ -z "$disp" ]; then
        if command -v Xvfb >/dev/null 2>&1; then
            Xvfb :97 -screen 0 1024x768x24 >"$TMPD/xvfb.log" 2>&1 &
            xvfb_pid=$!
            disp=":97"
            note "started our own Xvfb on :97 (pid $xvfb_pid)"
            sleep 2
        else
            echo "  RUNTIME-CHECK-INCONCLUSIVE (no DISPLAY and no Xvfb available)"
            return 0
        fi
    else
        note "using existing DISPLAY=$disp"
    fi

    if [ ! -d /proc ]; then
        echo "  RUNTIME-CHECK-INCONCLUSIVE (no /proc to inspect)"
        return 0
    fi

    note "launching $ROOT/AppRun"
    DISPLAY="$disp" "$ROOT/AppRun" >"$TMPD/apprun.log" 2>&1 &
    local app_pid=$!

    local found=0
    local outside=0
    local waited=0
    local webkit_pids=""

    # ppid of a pid, read from /proc/<pid>/stat. Field 2 is the comm value in
    # parentheses and may itself contain spaces or parentheses, so everything up
    # to the last ") " is discarded before fields are counted.
    ppid_of() {
        local pid="$1" line rest
        line="$(cat "/proc/$pid/stat" 2>/dev/null || true)"
        if [ -z "$line" ]; then
            return 0
        fi
        rest="${line##*) }"
        printf '%s\n' "$rest" | cut -d' ' -f2
    }

    # True when pid descends from app_pid. Only our own process tree counts -
    # an unrelated WebKit on the build node must not be mistaken for ours.
    is_descendant() {
        local pid="$1" hops=0 parent
        while [ "$hops" -lt 24 ]; do
            hops=$((hops + 1))
            if [ "$pid" = "$app_pid" ]; then
                return 0
            fi
            if [ -z "$pid" ] || [ "$pid" = "0" ] || [ "$pid" = "1" ]; then
                return 1
            fi
            parent="$(ppid_of "$pid")"
            if [ -z "$parent" ] || [ "$parent" = "$pid" ]; then
                return 1
            fi
            pid="$parent"
        done
        return 1
    }

    while [ "$waited" -lt 12 ]; do
        sleep 1
        waited=$((waited + 1))

        for procdir in /proc/[0-9]*; do
            exe="$(readlink "$procdir/exe" 2>/dev/null || true)"
            case "$exe" in
                */WebKitWebProcess|*/WebKitWebProcess" (deleted)")
                    pid="${procdir#/proc/}"
                    if ! is_descendant "$pid"; then
                        continue
                    fi
                    webkit_pids="$webkit_pids $pid"
                    case "$exe" in
                        "$TMPD"/*)
                            found=1
                            note "WebKitWebProcess pid $pid (descendant of $app_pid) exe=$exe"
                            ;;
                        *)
                            outside=1
                            note "WebKitWebProcess pid $pid (descendant of $app_pid) exe=$exe"
                            note "  that path is outside the extraction dir $TMPD"
                            ;;
                    esac
                    ;;
            esac
        done

        if [ "$found" -eq 1 ]; then
            break
        fi
        if ! kill -0 "$app_pid" 2>/dev/null; then
            note "AppRun exited after ${waited}s"
            break
        fi
    done

    # Tear down whatever is still running.
    kill "$app_pid" 2>/dev/null || true
    for pid in $webkit_pids; do
        kill "$pid" 2>/dev/null || true
    done
    wait "$app_pid" 2>/dev/null || true
    if [ -n "$xvfb_pid" ]; then
        kill "$xvfb_pid" 2>/dev/null || true
    fi

    if [ "$found" -eq 1 ]; then
        echo "  RUNTIME-VERIFIED (WebKitWebProcess ran from inside the bundle)"
    elif [ "$outside" -eq 1 ]; then
        # Suspicious, but not a verdict: this runs on a headless build node with
        # a matching host WebKit installed, so process attribution here is not
        # reliable enough to block a release. Checks 2 and 3 are the hard
        # guarantees; this is a signal for a human to read.
        echo "  RUNTIME-CHECK-INCONCLUSIVE (a descendant WebKitWebProcess ran from outside $TMPD)"
        note "check the Check 2 results above - a missing RPATH looks like this"
        note "last 20 lines of the application log:"
        tail -20 "$TMPD/apprun.log" 2>/dev/null | sed 's/^/        /' || true
    else
        echo "  RUNTIME-CHECK-INCONCLUSIVE (no WebKitWebProcess observed in ${waited}s)"
        note "last 20 lines of the application log:"
        tail -20 "$TMPD/apprun.log" 2>/dev/null | sed 's/^/        /' || true
    fi
    return 0
}

runtime_check

# =============================================================================
# Summary
# =============================================================================
echo ""
echo "=============================================="
if [ "$FAILURES" -eq 0 ]; then
    echo "PASS: AppImage verification gate"
    echo "=============================================="
    echo "helper path strategy: ${PATH_STRATEGY:-unknown}"
    echo "Finished: $(date)"
    exit 0
fi

echo "FAIL: AppImage verification gate ($FAILURES failed assertion(s))"
echo "=============================================="
echo "This AppImage must not be shipped. The most likely cause is that"
echo "build/linux/bundle-webkit.sh did not run, or ran against a different"
echo "AppDir than the one appimagetool packaged."
echo "Finished: $(date)"
exit 1
