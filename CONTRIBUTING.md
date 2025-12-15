# Contributing to Rescale Interlink

**Version**: 3.4.2
**Last Updated**: December 15, 2025

Thank you for your interest in contributing to Rescale Interlink!

For complete architecture details, see [ARCHITECTURE.md](ARCHITECTURE.md).
For comprehensive feature list, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

## Development Setup

### Prerequisites

- Go 1.24 or later (minimum required)
- macOS, Linux, or Windows development environment
- Git

### Getting Started

```bash
# Clone the repository (use your fork URL if contributing)
git clone https://github.com/rescale/rescale-int.git
cd rescale-int

# Install dependencies
go mod download

# Build
go build -o rescale-int ./cmd/rescale-int

# Run tests
go test ./...
```

## Build Requirements (CRITICAL)

**FIPS 140-3 Compliance is MANDATORY**

All production builds MUST be compiled with FIPS 140-3 support for FedRAMP compliance:

```bash
# Required build command (production)
GOFIPS140=latest go build -o rescale-int ./cmd/rescale-int

# Or use the Makefile (recommended - includes all necessary flags)
make build

# Development only (not for production releases)
RESCALE_ALLOW_NON_FIPS=true go build -o rescale-int ./cmd/rescale-int
```

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
├── cmd/rescale-int/    # Entry point
│   └── main.go         # Application bootstrap
├── internal/
│   ├── api/            # Rescale API client
│   ├── cli/            # CLI commands
│   ├── cloud/          # Cloud storage operations
│   │   ├── credentials/  # Credential management
│   │   ├── download/     # Download entry point
│   │   ├── providers/    # Provider implementations
│   │   │   ├── s3/         # S3 provider (upload, download, streaming)
│   │   │   └── azure/      # Azure provider (upload, download, streaming)
│   │   ├── state/        # Resume state management
│   │   ├── storage/      # Common interfaces
│   │   ├── transfer/     # Unified transfer orchestration
│   │   │   ├── downloader.go   # Download orchestrator
│   │   │   ├── uploader.go     # Upload orchestrator
│   │   │   └── streaming.go    # Streaming encryption
│   │   ├── upload/       # Upload entry point
│   │   └── interfaces.go # CloudTransfer interface
│   ├── config/         # Configuration management
│   ├── constants/      # Application constants (chunk sizes, etc.)
│   ├── core/           # Core engine
│   ├── crypto/         # Encryption (HKDF, AES-256-CBC)
│   ├── events/         # Event bus system
│   ├── gui/            # Fyne GUI components
│   │   ├── setup_tab.go        # Configuration UI
│   │   ├── jobs_tab.go         # Jobs management
│   │   ├── file_browser_tab.go # File browser
│   │   └── activity_tab.go     # Activity log
│   ├── http/           # HTTP client and retry logic
│   ├── pur/            # PUR pipeline integration
│   ├── trace/          # Debugging/tracing
│   └── transfer/       # Transfer handles & progress tracking
└── testdata/           # Test fixtures
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
- Release locks before calling widget refresh methods

### Fyne GUI

- Keep UI code in `internal/gui/`
- Don't call `table.Refresh()` while holding locks
- Use proper error dialogs for user feedback

## Debugging

The application includes instrumentation:

```bash
# Run with profiling enabled
./rescale-int

# Access profiler at http://localhost:6060
go tool pprof http://localhost:6060/debug/pprof/profile
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
