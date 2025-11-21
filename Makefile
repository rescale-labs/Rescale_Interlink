# Makefile for Rescale Interlink
# Build and package cross-platform binaries

# Variables
VERSION := v2.4.8
BINARY_NAME := rescale-int
BUILD_TIME := $(shell date +%Y-%m-%d)
LDFLAGS := -ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

# Directories
BIN_DIR := bin/$(VERSION)
DARWIN_ARM64_DIR := $(BIN_DIR)/darwin-arm64
DARWIN_AMD64_DIR := $(BIN_DIR)/darwin-amd64
LINUX_AMD64_DIR := $(BIN_DIR)/linux-amd64
WINDOWS_AMD64_DIR := $(BIN_DIR)/windows-amd64

# Default target
.PHONY: all
all: build

# Build for current platform
.PHONY: build
build:
	@echo "Building for current platform..."
	@go build $(LDFLAGS) -o $(BINARY_NAME) ./cmd/rescale-int
	@echo "✅ Built: $(BINARY_NAME)"

# Build macOS Apple Silicon binary
.PHONY: build-darwin-arm64
build-darwin-arm64:
	@echo "Building macOS Apple Silicon binary..."
	@mkdir -p $(DARWIN_ARM64_DIR)
	@GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DARWIN_ARM64_DIR)/$(BINARY_NAME) ./cmd/rescale-int
	@echo "✅ Built: $(DARWIN_ARM64_DIR)/$(BINARY_NAME)"

# Build macOS Intel binary
.PHONY: build-darwin-amd64
build-darwin-amd64:
	@echo "Building macOS Intel binary..."
	@mkdir -p $(DARWIN_AMD64_DIR)
	@GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(DARWIN_AMD64_DIR)/$(BINARY_NAME) ./cmd/rescale-int
	@echo "✅ Built: $(DARWIN_AMD64_DIR)/$(BINARY_NAME)"

# Build Linux binary (cross-compile or run on Linux)
.PHONY: build-linux-amd64
build-linux-amd64:
	@echo "Building Linux AMD64 binary..."
	@mkdir -p $(LINUX_AMD64_DIR)
	@GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(LINUX_AMD64_DIR)/$(BINARY_NAME) ./cmd/rescale-int
	@echo "✅ Built: $(LINUX_AMD64_DIR)/$(BINARY_NAME)"

# Build Windows binary
.PHONY: build-windows-amd64
build-windows-amd64:
	@echo "Building Windows AMD64 binary..."
	@mkdir -p $(WINDOWS_AMD64_DIR)
	@GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(WINDOWS_AMD64_DIR)/$(BINARY_NAME).exe ./cmd/rescale-int
	@echo "✅ Built: $(WINDOWS_AMD64_DIR)/$(BINARY_NAME).exe"

# Build all platform binaries
.PHONY: build-all
build-all: build-darwin-arm64 build-darwin-amd64 build-linux-amd64 build-windows-amd64
	@echo ""
	@echo "✅ All platform binaries built successfully!"
	@echo "   - macOS Apple Silicon: $(DARWIN_ARM64_DIR)/$(BINARY_NAME)"
	@echo "   - macOS Intel:         $(DARWIN_AMD64_DIR)/$(BINARY_NAME)"
	@echo "   - Linux AMD64:         $(LINUX_AMD64_DIR)/$(BINARY_NAME)"
	@echo "   - Windows AMD64:       $(WINDOWS_AMD64_DIR)/$(BINARY_NAME).exe"

# Package binaries for GitHub releases
.PHONY: package
package:
	@echo "Packaging binaries for GitHub releases..."
	@mkdir -p dist
	@cd $(DARWIN_ARM64_DIR) && tar -czf ../../../dist/$(BINARY_NAME)-$(VERSION)-darwin-arm64.tar.gz $(BINARY_NAME)
	@cd $(DARWIN_AMD64_DIR) && tar -czf ../../../dist/$(BINARY_NAME)-$(VERSION)-darwin-amd64.tar.gz $(BINARY_NAME)
	@cd $(LINUX_AMD64_DIR) && tar -czf ../../../dist/$(BINARY_NAME)-$(VERSION)-linux-amd64.tar.gz $(BINARY_NAME)
	@cd $(WINDOWS_AMD64_DIR) && zip -q ../../../dist/$(BINARY_NAME)-$(VERSION)-windows-amd64.zip $(BINARY_NAME).exe
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

# Run tests
.PHONY: test
test:
	@echo "Running tests..."
	@go test -v ./...

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	@echo "Running tests with coverage..."
	@go test -v -coverprofile=coverage.out ./...
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

# Install locally (copy to bin/v2.4.8/darwin-arm64/ for current macOS)
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
	@echo "Rescale Interlink Build System"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Build Targets:"
	@echo "  build                   Build for current platform (default)"
	@echo "  build-darwin-arm64      Build macOS Apple Silicon binary"
	@echo "  build-darwin-amd64      Build macOS Intel binary"
	@echo "  build-linux-amd64       Build Linux AMD64 binary"
	@echo "  build-windows-amd64     Build Windows AMD64 binary"
	@echo "  build-all               Build all platform binaries"
	@echo ""
	@echo "Release Targets:"
	@echo "  package                 Create release archives in dist/"
	@echo ""
	@echo "Development Targets:"
	@echo "  test                    Run all tests"
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
