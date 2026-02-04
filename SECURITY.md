# Security Documentation - Rescale Interlink

**Version:** 4.5.7
**Last Updated:** 2026-02-03

## Overview

Rescale Interlink is designed with security as a core priority, especially for FedRAMP compliance. This document outlines the security architecture, FIPS 140-3 compliance requirements, and important security considerations for deployment.

---

## FIPS 140-3 Compliance

Rescale Interlink REQUIRES FIPS 140-3 compliant builds for production use. This is mandatory for FedRAMP environments.

### Build Requirements

All production builds must be compiled with:

```bash
GOFIPS140=latest wails build
# or
make build
```

### Runtime Verification

The application verifies FIPS compliance at startup. Non-FIPS builds will:
- Display a critical error message
- Exit with code 2

For development only, bypass with:
```bash
RESCALE_ALLOW_NON_FIPS=true ./rescale-int
```

**Warning:** Never use non-FIPS builds in production or with FedRAMP platforms.

---

## Proxy Authentication

### Supported Modes

| Mode | Description | FIPS Compliant |
|------|-------------|----------------|
| `no-proxy` | Direct connection | Yes |
| `system` | Use system proxy settings | Depends on system |
| `basic` | Basic authentication over TLS | Yes |
| `ntlm` | NTLM authentication | **No** |

### NTLM and FIPS Compliance

NTLM proxy authentication (`proxy_mode=ntlm`) uses the Azure/go-ntlmssp library which requires MD4 and MD5 hashing algorithms. These algorithms are **not FIPS 140-3 approved**.

#### For FedRAMP Environments (`rescale-gov.com`)

- **NTLM proxy mode is automatically disabled** in the GUI when a FedRAMP platform is selected
- If NTLM is configured when switching to a FedRAMP platform, it automatically switches to `basic`
- A warning is displayed explaining the restriction

#### For Commercial Rescale Platforms

- NTLM proxy mode is available and supported
- The FIPS boundary is the Rescale API communication, not the proxy
- Use NTLM if your corporate proxy requires it

### Recommendations

1. **Always prefer `basic` authentication over TLS** for maximum compatibility
2. If your proxy requires NTLM and you're targeting FedRAMP:
   - Contact your IT team for proxy alternatives
   - Consider using a FIPS-compliant proxy gateway
3. The startup warning will alert you if NTLM is configured in a FIPS build

---

## Log Security

### Directory Permissions

All log directories are created with `0700` permissions (Unix) to restrict access to the owner only:

| Platform | Log Location | Permissions |
|----------|-------------|-------------|
| Unix | `~/.config/rescale/logs/` | `drwx------` (0700) |
| Windows | `%LOCALAPPDATA%\Rescale\Interlink\logs\` | Owner-only via ACL |

### Log Contents

Logs may contain:
- Operation timestamps
- Job and file identifiers
- Error messages
- Transfer progress

Logs do **not** contain:
- API keys (full or partial)
- Proxy passwords
- File contents
- Encryption keys

### Best Practices

1. Regularly rotate and archive logs
2. Do not share log files without reviewing contents
3. Use the built-in log rotation (keeps 5 backups, 30-day retention)

---

## API Key Security

### Storage

API keys should be stored securely:

| Method | Recommended | Notes |
|--------|-------------|-------|
| Token file (`~/.config/rescale/token`) | Yes | 0600 permissions |
| Environment variable (`RESCALE_API_KEY`) | Yes | Cleared after session |
| Config file (`config.csv`) | **No** | Keys are ignored from config.csv |

### Transmission

- API keys are transmitted only over HTTPS
- Keys are never logged (not even partially)
- The GUI allows viewing the key (toggle) but never exposes it externally

---

## IPC Security (Windows)

### Authorization Model

The Windows daemon uses named pipes for IPC with a two-tier security model:

1. **Connection Level**: Any authenticated user can connect
2. **Operation Level**: Modify operations require owner authorization

### Modify Operations (Protected)

These require the caller's SID to match the daemon owner:
- `PauseUser`
- `ResumeUser`
- `TriggerScan`
- `Shutdown`

### Read Operations (Open)

These are available to any authenticated user:
- `GetStatus`
- `GetUserList`
- `GetRecentLogs`
- `OpenLogs`

### Fail-Closed Authorization

If the daemon cannot capture the owner SID at startup, all modify operations are denied. This prevents potential authorization bypass.

---

## Windows Service Mode

When running as a Windows Service:

1. **User isolation**: Each user's daemon instance is scoped to their profile
2. **Elevated controls**: Starting/stopping the service requires UAC approval
3. **Per-user state**: Downloads, pauses, and scans are user-specific

---

## Encryption

### File Encryption

Interlink uses mandatory AES-256 encryption for all file transfers:
- AES-256-GCM for authenticated encryption
- Keys derived using HKDF with SHA-256
- Random IVs for each encryption operation

### TLS

All API communication uses TLS 1.2+ with FIPS-approved cipher suites when FIPS mode is active.

---

## Reporting Security Issues

If you discover a security vulnerability, please report it to:
- Email: security@rescale.com
- Include "Interlink Security" in the subject line
- Provide steps to reproduce if possible

Do not disclose security issues publicly until a fix is available.

---

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 4.5.7 | 2026-02-03 | Auto-download settings auto-save fix, debounced config save |
| 4.5.1 | 2026-01-28 | Log permissions hardened (0700), NTLM/FIPS safeguards, fail-closed IPC auth |
| 4.4.2 | 2025-12-XX | Centralized log directory |
| 4.0.0 | 2025-XX-XX | Initial FIPS 140-3 compliance |
