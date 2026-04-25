//go:build linux

// Package wailsapp — xdg-desktop-portal FileChooser dialog path.
//
// Routes file/folder picker calls through the freedesktop portal service
// over the user session D-Bus, bypassing Wails/WebKit's embedded GTK file
// chooser entirely. Fixes the Linux #41 SIGTRAP crash on hosts whose GTK
// GSettings schema is missing the 'show-type-column' key (the customer's
// RHEL 9 VDI case).
//
// xdg-desktop-portal ships as a platform default on RHEL 9 GNOME sessions
// (xdg-desktop-portal + xdg-desktop-portal-gtk RPMs). Minimal WMs without
// a portal backend automatically fall back to the legacy Wails GTK path
// via errPortalUnavailable classification. Users on broken-portal hosts
// can force the GTK path with RESCALE_DISABLE_PORTAL=1.
package wailsapp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	portalBusName     = "org.freedesktop.portal.Desktop"
	portalObjPath     = "/org/freedesktop/portal/desktop"
	portalFileCh      = "org.freedesktop.portal.FileChooser"
	portalReqIface    = "org.freedesktop.portal.Request"
	portalReqClose    = "org.freedesktop.portal.Request.Close"
	portalRespOnSig   = portalReqIface + ".Response"
	portalDialogTimeout    = 5 * time.Minute
	portalStillOpenWarn    = 30 * time.Second
	portalRequestCloseWait = 2 * time.Second
)

const (
	portalResponseOK     uint32 = 0
	portalResponseCancel uint32 = 1
	portalResponseOther  uint32 = 2
)

// errPortalUnavailable marks errors that mean "portal service is not
// available; fall through to the legacy GTK path". Network/timeout errors
// are deliberately NOT classified this way — they surface to the caller
// so the user sees an actionable hint about RESCALE_DISABLE_PORTAL=1.
var errPortalUnavailable = errors.New("portal: unavailable")

// portalEnabled reports whether the portal dialog path is active. On
// Linux it is the default; users can opt out with RESCALE_DISABLE_PORTAL=1.
func portalEnabled() bool {
	return os.Getenv("RESCALE_DISABLE_PORTAL") != "1"
}

// isPortalUnavailable reports whether err indicates the portal service
// itself is missing/unreachable (not an in-flight timeout or user-level
// failure). Used to decide whether to fall back to the Wails GTK dialog
// path instead of surfacing the error.
func isPortalUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		switch dbusErr.Name {
		case "org.freedesktop.DBus.Error.ServiceUnknown",
			"org.freedesktop.DBus.Error.UnknownMethod",
			"org.freedesktop.DBus.Error.UnknownInterface",
			"org.freedesktop.DBus.Error.UnknownObject",
			"org.freedesktop.DBus.Error.NoReply",
			"org.freedesktop.DBus.Error.NotSupported",
			"org.freedesktop.DBus.Error.Spawn.ServiceNotFound":
			return true
		}
	}
	return errors.Is(err, errPortalUnavailable)
}

// portalCall invokes a FileChooser method and blocks for the matching
// Request.Response signal. All public entry points funnel through here.
// Shares a single 5-minute deadline across the AddMatchSignalContext, the
// CallWithContext, and the Response-signal wait; on timeout after reqPath
// is known, best-effort Request.Close is fired so we don't leak dialogs.
func portalCall(method, parent, title string, opts map[string]dbus.Variant) ([]string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: connect session bus: %v", errPortalUnavailable, err)
	}
	defer conn.Close()

	obj := conn.Object(portalBusName, portalObjPath)

	ctx, cancel := context.WithTimeout(context.Background(), portalDialogTimeout)
	defer cancel()

	// Sign up for Request.Response BEFORE calling the method — a very fast
	// portal can respond between our call-return and signal subscription.
	ch := make(chan *dbus.Signal, 4)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)

	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchInterface(portalReqIface),
		dbus.WithMatchMember("Response"),
	); err != nil {
		return nil, fmt.Errorf("%w: AddMatchSignal: %v", errPortalUnavailable, err)
	}
	defer func() {
		// Use Background so cleanup survives even after ctx expires.
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), portalRequestCloseWait)
		defer cleanCancel()
		_ = conn.RemoveMatchSignalContext(cleanCtx,
			dbus.WithMatchInterface(portalReqIface),
			dbus.WithMatchMember("Response"),
		)
	}()

	var reqPath dbus.ObjectPath
	methodFQN := portalFileCh + "." + method
	if err := obj.CallWithContext(ctx, methodFQN, 0, parent, title, opts).Store(&reqPath); err != nil {
		return nil, err
	}

	wailsLogger.Debug().
		Str("method", method).
		Str("request_path", string(reqPath)).
		Msg("portal call issued; waiting for Response signal")

	// "Portal dialog still open after 30s" watchdog — helpful when a user
	// leaves a dialog open or the portal hangs.
	stillOpen := time.AfterFunc(portalStillOpenWarn, func() {
		wailsLogger.Warn().Str("method", method).Str("request_path", string(reqPath)).
			Msg("portal dialog still open after 30s")
	})
	defer stillOpen.Stop()

	for {
		select {
		case sig, ok := <-ch:
			if !ok {
				return nil, fmt.Errorf("portal: signal channel closed")
			}
			if sig.Path != reqPath {
				continue
			}
			if sig.Name != portalRespOnSig {
				continue
			}
			if len(sig.Body) < 2 {
				return nil, fmt.Errorf("portal: malformed Response signal (len(body)=%d)", len(sig.Body))
			}
			response, ok := sig.Body[0].(uint32)
			if !ok {
				return nil, fmt.Errorf("portal: Response arg 0 not uint32 (got %T)", sig.Body[0])
			}
			results, ok := sig.Body[1].(map[string]dbus.Variant)
			if !ok {
				return nil, fmt.Errorf("portal: Response arg 1 not map[string]variant (got %T)", sig.Body[1])
			}
			switch response {
			case portalResponseOK:
				return extractURIs(results)
			case portalResponseCancel:
				return nil, nil // user cancelled — empty selection, no error
			case portalResponseOther:
				return nil, fmt.Errorf("portal: dialog returned error (response=2); try setting RESCALE_DISABLE_PORTAL=1 to use the legacy Wails GTK dialog")
			default:
				return nil, fmt.Errorf("portal: unknown response code %d", response)
			}
		case <-ctx.Done():
			// Best-effort: close the orphaned request so we don't leave a
			// dialog dangling in the portal's state.
			closeCtx, closeCancel := context.WithTimeout(context.Background(), portalRequestCloseWait)
			defer closeCancel()
			reqObj := conn.Object(portalBusName, reqPath)
			_ = reqObj.CallWithContext(closeCtx, portalReqClose, 0).Err
			return nil, fmt.Errorf("portal: timeout after %s waiting for Response signal; try setting RESCALE_DISABLE_PORTAL=1 to use the legacy Wails GTK dialog", portalDialogTimeout)
		}
	}
}

// extractURIs reads the "uris" key from a portal Response results map
// (both OpenFile and SaveFile use it) and returns filesystem paths.
func extractURIs(results map[string]dbus.Variant) ([]string, error) {
	urisV, ok := results["uris"]
	if !ok {
		// No selection returned; treat as cancellation.
		return nil, nil
	}
	uris, ok := urisV.Value().([]string)
	if !ok {
		return nil, fmt.Errorf("portal: uris result not []string (got %T)", urisV.Value())
	}
	out := make([]string, 0, len(uris))
	for _, u := range uris {
		p, err := uriToPath(u)
		if err != nil {
			return nil, fmt.Errorf("portal: invalid uri %q: %w", u, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func uriToPath(u string) (string, error) {
	if !strings.HasPrefix(u, "file://") {
		return "", fmt.Errorf("not a file:// uri")
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	return parsed.Path, nil
}

// buildPortalSaveOptions builds the FileChooser.SaveFile options dict
// from a Wails SaveDialogOptions. current_name is type 's' (string);
// filters is a(sa(us)) per the portal spec, with Wails semicolon-separated
// patterns split into one glob sub-tuple each.
func buildPortalSaveOptions(defaultName string, filters []runtime.FileFilter) map[string]dbus.Variant {
	out := map[string]dbus.Variant{}
	if defaultName != "" {
		out["current_name"] = dbus.MakeVariant(defaultName)
	}
	if len(filters) > 0 {
		out["filters"] = dbus.MakeVariant(convertFilters(filters))
	}
	return out
}

// filterTuple mirrors the portal spec's filter tuple shape:
//
//	(s, a(us))  — display name + list of (type, pattern) pairs
//	where type 0 = glob, type 1 = mimetype.
type filterTuple struct {
	DisplayName string
	Patterns    []filterPattern
}

type filterPattern struct {
	Type    uint32
	Pattern string
}

// convertFilters translates Wails FileFilter (semicolon-separated globs
// in Pattern) into the portal's a(sa(us)) shape. Empty patterns are
// dropped; whitespace is trimmed.
func convertFilters(filters []runtime.FileFilter) []filterTuple {
	out := make([]filterTuple, 0, len(filters))
	for _, f := range filters {
		parts := strings.Split(f.Pattern, ";")
		patterns := make([]filterPattern, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			patterns = append(patterns, filterPattern{Type: 0, Pattern: p})
		}
		if len(patterns) == 0 {
			continue
		}
		out = append(out, filterTuple{DisplayName: f.DisplayName, Patterns: patterns})
	}
	return out
}

// portalOpenFile shows a single-file open dialog via the portal.
func portalOpenFile(parent, title string) (string, error) {
	paths, err := portalCall("OpenFile", parent, title, map[string]dbus.Variant{})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}

// portalOpenDirectory shows a directory picker via the portal (OpenFile
// with directory=true — the portal API has no dedicated method).
func portalOpenDirectory(parent, title string) (string, error) {
	paths, err := portalCall("OpenFile", parent, title, map[string]dbus.Variant{
		"directory": dbus.MakeVariant(true),
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}

// portalOpenMultipleFiles shows a multi-file open dialog.
func portalOpenMultipleFiles(parent, title string) ([]string, error) {
	paths, err := portalCall("OpenFile", parent, title, map[string]dbus.Variant{
		"multiple": dbus.MakeVariant(true),
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// portalSaveFile shows a save-file dialog. defaultName / filters map to
// the portal's current_name / filters options.
func portalSaveFile(parent, title, defaultName string, filters []runtime.FileFilter) (string, error) {
	paths, err := portalCall("SaveFile", parent, title, buildPortalSaveOptions(defaultName, filters))
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}
