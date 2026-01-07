# Commit Whitelist Manifest
**Purpose:** Single source of truth for what can be committed to this repository.
**Audience:** Developers and Claude Code
**Last Updated:** 2026-01-05

## Philosophy
This is a PUBLIC repository. Only production code and public documentation belong here.
Internal development files (scripts, planning docs, logs) stay LOCAL only.

## ALLOWED: Source Code

| Path | Purpose | Added |
|------|---------|-------|
| `cmd/` | Application entry points | Initial |
| `internal/` | Core library code | Initial |
| `frontend/` | React/TypeScript GUI | Initial |
| `installer/` | Cross-platform packaging | v4.0.0 |
| `build/windows/icon.ico` | Windows application icon | v4.0.8 |
| `.github/` | CI/CD workflows and repo metadata | v4.0.8 |

## ALLOWED: Configuration

| File | Purpose | Added |
|------|---------|-------|
| `go.mod`, `go.sum` | Go dependencies | Initial |
| `Makefile` | Build automation | Initial |
| `wails.json` | Wails configuration | v4.0.0 |
| `main.go` | Wails GUI entry point | v4.0.8 |
| `.gitignore` | Git ignore rules | Initial |

## ALLOWED: Public Documentation

| File | Purpose | Added |
|------|---------|-------|
| `README.md` | Project overview | Initial |
| `LICENSE` | MIT license | Initial |
| `CLI_GUIDE.md` | Command reference | v3.0.0 |
| `ARCHITECTURE.md` | System design | v3.0.0 |
| `CONTRIBUTING.md` | Developer guide | v3.0.0 |
| `TESTING.md` | Test strategy | v3.0.0 |
| `RELEASE_NOTES.md` | Version history | Initial |
| `FEATURE_SUMMARY.md` | Public feature list | v3.0.0 |

## BLOCKED: Internal/Development (NEVER COMMIT)

| Path/Pattern | Reason | Added |
|--------------|--------|-------|
| `scripts/` | Release automation (internal) | Initial |
| `docs/` | Internal documentation | Initial |
| `testdata/` | Test fixtures (internal) | Initial |
| `old-reference/` | Historical archives | Initial |
| `_archive_*` | Archived code | Initial |
| `CLAUDE.md` | AI instructions | Initial |
| `GITHUB_READY.md` | Internal checklist | v4.0.0 |
| `DOCUMENTATION_SUMMARY.md` | Internal documentation index | v4.0.0 |
| `CHECKPOINT*` | Planning docs | v3.0.0 |
| `ROADMAP*` | Planning docs | v3.0.0 |
| `TODO_*` | Planning docs | v3.0.0 |
| `SECURITY_AUDIT*` | Internal audits | v4.0.0 |
| `Terminal_*.txt` | Session logs | v3.0.0 |
| `*_session_*.txt` | Session logs | v3.0.0 |
| `*.env` | Environment secrets | Initial |
| `.api_key` | API keys | Initial |
| `*token*.txt` | Token files | Initial |
| `*.md` (not whitelisted) | Catch-all for internal docs | Initial |
| `*.txt` (not whitelisted) | Catch-all for logs/notes | Initial |

## Whitelist Change Process

When NEW files/folders need to be committed:

1. **Detection:** Claude identifies file not in whitelist
2. **Analysis:** Determine if it's legitimate production code/docs
3. **Discussion:** Claude proposes addition with justification
4. **Approval:** User approves
5. **Update:** Add to this manifest + update hooks/gitignore
6. **Commit:** Commit the manifest update WITH the new file

### Questions to Ask:
- Is this production code or internal development?
- Will external users need this file?
- Does it contain sensitive information?
- Is it generated/temporary?

### Example: Adding a New Public Document

```
User: "I created SECURITY.md for security disclosures"
Claude: "I see SECURITY.md is not in the whitelist. This appears to be
        public-facing security documentation. Should I:
        A) Add it to the whitelist (under Public Documentation)
        B) Keep it local-only

        Recommendation: A - security disclosure docs are typically public"
User: "Yes, add to whitelist"
Claude: Updates this manifest, then commits both manifest and SECURITY.md
```

### Example: Blocking an Internal File

```
User: Creates "PERFORMANCE_RESEARCH.txt"
Claude: "I see PERFORMANCE_RESEARCH.txt which is not whitelisted.
        .txt files that aren't explicitly allowed are blocked.
        This appears to be internal research notes.
        Should this stay local-only? (recommended: yes)"
User: "Yes, local only"
Claude: Confirms .gitignore will handle it, never commits it
```
