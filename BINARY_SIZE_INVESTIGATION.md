# Binary Size Investigation - December 3, 2025

## Summary

**Question**: Why do binary sizes vary significantly across versions (34-49 MB)?

## Root Cause: Missing `-s -w` Linker Flags

The binary size difference is caused by whether the **`-s -w`** linker flags were included during build:

| Version | Size | ldflags | FIPS | Notes |
|---------|------|---------|------|-------|
| v2.6.0 | 34M | `-s -w` | NO | Stripped, no FIPS |
| v3.1.0 | 35M | `-s -w -X main.Version=... -X main.BuildTime=...` | YES | **Properly stripped** |
| v3.0.1 | 48M | *(missing)* | YES | **Not stripped** |
| v3.2.0 | 49M | `-X main.Version=... -X main.BuildTime=...` | YES | **Missing -s -w!** |

## What `-s -w` Does

- **`-s`**: Omit the symbol table and debug information
- **`-w`**: Omit the DWARF symbol table

These flags reduce binary size by ~14MB (from 48-49MB to 34-35MB).

## Why This Happened

The **Makefile** correctly includes `-s -w`:
```makefile
LDFLAGS := -ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"
```

However, v3.2.0 was built using a **direct `go build` command** that didn't include `-s -w`:
```
-ldflags="-X main.Version=v3.2.0 -X main.BuildTime=2025-12-03T04:35:48Z"
```

## Resolution

**No code changes needed.** Simply rebuild using the Makefile:

```bash
make build-darwin-arm64
```

This will produce a ~35MB binary with proper stripping.

## Verification

After rebuilding, verify with:
```bash
go version -m bin/v3.2.0/darwin-arm64/rescale-int | grep ldflags
# Should show: -ldflags="-s -w -X main.Version=v3.2.0 ..."
```

## Action Items

1. Rebuild v3.2.0 binary using `make build-darwin-arm64`
2. Verify the new binary is ~35MB (not 49MB)
3. Verify ldflags include `-s -w`

---

*Investigation completed: December 3, 2025*
