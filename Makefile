# Makefile for Rescale Interlink
# Build and package cross-platform FIPS 140-3 compliant binaries

# Variables
VERSION := v4.0.7
BINARY_NAME := rescale-int
BUILD_TIME := $(shell date +%Y-%m-%d)
LDFLAGS := -ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

# FIPS 140-3 compliance: Use Go's native FIPS crypto module
# See: https://go.dev/doc/security/fips140
GOFIPS := GOFIPS140=latest

# Suppress macOS linker warning about duplicate libraries
CGO_LDFLAGS_MACOS := CGO_LDFLAGS="-Wl,-no_warn_duplicate_libraries"

# Directories
BIN_DIR := bin/$(VERSION)
DARWIN_ARM64_DIR := $(BIN_DIR)/darwin-arm64
DARWIN_AMD64_DIR := $(BIN_DIR)/darwin-amd64
LINUX_AMD64_DIR := $(BIN_DIR)/linux-amd64
WINDOWS_AMD64_DIR := $(BIN_DIR)/windows-amd64
WINDOWS_AMD64_MESA_DIR := $(BIN_DIR)/windows-amd64-mesa

# Default target
.PHONY: all
all: build

# Build for current platform (FIPS 140-3 compliant)
.PHONY: build
build:
	@echo "Building FIPS 140-3 compliant binary for current platform..."
	@$(CGO_LDFLAGS_MACOS) $(GOFIPS) go build $(LDFLAGS) -o $(BINARY_NAME) ./cmd/rescale-int
	@echo "✅ Built: $(BINARY_NAME) [FIPS 140-3]"

# Build macOS Apple Silicon binary (FIPS 140-3 compliant)
.PHONY: build-darwin-arm64
build-darwin-arm64:
	@echo "Building macOS Apple Silicon binary [FIPS 140-3]..."
	@mkdir -p $(DARWIN_ARM64_DIR)
	@$(CGO_LDFLAGS_MACOS) $(GOFIPS) GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DARWIN_ARM64_DIR)/$(BINARY_NAME) ./cmd/rescale-int
	@echo "✅ Built: $(DARWIN_ARM64_DIR)/$(BINARY_NAME) [FIPS 140-3]"

# Build macOS Intel binary (FIPS 140-3 compliant)
.PHONY: build-darwin-amd64
build-darwin-amd64:
	@echo "Building macOS Intel binary [FIPS 140-3]..."
	@mkdir -p $(DARWIN_AMD64_DIR)
	@$(CGO_LDFLAGS_MACOS) $(GOFIPS) GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(DARWIN_AMD64_DIR)/$(BINARY_NAME) ./cmd/rescale-int
	@echo "✅ Built: $(DARWIN_AMD64_DIR)/$(BINARY_NAME) [FIPS 140-3]"

# Build Linux binary (FIPS 140-3 compliant)
.PHONY: build-linux-amd64
build-linux-amd64:
	@echo "Building Linux AMD64 binary [FIPS 140-3]..."
	@mkdir -p $(LINUX_AMD64_DIR)
	@$(GOFIPS) GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(LINUX_AMD64_DIR)/$(BINARY_NAME) ./cmd/rescale-int
	@echo "✅ Built: $(LINUX_AMD64_DIR)/$(BINARY_NAME) [FIPS 140-3]"

# Build Windows binary - standard (smaller, requires GPU)
.PHONY: build-windows-amd64
build-windows-amd64:
	@echo "Building Windows AMD64 binary [FIPS 140-3] (standard, no Mesa)..."
	@mkdir -p $(WINDOWS_AMD64_DIR)
	@$(GOFIPS) GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(WINDOWS_AMD64_DIR)/$(BINARY_NAME).exe ./cmd/rescale-int
	@echo "✅ Built: $(WINDOWS_AMD64_DIR)/$(BINARY_NAME).exe [FIPS 140-3] (requires GPU)"

# Build Windows binary with Mesa (larger, software rendering for VMs/RDP)
# Also copies manifest and .local file for DLL redirection
.PHONY: build-windows-amd64-mesa
build-windows-amd64-mesa:
	@echo "Building Windows AMD64 binary [FIPS 140-3] (with Mesa software rendering)..."
	@mkdir -p $(WINDOWS_AMD64_MESA_DIR)
	@$(GOFIPS) GOOS=windows GOARCH=amd64 go build -tags mesa $(LDFLAGS) -o $(WINDOWS_AMD64_MESA_DIR)/$(BINARY_NAME).exe ./cmd/rescale-int
	@echo "Copying manifest and .local file for DLL redirection..."
	@cp cmd/rescale-int/rescale-int.manifest $(WINDOWS_AMD64_MESA_DIR)/$(BINARY_NAME).exe.manifest 2>/dev/null || true
	@cp cmd/rescale-int/rescale-int.exe.local $(WINDOWS_AMD64_MESA_DIR)/$(BINARY_NAME).exe.local 2>/dev/null || true
	@echo "✅ Built: $(WINDOWS_AMD64_MESA_DIR)/$(BINARY_NAME).exe [FIPS 140-3] (Mesa software rendering)"

# Build all Windows variants
.PHONY: build-windows-all
build-windows-all: build-windows-amd64 build-windows-amd64-mesa
	@echo ""
	@echo "✅ All Windows binaries built:"
	@echo "   - Standard (GPU): $(WINDOWS_AMD64_DIR)/$(BINARY_NAME).exe"
	@echo "   - Mesa (VMs/RDP): $(WINDOWS_AMD64_MESA_DIR)/$(BINARY_NAME).exe"

# Build all platform binaries
.PHONY: build-all
build-all: build-darwin-arm64 build-darwin-amd64 build-linux-amd64 build-windows-all
	@echo ""
	@echo "✅ All platform binaries built successfully!"
	@echo "   - macOS Apple Silicon: $(DARWIN_ARM64_DIR)/$(BINARY_NAME)"
	@echo "   - macOS Intel:         $(DARWIN_AMD64_DIR)/$(BINARY_NAME)"
	@echo "   - Linux AMD64:         $(LINUX_AMD64_DIR)/$(BINARY_NAME)"
	@echo "   - Windows AMD64:       $(WINDOWS_AMD64_DIR)/$(BINARY_NAME).exe (standard)"
	@echo "   - Windows AMD64 Mesa:  $(WINDOWS_AMD64_MESA_DIR)/$(BINARY_NAME).exe (software rendering)"

# Package binaries for GitHub releases
.PHONY: package
package:
	@echo "Packaging binaries for GitHub releases..."
	@mkdir -p dist
	@cd $(DARWIN_ARM64_DIR) && tar -czf ../../../dist/$(BINARY_NAME)-$(VERSION)-darwin-arm64.tar.gz $(BINARY_NAME)
	@cd $(DARWIN_AMD64_DIR) && tar -czf ../../../dist/$(BINARY_NAME)-$(VERSION)-darwin-amd64.tar.gz $(BINARY_NAME)
	@cd $(LINUX_AMD64_DIR) && tar -czf ../../../dist/$(BINARY_NAME)-$(VERSION)-linux-amd64.tar.gz $(BINARY_NAME)
	@cd $(WINDOWS_AMD64_DIR) && zip -q ../../../dist/$(BINARY_NAME)-$(VERSION)-windows-amd64.zip $(BINARY_NAME).exe
	@cd $(WINDOWS_AMD64_MESA_DIR) && zip -q ../../../dist/$(BINARY_NAME)-$(VERSION)-windows-amd64-mesa.zip $(BINARY_NAME).exe
	@echo ""
	@echo "✅ Release packages created in dist/:"
	@ls -lh dist/$(BINARY_NAME)-$(VERSION)-*

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	@rm -f $(BINARY_NAME)
	@rm -rf dist/
	@echo "✅ Cleaned!"

# Clean specific version binaries
.PHONY: clean-version
clean-version:
	@echo "Cleaning $(VERSION) binaries..."
	@rm -rf $(BIN_DIR)
	@echo "✅ Removed $(BIN_DIR)"

# Run tests (FIPS 140-3 mode)
.PHONY: test
test:
	@echo "Running tests [FIPS 140-3]..."
	@$(GOFIPS) go test -v ./...

# Run tests with coverage (FIPS 140-3 mode)
.PHONY: test-coverage
test-coverage:
	@echo "Running tests with coverage [FIPS 140-3]..."
	@$(GOFIPS) go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report: coverage.html"

# Format code
.PHONY: fmt
fmt:
	@echo "Formatting code..."
	@go fmt ./...
	@echo "✅ Code formatted"

# Lint code
.PHONY: lint
lint:
	@echo "Linting code..."
	@golangci-lint run ./...
	@echo "✅ Linting complete"

# Install locally (copy to bin/$(VERSION)/darwin-arm64/ for current macOS)
.PHONY: install
install: build-darwin-arm64
	@echo "Binary installed to: $(DARWIN_ARM64_DIR)/$(BINARY_NAME)"
	@echo "Add to PATH: export PATH=\"\$$PATH:$(shell pwd)/$(DARWIN_ARM64_DIR)\""

# Show version
.PHONY: version
version:
	@echo "Version: $(VERSION)"
	@echo "Build Time: $(BUILD_TIME)"

# Help
.PHONY: help
help:
	@echo "Rescale Interlink Build System (FIPS 140-3 Compliant)"
	@echo ""
	@echo "All builds use GOFIPS140=latest for FedRAMP/FIPS compliance."
	@echo "See: https://go.dev/doc/security/fips140"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Build Targets (FIPS 140-3):"
	@echo "  build                   Build for current platform (default)"
	@echo "  build-darwin-arm64      Build macOS Apple Silicon binary"
	@echo "  build-darwin-amd64      Build macOS Intel binary"
	@echo "  build-linux-amd64       Build Linux AMD64 binary"
	@echo "  build-windows-amd64     Build Windows AMD64 binary (standard, requires GPU)"
	@echo "  build-windows-amd64-mesa Build Windows AMD64 binary (Mesa software rendering)"
	@echo "  build-windows-all       Build both Windows variants"
	@echo "  build-all               Build all platform binaries (including both Windows variants)"
	@echo ""
	@echo "Release Targets:"
	@echo "  package                 Create release archives in dist/"
	@echo ""
	@echo "Development Targets:"
	@echo "  test                    Run all tests (FIPS mode)"
	@echo "  test-coverage           Run tests with coverage report"
	@echo "  fmt                     Format code with go fmt"
	@echo "  lint                    Lint code (requires golangci-lint)"
	@echo "  install                 Build and install locally"
	@echo ""
	@echo "Utility Targets:"
	@echo "  clean                   Remove build artifacts"
	@echo "  clean-version           Remove $(VERSION) binaries"
	@echo "  version                 Show version information"
	@echo "  help                    Show this help message"
	@echo ""
	@echo "Examples:"
	@echo "  make build-all          # Build for all platforms"
	@echo "  make package            # Create release archives"
	@echo "  make clean build        # Clean then build"
