# Contributing to Rescale Interlink

**Version**: 4.9.9
**Last Updated**: August 25, 2026

Thank you for your interest in contributing to Rescale Interlink!

For complete architecture details, see [ARCHITECTURE.md](ARCHITECTURE.md).
For comprehensive feature list, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

## Development Setup

### Prerequisites

- Go 1.26.7 (the version in `go.mod`, and what CI pins)
- Node.js 20 (CI pins 20; the frontend uses Vite 6 and Vitest 4)
- Wails v2 CLI v2.12.0 (matching the `github.com/wailsapp/wails/v2` require in `go.mod`)
- macOS, Linux, or Windows development environment
- Git

### Getting Started

```bash
# Clone the repository (use your fork URL if contributing)
git clone https://github.com/rescale-labs/Rescale_Interlink.git
cd Rescale_Interlink

# Install Go dependencies
go mod download

# Build CLI only (use Makefile for proper output location)
make build-darwin-arm64  # or make build for current platform

# Run tests the way CI does (FIPS module + fips build tag)
make test
```

### GUI Development (Wails)

```bash
# Install Wails CLI (if not already installed)
go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
# go install puts it in $(go env GOPATH)/bin (usually ~/go/bin). Put that on your
# PATH, or invoke the CLI as ~/go/bin/wails in the commands below.

# Install frontend dependencies (this is what wails.json's frontend:install runs)
cd frontend && npm ci && cd ..

# Development mode (hot reload)
wails dev

# Production build (macOS). GOFIPS140=certified is what the startup FIPS check tests —
# a GUI built without it exits 2. -tags fips selects the FIPS-only proxy/NTLM paths,
# so production builds need both.
GOFIPS140=certified CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -tags fips -platform darwin/arm64
```

Use `npm ci` rather than `npm install`: it installs exactly what `package-lock.json`
pins, which is what `wails build` and CI do. `npm install` can silently move
dependencies and produce a lockfile diff you did not intend.

## Build Requirements (CRITICAL)

**FIPS 140-3 Compliance is MANDATORY**

All production builds MUST be compiled with FIPS 140-3 support for FedRAMP compliance:

```bash
# REQUIRED: Use the Makefile for all builds (enforces FIPS and correct output path)
make build                    # Build for current platform
make build-darwin-arm64       # Build for macOS ARM64
make build-all                # Build for all platforms

# Output goes to: bin/{VERSION}/{PLATFORM}/rescale-int
# Example: bin/v4.9.9/darwin-arm64/rescale-int

# Production GUI build
GOFIPS140=certified CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -tags fips -platform darwin/arm64

# Development only (not for production releases). RESCALE_ALLOW_NON_FIPS is read at
# run time, not build time, so it belongs on the run rather than on the build.
# Note: Output to bin/dev/ to avoid polluting project root
go build -o bin/dev/rescale-int ./cmd/rescale-int
RESCALE_ALLOW_NON_FIPS=true ./bin/dev/rescale-int --version
```

**IMPORTANT:** Never output binaries to the project root directory. The `bin/` directory is gitignored; the root is not.

`GOFIPS140=certified` selects the latest Go Cryptographic Module version that holds a
CMVP validation certificate, which is the audit-grade choice for FedRAMP. Do not
substitute `latest`, which tracks the toolchain-bundled module and may not yet be
CMVP-validated.

Non-FIPS builds will refuse to run (exit code 2) unless `RESCALE_ALLOW_NON_FIPS=true` is set. This environment variable is for development purposes only and must not be used in production.

See [Go FIPS 140-3 Documentation](https://go.dev/doc/security/fips140) for details.

## Code Style

- Follow standard Go conventions and idioms
- Run `gofmt` before committing
- Run `go vet` to catch common mistakes
- Add comments for exported functions and types

### Formatting

```bash
# Format all code
gofmt -w .

# Check for issues (same invocation CI runs)
GOFIPS140=certified go vet -tags fips ./...
```

## Testing

All new features should include appropriate tests:

```bash
# Run the whole Go suite the way CI does
make test

# Run with coverage
make test-coverage

# Run specific package tests
go test -v ./internal/events/

# Frontend suite and lint
cd frontend && npm run test:run && npm run lint
```

`make test` is `GOFIPS140=certified go test -tags fips -v ./...`. A bare `go test ./...`
skips the files behind the `fips` build tag, so use the make target before opening a PR.

Go tests must not read from a checked-in fixture directory: `testdata/` is gitignored
and blocked by the pre-commit hook. Inline fixture content in the test file and write it
to `t.TempDir()`, which is what the config tests do.

See [TESTING.md](TESTING.md) for the full test guide.

## Pull Request Process

1. **Fork the repository**
2. **Create a feature branch**: `git checkout -b feature/your-feature-name`
3. **Make your changes**:
   - Write clean, documented code
   - Add tests for new functionality
   - Update documentation as needed
4. **Test thoroughly**:
   - Run `make test`
   - Run `GOFIPS140=certified go vet -tags fips ./...`
   - Run `npm run test:run` and `npm run lint` in `frontend/` if you touched the frontend
   - Test the GUI manually if UI changes
5. **Commit with clear messages**:
   ```
   feat: Add new feature X

   - Implemented Y
   - Updated Z
   - Fixes #123
   ```
6. **Push to your fork**: `git push origin feature/your-feature-name`
7. **Create a Pull Request**:
   - Provide clear description of changes
   - Reference any related issues
   - Include screenshots for UI changes

## Commit Message Guidelines

Format: `type: subject`

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation only
- `style`: Formatting, missing semicolons, etc.
- `refactor`: Code restructuring
- `test`: Adding tests
- `chore`: Maintenance

## Architecture Overview

```
rescale-int/
├── main.go                    # GUI+CLI binary entry point (rescale-int-gui)
├── cmd/
│   ├── rescale-int/           # CLI-only binary entry point
│   │   └── main.go
│   └── rescale-int-tray/      # Windows system tray companion
│       └── main.go
│
├── frontend/                  # Wails GUI (React/TypeScript)
│   ├── src/
│   │   ├── components/        # React components
│   │   │   ├── tabs/          # Tab components (Setup, SingleJob, PUR, JobStatus,
│   │   │   │                  #   FileBrowser, Transfers, Activity)
│   │   │   ├── widgets/       # Shared reusable widgets (JobsTable, StatsBar, etc.)
│   │   │   └── common/        # Common components (ErrorBoundary, etc.)
│   │   ├── stores/            # Zustand state stores (jobStore, runStore, etc.)
│   │   ├── lib/               # Shared frontend helpers
│   │   ├── test/              # Vitest setup
│   │   ├── types/             # TypeScript type definitions (jobs, run, events)
│   │   └── utils/             # Shared utilities (stageStats, formatDuration)
│   ├── package.json           # Node.js dependencies
│   └── wailsjs/               # Generated Wails bindings
│
├── internal/
│   ├── api/                   # Rescale API client (v3 + v2)
│   ├── cli/                   # CLI commands (Cobra)
│   │   └── compat/            # rescale-cli compatibility mode
│   ├── cloud/                 # Cloud storage operations
│   │   ├── credentials/       # Credential management + warming
│   │   ├── download/          # Download entry point
│   │   ├── providers/         # Provider implementations
│   │   │   ├── s3/            # S3 provider
│   │   │   └── azure/         # Azure provider
│   │   ├── state/             # Resume state management
│   │   ├── storage/           # Common interfaces and errors
│   │   ├── transfer/          # Unified transfer orchestration
│   │   └── upload/            # Upload entry point
│   ├── config/                # Configuration, CSV parsing, API key resolution
│   ├── constants/             # Application-wide constants
│   ├── core/                  # Core engine (job pipeline orchestration)
│   ├── crypto/                # Encryption (AES-256-CBC, HKDF, streaming)
│   ├── daemon/                # Auto-download daemon (background service)
│   ├── diskspace/             # Cross-platform disk space checking
│   ├── elevation/             # Windows UAC / Unix privilege elevation
│   ├── events/                # Event bus system (pub/sub + ring buffer)
│   ├── fips/                  # FIPS 140-3 init
│   ├── http/                  # HTTP client, proxy, and retry logic
│   ├── ipc/                   # Cross-process IPC (daemon ↔ GUI)
│   ├── localfs/               # Local filesystem browser (WalkStream)
│   ├── logging/               # Logger and TeeWriter (log → EventBus)
│   ├── mesa/                  # Mesa/OpenGL setup (Windows/Linux GPU)
│   ├── mesainit/              # Mesa early initialization
│   ├── models/                # Data models (jobs, files, credentials)
│   ├── pathutil/              # Path resolution utilities
│   ├── platform/              # Cross-platform sleep prevention
│   ├── progress/              # Progress bar UI (mpb wrapper)
│   ├── pur/                   # PUR (Parallel Upload and Run)
│   │   ├── filescan/          # File scanning
│   │   ├── parser/            # SGE script parsing
│   │   ├── pattern/           # Pattern detection for batch jobs
│   │   ├── pipeline/          # Pipeline orchestration
│   │   ├── state/             # PUR state management
│   │   └── validation/        # Core type validation
│   ├── ratelimit/             # Token bucket rate limiting
│   │   └── coordinator/       # Cross-process rate limit coordinator
│   ├── reporting/             # Error reporting (classify → redact → report)
│   ├── resources/             # Resource management (threads, memory)
│   ├── service/               # Windows service mode (multi-user daemon)
│   ├── services/              # GUI-agnostic services (TransferService, FileService)
│   ├── transfer/              # Transfer coordination and batch abstraction
│   │   ├── folder/            # Folder creation and orchestration
│   │   └── scan/              # Remote folder scanning
│   ├── util/                  # General utilities
│   │   ├── analysis/          # Analysis utilities
│   │   ├── buffers/           # Buffer pooling
│   │   ├── filter/            # File filtering
│   │   ├── glob/              # Glob pattern matching
│   │   ├── multipart/         # Multipart upload and scan utilities
│   │   ├── paths/             # Path collision detection
│   │   ├── sanitize/          # String sanitization
│   │   ├── tags/              # File tag utilities
│   │   └── tar/               # TAR archive creation
│   ├── validation/            # Path validation
│   ├── version/               # Version constant
│   ├── wailsapp/              # Wails v2 Go bindings
│   └── watch/                 # Job watch engine (polling + download)
│
├── build/                     # Wails build assets (icons, manifests), plus
│                              # build/linux/ AppImage WebKit bundling + verify scripts
├── installer/                 # Windows MSI installer sources
├── packaging/                 # Desktop entry, icon, macOS install helper
└── .github/workflows/         # release.yml — the tagged release pipeline
```

## Key Patterns

### Event System

Use the event bus for decoupled communication:

```go
// Publish an event
eventBus.PublishStateChange(jobName, oldStatus, newStatus, stage, jobID, errMsg)

// Subscribe to events
ch := eventBus.Subscribe(events.EventStateChange)
```

### Thread Safety

- UI updates must be thread-safe
- Use mutexes appropriately but avoid deadlocks
- In Wails, use the event bridge to communicate with frontend
- Release locks before calling widget refresh methods

### Wails GUI

- Go bindings in `internal/wailsapp/`
- Frontend React code in `frontend/src/`
- State management via Zustand stores
- Event bridge connects Go EventBus → Wails events → React stores
- Build with: `wails build -platform <target>`

### Frontend Development

```bash
# Start development server with hot reload
wails dev

# Lint frontend code (CI runs this with --max-warnings 0, so warnings fail)
cd frontend && npm run lint

# Run the vitest suite once
cd frontend && npm run test:run

# Build frontend only (runs tsc, then vite build)
cd frontend && npm run build
```

## Debugging

```bash
# Run any command with verbose logging
rescale-int --verbose files list

# Logs are written to:
#   macOS/Linux: ~/.config/rescale/logs/
#   Windows:     %LOCALAPPDATA%\Rescale\Interlink\logs\

# Test profiling for specific packages
go test -cpuprofile=cpu.prof -memprofile=mem.prof ./internal/transfer/
go tool pprof cpu.prof
```

## Documentation

Update documentation when:
- Adding new features
- Changing behavior
- Fixing significant bugs
- Updating dependencies

## Questions?

- Check existing issues
- Review the README.md
- Contact the maintainers

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
