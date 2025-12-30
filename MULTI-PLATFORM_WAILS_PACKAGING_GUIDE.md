# Interlink (Wails) Build & Packaging Guide
**Targets:** macOS (Apple Silicon), Windows Server 2019 (amd64), RHEL-like 8.x (amd64)  
**Goals:**  
- “Portable” distribution (no root/admin installer required for normal use)  
- Minimal external dependencies (bundle what we can)  
- Enterprise-friendly behavior on minimal OS installs  
- Output suitable for automation / AI-driven implementation

---

## 0) Hard reality check: “no warnings” vs “no money”
### macOS
Gatekeeper is designed to warn/block software that is not from an identified developer and notarized. Apple’s own security guide describes Gatekeeper verifying software is from an identified developer and notarized. :contentReference[oaicite:0]{index=0}

To be “clean” (no Gatekeeper prompts for typical web-browser downloads), the normal path is **Developer ID signing + notarization**. Apple’s Developer ID program + notarization workflow requires Apple Developer Program membership, which costs **$99/year**.
➡️ If *spending money is off the table*, you cannot guarantee a universally “prompt-free” macOS first-run experience in the typical “download in browser → double click” flow.

**However:** Gatekeeper prompts are triggered by the `com.apple.quarantine` attribute, which is applied by many “quarantine-aware” download agents (browsers, etc.). This attribute is opt-in by the downloading application.
Tools like `curl` typically do **not** set quarantine, which is one reason adversaries have used it to bypass Gatekeeper checks.
➡️ In practice, a *no-cost* way to avoid prompts is: **ensure the app is not quarantined** (download/untar via CLI, or remove quarantine xattrs before first run).

### Windows
Windows “Unknown Publisher” and Microsoft SmartScreen warnings are fundamentally tied to lack of code signing + reputation. “Unknown Publisher” warnings occur when an application is not signed with a trusted code signing certificate.
Avoiding SmartScreen warnings reliably generally requires a paid code signing certificate (EV is the fast path). :contentReference[oaicite:5]{index=5}  
➡️ With $0 spend, you should assume some users will see warnings on first run, and you should plan UX/docs accordingly.

### Linux
Linux generally does not show OS-level “unsigned developer” prompts for binaries the same way macOS/Windows do. Your main risk is **missing runtime libraries** (GTK/WebKitGTK) on minimal systems—so bundling is the priority.

---

## 1) Wails runtime dependencies that matter for packaging
- **Windows:** Wails uses **WebView2**; you can ship a **Fixed Version WebView2 Runtime** alongside your app and point Wails to it. :contentReference[oaicite:6]{index=6}
- **macOS:** Wails uses system WebKit (WKWebView). Desktop apps are normally packaged as a `.app` bundle with Info.plist metadata. Apple documents bundles as structured directories and requires Info.plist for runnable bundles. :contentReference[oaicite:7]{index=7}
- **Linux:** Wails uses **WebKitGTK** on Linux. :contentReference[oaicite:8]{index=8}  
  Minimal Rocky/Alma installs often won’t have WebKitGTK/GTK, and users may not have root to install them—so we bundle them.

---

## 2) Recommended distribution artifacts (one downloadable file per OS)
### macOS (Apple Silicon)
- `interlink-macos-arm64.tar.gz`
  - Contains: `Interlink.app/` only (+ optional tiny helper script + checksums)

### Windows Server 2019 (amd64)
- `interlink-windows-amd64.zip`
  - Contains: `Interlink.exe`, `webview2/` (Fixed Version Runtime), optional `README.txt`

### RHEL-like 8.x (amd64)
- `interlink-linux-rhel8-amd64.tar.gz`
  - Contains: `Interlink.AppDir/` (prebuilt AppDir, not an AppImage)
  - User runs: `./Interlink.AppDir/AppRun`

---

## 3) macOS packaging: “bundle + tarball” and a **no-cost warning-minimization strategy**
### 3.1 Build output
Wails produces the production-ready output via `wails build` (binary output goes to `build/bin`). :contentReference[oaicite:9]{index=9}  
On macOS, Wails packaging is a `.app` bundle (standard macOS format). :contentReference[oaicite:10]{index=10}

### 3.2 Why `.app` is required
A macOS app bundle is a directory with standardized structure containing executable + resources + metadata. Apple documents bundle structure and Info.plist placement/requirements. :contentReference[oaicite:11]{index=11}

### 3.3 “No warnings” options
#### Option A (Paid, best UX) — **OFF THE TABLE**
- Developer ID signing + notarization
- Requires Apple Developer Program membership ($99/year) :contentReference[oaicite:12]{index=12}
- Apple notes that Developer ID distributed software is expected to be notarized (macOS 10.15+ rules). :contentReference[oaicite:13]{index=13}

#### Option B (No-cost, recommended under your constraints): **avoid quarantine**
Gatekeeper checks are triggered when the app has the `com.apple.quarantine` attribute. :contentReference[oaicite:14]{index=14}

**B1. Preferred “install flow” to avoid quarantine prompts:**
- Provide a one-liner install path that uses `curl` + `tar` (CLI tools that typically don’t set quarantine). :contentReference[oaicite:15]{index=15}

Example (shape of the workflow; implement exact URL + checksum):
```bash
mkdir -p ~/Applications/Interlink
cd ~/Applications/Interlink
curl -L -o interlink-macos-arm64.tar.gz "<RELEASE_URL>"
# Verify SHA256 before extraction (strongly recommended)
shasum -a 256 interlink-macos-arm64.tar.gz
tar -xzf interlink-macos-arm64.tar.gz
open Interlink.app
````

**B2. If user downloaded via browser and gets blocked anyway:**
Apple documents “Open a Mac app from an unknown developer” (Control-click → Open) as the override path. ([Apple Support][1])

You can also remove quarantine recursively:

```bash
xattr -r -d com.apple.quarantine "/path/to/Interlink.app"
```

(`xattr` usage is documented in common references/manpage summaries. ([SS64][2]))

> Practical guidance: ship a tiny `install-macos.command` script inside the tarball that:
>
> 1. copies/moves the `.app` into `~/Applications` (user-writable)
> 2. removes quarantine xattrs on the `.app`
> 3. launches the app
>
> This keeps “no installer / no admin” while making first run as smooth as possible.

### 3.4 macOS packaging checklist (AI-implementable)

* [ ] Ensure the Wails app name and CFBundleExecutable remain consistent (Wails mac builds can be sensitive to naming/Info.plist coupling; test by double-click). ([GitHub][3])
* [ ] Create `dist/macos/interlink-macos-arm64.tar.gz` containing:

  * `Interlink.app/`
  * `install-macos.command` (optional helper)
  * `SHA256SUMS.txt`
* [ ] Provide docs that prefer “curl install” over browser download to reduce quarantine prompts.

---

## 4) Windows Server 2019 packaging: portable ZIP + Fixed WebView2 Runtime

### 4.1 Why this is necessary

Many Server 2019 environments won’t have WebView2 installed, and users may not be able to install it. Wails explicitly supports bundling the **Fixed Version WebView2 Runtime** and configuring `windows.Options.WebviewBrowserPath`. ([Wails][4])

### 4.2 Build + runtime bundle steps

1. Build the app:

```powershell
wails build -platform windows/amd64
```

2. Acquire the Fixed Version Runtime (CAB) and extract it.
   Wails docs note the downloaded runtime is compressed (`.cab`) and show using `expand` to extract. ([Wails][4])

Example:

```powershell
expand WebView2Runtime.cab -F:* .\webview2\
```

3. Package:

```
interlink-windows-amd64.zip
  Interlink.exe
  webview2\
    (extracted fixed runtime contents)
  README.txt
  SHA256SUMS.txt
```

4. Code change required: point Wails at the runtime folder.
   Wails Windows guide: set `WebviewBrowserPath` in `windows.Options`. ([Wails][4])
   The go-webview2 loader supports relative paths to the executable. ([Go Packages][5])

### 4.3 Windows warnings (no-cost reality)

Without paid code signing, “Unknown Publisher” / SmartScreen warnings may appear. ([SSLInsights][6])
Plan for:

* `README.txt` with “More info → Run anyway” guidance
* optional PowerShell helper to `Unblock-File` the extracted exe (not a guarantee, but reduces friction)

---

## 5) Linux RHEL8-like packaging: portable tarball of an AppDir (no AppImage required)

### 5.1 Why AppDir (instead of “just a binary + random libs”)

Wails on Linux relies on WebKitGTK. ([DeepWiki][7])
Minimal Rocky/Alma systems often lack GTK/WebKitGTK, and users may not have root to install them. So we must ship the dependent `.so` libraries + required GTK resources.

An **AppDir** is simply a structured directory for “app + bundled deps + resources.” `linuxdeploy` automates bundling dependencies into an AppDir, and explicitly supports creating the AppDir from scratch or filling an existing AppDir. ([AppImage Documentation][8])

For GTK apps, `linuxdeploy-plugin-gtk` bundles additional resources (including GLib schemas) and runs schema compilation, etc. ([GitHub][9])

### 5.2 Build prerequisites (build machine only)

On your build machine (Rocky/Alma 8), you *will* need the dev packages to compile and to collect dependencies—this is fine because the build machine is under your control.

### 5.3 Packaging flow (tarred AppDir)

1. Build:

```bash
wails build -platform linux/amd64
```

(If you hit WebKitGTK version tag issues, Wails documents using build tags for distros lacking certain WebKitGTK versions. ([Wails][10]))

2. Create AppDir using linuxdeploy.
   linuxdeploy bundles executables (`-e` / `--executable`), desktop file (`-d`), icon (`-i`) into the correct places and rewrites the AppDir to prefer bundled libraries. ([AppImage Documentation][8])

3. Run GTK plugin.
   Plugin usage from its README shows calling via linuxdeploy with `--plugin gtk`. ([GitHub][9])
   (You can omit `--output appimage` if you only want the AppDir and will ship a tarball.)

Illustrative commands (AI should adapt paths):

```bash
# Get tools (build machine)
wget -c "https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/master/linuxdeploy-plugin-gtk.sh"
wget -c "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage"
chmod +x linuxdeploy-x86_64.AppImage linuxdeploy-plugin-gtk.sh

# Create AppDir and bundle deps/resources
APPDIR="$PWD/Interlink.AppDir"
./linuxdeploy-x86_64.AppImage --appdir "$APPDIR" \
  -e "build/bin/interlink" \
  -d "packaging/interlink.desktop" \
  -i "packaging/interlink.png" \
  --plugin gtk
```

4. Ship as tarball (no AppImage for users):

```bash
tar -czf interlink-linux-rhel8-amd64.tar.gz Interlink.AppDir
```

### 5.4 Linux user run instructions

```bash
tar -xzf interlink-linux-rhel8-amd64.tar.gz
./Interlink.AppDir/AppRun
```

---

## 6) Cross-platform release hygiene (strongly recommended)

Because you’re avoiding paid signing, you should compensate with transparent integrity checks.

### 6.1 Always publish checksums

* Create `SHA256SUMS.txt` for every artifact
* Instruct users to verify the checksum before running

### 6.2 Test on truly minimal targets

* Windows Server 2019 VM without WebView2 installed
* Rocky/Alma 8 minimal + whatever GUI stack your users actually have
* macOS Apple Silicon with downloads performed via:

  * browser (expect quarantine prompts)
  * `curl` + `tar` (target “prompt-free”)

---

## 7) Summary: recommended “$0 spend” strategy

* **macOS:** Ship `.app` in a tarball; to avoid warnings, steer users to a CLI install flow (`curl` + `tar`) or provide a helper script that strips quarantine xattrs before first launch. Gatekeeper prompts are driven by quarantine + trust checks. ([Apple Support][11])
* **Windows:** Ship portable zip with fixed WebView2 runtime and set `WebviewBrowserPath`. Expect some SmartScreen/Unknown Publisher friction without paid signing. ([Wails][4])
* **Linux:** Ship a tarred AppDir built via linuxdeploy + gtk plugin so minimal systems don’t need root-installed GTK/WebKitGTK. ([AppImage Documentation][8])