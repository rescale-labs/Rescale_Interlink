# Contributing to Rescale Interlink

**Version**: 4.9.1
**Last Updated**: April 12, 2026

Thank you for your interest in contributing to Rescale Interlink!

For complete architecture details, see [ARCHITECTURE.md](ARCHITECTURE.md).
For comprehensive feature list, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

## Development Setup

### Prerequisites

- Go 1.24 or later (minimum required)
- Node.js 18+ (for GUI development)
- Wails v2 CLI (for GUI builds)
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

# Run tests
go test ./...
```

### GUI Development (Wails)

```bash
# Install Wails CLI (if not already installed)
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Install frontend dependencies
cd frontend && npm install && cd ..

# Development mode (hot reload)
wails dev

# Production build
CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -platform darwin/arm64
```

## Build Requirements (CRITICAL)

**FIPS 140-3 Compliance is MANDATORY**

All production builds MUST be compiled with FIPS 140-3 support for FedRAMP compliance:

```bash
# REQUIRED: Use the Makefile for all builds (enforces FIPS and correct output path)
make build                    # Build for current platform
make build-darwin-arm64       # Build for macOS ARM64
make build-all                # Build for all platforms

# Output goes to: bin/{VERSION}/{PLATFORM}/rescale-int
# Example: bin/v4.0.0/darwin-arm64/rescale-int

# Production GUI build
GOFIPS140=latest CGO_LDFLAGS="-framework UniformTypeIdentifiers" ~/go/bin/wails build -platform darwin/arm64

# Development only (not for production releases)
# Note: Output to bin/dev/ to avoid polluting project root
RESCALE_ALLOW_NON_FIPS=true go build -o bin/dev/rescale-int ./cmd/rescale-int
```

**IMPORTANT:** Never output binaries to the project root directory. The `bin/` directory is gitignored; the root is not.

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

# Check for issues
go vet ./...
```

## Testing

All new features should include appropriate tests:

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package tests
go test -v ./internal/events/
```

## Pull Request Process

1. **Fork the repository**
2. **Create a feature branch**: `git checkout -b feature/your-feature-name`
3. **Make your changes**:
   - Write clean, documented code
   - Add tests for new functionality
   - Update documentation as needed
4. **Test thoroughly**:
   - Run `go test ./...`
   - Run `go vet ./...`
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
├── cmd/
│   ├── rescale-int/           # CLI-only binary entry point
│   │   └── main.go
│   └── rescale-int-tray/      # Windows system tray companion
│       └── main.go
│
├── frontend/                  # Wails GUI (React/TypeScript)
│   ├── src/
│   │   ├── components/        # React components
│   │   │   ├── tabs/          # Tab components (Setup, SingleJob, PUR, etc.)
│   │   │   ├── widgets/       # Shared reusable widgets (JobsTable, StatsBar, etc.)
│   │   │   └── common/        # Common components (ErrorBoundary, etc.)
│   │   ├── stores/            # Zustand state stores (jobStore, runStore, etc.)
│   │   ├── types/             # TypeScript type definitions (jobs, run, events)
│   │   └── utils/             # Shared utilities (stageStats, formatDuration)
│   ├── package.json           # Node.js dependencies
│   └── wailsjs/               # Generated Wails bindings
│
├── internal/
│   ├── api/                   # Rescale API client (v3 + v2)
│   ├── cli/                   # CLI commands (Cobra)
│   │   └── compat/            # rescale-cli compatibility mode (24 files)
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
├── build/                     # Wails build assets (icons, manifests)
└── testdata/                  # Test fixtures
```

## Key Patterns

### Event System

Use the event bus for decoupled communication:

```go
// Publish an event
eventBus.PublishStateChange(jobName, stage, status, jobID, error, progress)

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

# Lint frontend code
cd frontend && npm run lint

# Build frontend only
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
