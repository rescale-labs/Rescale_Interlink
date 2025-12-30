# Rescale Interlink — Windows Auto-Downloader Service + System Tray Mode (Implementation Spec)

## Background / context

* Interlink was previously distributed as platform tar/zip archives; on Windows the release notes instruct users to extract a ZIP and run `rescale-int.exe
* Interlink config paths were recently standardized to `~/.config/rescale/` (including `config.csv`, plus a token file called `token`).
* Rescale’s REST API supports:

  * Listing jobs with query params like `job_status` and `state=completed`, and tag-based filtering via the `q` parameter (e.g. `#tagname`).
  * Reading job custom fields (included on “Get a Specific Job” and via a dedicated custom-fields endpoint).
  * Adding and reading tags on a job via `/api/v2/jobs/{job_id}/tags/`.
  * Listing job output files via `/api/v2/jobs/{job_id}/files/` (each includes a `downloadUrl`).

This feature set adds **Windows Service “auto-downloader” mode**, an **installer option** for service install/config checks, and a **system tray experience** for background operation visibility and control.

---

## 1) Goals

### 1.1 Primary goal (user story)

As a Simulation Engineer, submit jobs overnight and have “valuable + correct” completed job results automatically downloaded to a local workstation by morning.

### 1.2 In-scope

* A Windows Service that:

  * Runs continuously (auto-start on reboot).
  * Periodically scans each Windows user profile for Interlink auto-download configuration.
  * For each configured user, polls Rescale for completed jobs in a lookback window (default 7 days).
  * Downloads jobs that meet *tag + custom-field* criteria and are not already marked downloaded.
  * Adds a `autoDownloaded:true` tag to the job on success.
* A Windows installer that:

  * Installs Interlink for all users (per-machine).
  * Offers an “Install auto-downloader as a Windows Service” option.
  * Runs post-install connectivity + filesystem tests (platform reachability, download root read/write).
* A Windows “background mode” UX via **system tray icon**:

  * Shows service status and activity.
  * Provides quick actions (open UI, pause/resume auto-download, view logs, trigger scan).

### 1.3 Out-of-scope

* The “Job Correctness Agent” that decides correctness. Interlink only consumes its tag output.
* Designing solver-specific correctness logic.

---

## 2) Key assumptions & constraints

### 2.1 Assumptions (from requirements)

* Service runs with Administrator-level privileges.
* Machine is “always on”.
* Service can access target drives, including network drives (e.g., McLaren `A:\` mapping).
* Service has outbound HTTPS/443 access to:

  1. Rescale platform URL
  2. S3 and Azure Blob URLs (if Interlink’s downloader hits them)

### 2.2 Windows network drive caveat (important)

Mapped drive letters are typically tied to a user logon session; Windows services may not see the same mappings even if running under the same account. Recommended approaches are:

* Prefer UNC paths, or
* Explicitly map drives within the service context (e.g., using Windows networking APIs).

**Decision for this spec:** support both **UNC paths** and **drive letters**, but:

* Installer must validate the chosen path from the service context.
* If a drive letter fails, show a clear warning and recommend UNC.

---

## 3) User-facing behavior

### 3.1 Workstation Admin

1. Runs Interlink installer (signed).
2. Chooses options:

   * Install Interlink GUI + CLI
   * **(Optional)** Install and start “Interlink Auto-Downloader Service”
   * **(Optional but recommended)** Install “Tray companion” to run at user login (for status/controls)
3. Installer post-install checks:

   * Platform reachability test (HTTPS/TLS to chosen platform URL)
   * Prompt for “download root folder” (default download folder baseline) and run read/write test
   * Optional warning if path is a mapped drive letter and service cannot access it

### 3.2 Simulation Engineer

**Interlink setup (per user):**

* Launch Interlink (GUI or CLI) and configure:

  * `PlatformURL` (required)
  * `APIKey`/token (required)
  * Auto-download enabled checkbox (optional)
  * “Job correctness tag” (required; default `isCorrect:true`)
  * Default download folder (required)
  * Poll interval (optional; default 10 minutes)
  * Lookback window (optional; default 7 days)

**Job setup (not implemented by Interlink, but required for correct behavior):**

* Job has custom field **Auto Download** = `Enable`
* Optional custom field **Auto Download Path** set to per-job output destination folder

### 3.3 Rescale Workspace Admin (prereq)

Create custom fields in the workspace:

* `Auto Download` (select: `Enable` / `Disable`)
* `Auto Download Path` (text)

(Interlink will read these fields per job via the API.) 

---

## 4) System behavior (auto-downloader)

### 4.1 Core rules

For each user that enabled auto-download, the service will scan completed jobs in the last **N** days (default **7**). For each job, initiate download only if all are true:

1. Job is **completed** (Rescale job state/status)

   * Use list jobs endpoint filtering `state=completed` and/or `job_status=COMPLETED`. 
2. Job has custom field `Auto Download` == `Enable`
3. Job has correctness tag (default `isCorrect:true`)

   * Can pre-filter jobs by tag using the jobs list query `q=#tagname` semantics. 
4. Job does **not** have tag `autoDownloaded:true`
5. If `Auto Download Path` is set:

   * Download into that path
     Else:
   * Download into per-user “default download folder”
6. If target folder does not exist, create it.
7. On successful download, add tag `autoDownloaded:true` to the job via API. 

### 4.2 Poll schedule

* Default scan interval: **10 minutes**
* Configurable per user.
* Service should jitter polling per user slightly (e.g., ±30s) to avoid bursty API usage on multi-user machines.

### 4.3 Multi-user handling

* Service enumerates all local user profiles (e.g., `C:\Users\*`) and inspects each for an Interlink config file.
* Each user is processed independently:

  * Separate API credentials
  * Separate default folder
  * Separate state (last scan time, download-in-progress)
* Concurrency:

  * At most **1 active download per user** by default.
  * Global max concurrency configurable (default 2–3).

---

## 5) Configuration

### 5.1 Per-user config discovery

Service iterates all user profile directories and looks for:

`%USERPROFILE%\.config\rescale\apiconfig` (Windows)
(Interlink already standardizes config directories under `~/.config/rescale/` across platforms. ([GitHub][2]))

### 5.2 `apiconfig` format (INI)

Create/extend a dedicated INI file as the stable contract between GUI/CLI and the Windows Service.

Example:

```ini
[rescale]
platform_url = https://platform.rescale.com
api_key = <token-or-api-key>

[interlink.autoDownload]
enabled = true
correctness_tag = isCorrect:true
default_download_folder = A:\Rescale\Downloads
scan_interval_minutes = 10
lookback_days = 7
```

Notes:

* `correctness_tag` is **required** (default `isCorrect:true`)
* `default_download_folder` is **required**
* `api_key` is sensitive: never log it; ensure file ACLs are user-only readable.
* Keep compatibility with existing Interlink config artifacts (e.g., `token`, `config.csv`) by:

  * Continuing to support them as sources-of-truth in the GUI/CLI, but
  * Writing `apiconfig` as the service contract (and reading from it in the service)

### 5.3 Per-user auto-download state (service-managed)

To avoid re-downloading and reduce API calls, maintain a small state file per user:

`%USERPROFILE%\.config\rescale\autodownload_state.json`

Contents:

* `last_scan_time_utc`
* `last_success_time_utc`
* `recent_job_ids_seen` (bounded LRU, e.g. last 500)
* `failures` counters (bounded)

---

## 6) Rescale API usage (required endpoints)

### 6.1 List candidate jobs

Use:

`GET /api/v2/jobs/` with:

* `state=completed` and/or `job_status=COMPLETED` 
* `q=#isCorrect:true` (URL encode `#`) to pre-filter by correctness tag 
* `page_size` and pagination

Also filter client-side by `dateInserted` >= now - lookback_days.

### 6.2 Job details & custom fields

For each candidate job:

* `GET /api/v2/jobs/{job_id}/` and read `customFields` if present 
* If needed, fall back to `GET /api/v2/jobs/{job_id}/custom-fields/` 

### 6.3 Tags

* `GET /api/v2/jobs/{job_id}/tags/` to check existing tags 
* `POST /api/v2/jobs/{job_id}/tags/` with `{ "name": "autoDownloaded:true" }` after success 

### 6.4 Download job outputs

Preferred: reuse Interlink’s existing download pipeline.

If implementing via Rescale API:

* List output files: `GET /api/v2/jobs/{job_id}/files/`
* Each file includes `downloadUrl` pointing to `GET /api/v2/files/{file_id}/contents/`

(Interlink may already optimize downloads and handle storage backends; keep that logic centralized.)

---

## 7) Windows components & architecture

### 7.1 Proposed processes

1. **Windows Service**: `RescaleInterlinkService`

   * Headless polling + downloading engine
   * Runs as LocalSystem or a configurable admin account
   * Auto-start, automatic recovery on crash
2. **Tray companion**: `rescale-int-tray.exe`

   * Runs in user session at login
   * Shows system tray icon + menu
   * Talks to service via IPC for status/control
3. **Interlink GUI/CLI**: existing `rescale-int.exe`

   * Provides configuration UI/commands
   * Writes `apiconfig` for the service contract

### 7.2 System tray implementation

Interlink previouly used Fyne and now uses Wails; we will need to determine how to integrate the Wails-based Interlink into the system tray.

**Spec requirement:** tray shows status even when main GUI is closed; main window can be opened from tray.

### 7.3 Service implementation approach

We could use one of:

* `golang.org/x/sys/windows/svc` (native Windows service API) 
* or `github.com/kardianos/service` (simpler cross-platform service install/run wrapper)

Or something else based on your assessment

### 7.4 IPC between tray and service

Use a local IPC channel:

* **Named pipe** (recommended on Windows): `\\.\pipe\rescale-interlink`
* Messages: JSON request/response
* Auth: local machine only; optionally restrict pipe ACL to `Administrators` + `Authenticated Users` (read-only status for standard users; admin-only control actions)

IPC operations:

* `GetStatus` → { service_state, last_scan_time, active_downloads, last_error, version }
* `PauseUser(userSid)` / `ResumeUser(userSid)`
* `TriggerScan(userSid|all)`
* `OpenLogs(userSid|service)`
* `OpenGUI` (tray launches GUI locally; service doesn’t spawn UI)

---

## 8) Installer (Windows)

### 8.1 Packaging goals

* Provide a signed installer EXE/MSI that:

  * Installs binaries into `Program Files\Rescale\Interlink\`
  * Adds Start Menu shortcuts
  * Optionally installs the Windows Service and starts it
  * Optionally installs tray companion auto-start for all users

### 8.2 Install-time prompts / checks

1. Platform URL (default `https://platform.rescale.com`)
2. Default download root folder (path chooser)
3. Run checks:

   * HTTPS reachability to Platform URL (basic connect)
   * Read/write test:

     * create folder if missing
     * write a temp file + read it back + delete it
   * If path is mapped drive:

     * validate accessible from service context
     * if not, warn and recommend UNC, or require mapping setup

### 8.3 Service registration requirements

* Service name: `RescaleInterlinkService`
* Startup type: `Automatic (Delayed Start)`
* Recovery: restart on failure (e.g., 1st/2nd/subsequent failures)
* Logging:

  * Write to a rotating log file under `%ProgramData%\Rescale\Interlink\logs\service.log`
  * Optionally write to Windows Event Log

### 8.4 Tray auto-start

Create a per-machine startup mechanism:

* Preferred: Scheduled Task “At log on” for any user
* Alternative: HKLM Run key (less enterprise-friendly)

Tray app should:

* Start silently (no window)
* Only show tray icon
* Offer “Open Interlink” and “Quit tray” actions

---

## 9) UX: tray menu & status

Tray tooltip should show:

* “Interlink Auto-Downloader: Running”
* “Last scan: <time>”
* “Downloads in progress: N”
* “Last error: <summary>” (if any)

Menu items:

* Open Interlink (GUI)
* Status (opens a small status window)
* Pause auto-download (for current user)
* Resume auto-download
* Trigger scan now
* View logs
* Service controls (admin-only): Restart service
* Quit tray

Notifications (optional but recommended):

* “Downloaded job <name> to <path>”
* “Auto-download failed for job <name>: <error>”

---

## 10) Edge cases & failure handling

* **Partially completed downloads**: download to a temp folder (`.partial`) and atomically rename on success.
* **Tagging failure after download**:

  * Record locally that download succeeded
  * Retry tagging on next scan (idempotent)
* **Directory creation failure**:

  * Record error, skip job, retry next scan
* **Rate limiting / transient HTTP errors**:

  * Exponential backoff per user
  * Cap retries per scan iteration
* **Large output / many files**:

  * Ensure pagination when listing output files
  * Throttle concurrency
* **Drive letter path not available to service**:

  * Warn in installer tests
  * Show actionable error in tray status

---

## 11) Implementation plan (suggested work breakdown)

1. **Config contract**

   * Add `apiconfig` read/write to CLI/GUI.
   * Add validation + migration helper (if existing token/config.csv present).
2. **Download eligibility engine**

   * Implement job query + filtering:

     * completed
     * has correctness tag
     * has custom field Auto Download = Enable
     * lacks autoDownloaded tag
   * Implement destination resolution rules and folder creation.

3. **Windows Service**

   * Service lifecycle (start/stop)
   * Scheduler loop (poll interval)
   * Per-user enumeration + processing
   * Logging + state file

4. **Job download**

   * Reuse existing Interlink downloader.
   * Ensure safe atomic completion semantics.

5. **Job tagging**

   * Add `autoDownloaded:true` tag via API.

6. **Tray companion**

   * Systray icon + menu
   * IPC client
   * Status UI + notifications

7. **Installer**

   * Install binaries
   * Register service (optional)
   * Create tray auto-start (optional)
   * Post-install tests

8. **QA**

   * Unit tests for config parsing and job eligibility
   * Integration tests with mocked Rescale API endpoints
   * Manual Windows test matrix (local path, UNC path, mapped drive, multi-user)

---

## 12) Acceptance criteria (testable)

1. **Service install + persistence**

* When installed with service option, service exists, is running, and starts automatically after reboot.

2. **Per-user opt-in**

* If user has no `apiconfig` or `enabled=false`, service does nothing for that user.

3. **Correct job filtering**

* For a user with `enabled=true`, service downloads only jobs that:

  * are completed 
  * have `isCorrect:true` tag (or configured tag) 
  * have custom field `Auto Download=Enable` 
  * do not already have `autoDownloaded:true`

4. **Correct destination**

* If job custom field `Auto Download Path` set → download there
* Else → download to user’s default download folder
* Missing folders are created.

5. **Tagging after download**

* On successful download, job receives `autoDownloaded:true` tag.

6. **Tray visibility**

* Tray icon appears for logged-in users (when tray option enabled).
* Tray shows last scan time and provides “Trigger scan now”.
* Tray can open the Interlink GUI.

7. **Multi-user**

* If two Windows users on the machine enabled auto-download, service processes both independently and correctly.

---

## 13) Reference implementation choices (explicit)

* **Service:** implement with `kardianos/service` (preferred) or native `golang.org/x/sys/windows/svc`.
