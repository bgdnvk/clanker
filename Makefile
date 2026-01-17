BINARY_NAME=clanker
BUILD_DIR=./bin
MAIN_PATH=./main.go

# Install settings
# - If Homebrew is available, install into $(brew --prefix)/bin so it wins on PATH.
# - Otherwise fall back to /usr/local/bin.
BREW_PREFIX := $(shell brew --prefix 2>/dev/null)
INSTALL_PREFIX ?= $(if $(BREW_PREFIX),$(BREW_PREFIX),/usr/local)
INSTALL_BIN ?= $(INSTALL_PREFIX)/bin
INSTALL_PATH ?= $(INSTALL_BIN)/$(BINARY_NAME)

# Release settings (override at runtime)
# Example:
#   make release TAG=v0.0.2
#   make release-create TAG=v0.0.2
TAG ?= v0.0.0
DIST_DIR ?= ./dist

.PHONY: build build-all clean test test-short run install uninstall dev deps fmt vet lint docs quick ci help \
	release-clean release-build-macos release-tar-macos release-sha release release-create release-upload

# Default target
all: build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	@go build -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f $(BINARY_NAME)
	@echo "Clean complete"

# Run tests
test:
	@echo "Running tests..."
	@go test -v ./...

# Run tests in short mode (for CI)
test-short:
	@echo "Running tests in short mode..."
	@go test -short ./...

# Build for multiple platforms
build-all: clean
	@echo "Building for multiple platforms..."
	@mkdir -p $(BUILD_DIR)
	
	@echo "Building for Linux AMD64..."
	@GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	
	@echo "Building for Linux ARM64..."
	@GOOS=linux GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(MAIN_PATH)
	
	@echo "Building for macOS AMD64..."
	@GOOS=darwin GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	
	@echo "Building for macOS ARM64..."
	@GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	
	@echo "Building for Windows AMD64..."
	@GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)
	
	@echo "Multi-platform build complete"

# Check for potential issues
vet:
	@echo "Running go vet..."
	@go vet ./...

# CI pipeline (what GitHub Actions runs)
ci: deps fmt vet test-short build
	@echo "CI pipeline complete"

# Run with arguments (make run ARGS="--help")
run: build
	@$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

install: build
	@echo "Installing $(BINARY_NAME) to $(INSTALL_PATH)..."
	@mkdir -p $(INSTALL_BIN)
	@if [ -w "$(INSTALL_BIN)" ]; then \
		install -m 0755 "$(BUILD_DIR)/$(BINARY_NAME)" "$(INSTALL_PATH)"; \
	else \
		sudo install -m 0755 "$(BUILD_DIR)/$(BINARY_NAME)" "$(INSTALL_PATH)"; \
	fi
	@echo "Installation complete. You can now run '$(BINARY_NAME)' from anywhere."

uninstall:
	@echo "Removing $(BINARY_NAME) from $(INSTALL_PATH)..."
	@if [ -w "$(INSTALL_BIN)" ]; then \
		rm -f "$(INSTALL_PATH)"; \
	else \
		sudo rm -f "$(INSTALL_PATH)"; \
	fi
	@echo "Uninstallation complete"

# Development build (builds in current directory)
dev:
	@echo "Building for development..."
	@go build -o $(BINARY_NAME) $(MAIN_PATH)
	@echo "Development build complete: ./$(BINARY_NAME)"

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	@go mod download
	@go mod tidy

# Format code
fmt:
	@echo "Formatting code..."
	@go fmt ./...

# Lint code (requires golangci-lint)
lint:
	@echo "Linting code..."
	@golangci-lint run

# Generate documentation
docs:
	@echo "Generating documentation..."
	@go doc -all ./... > docs.txt

# Quick development cycle
quick: fmt build

# Show help
help:
	@echo "Available targets:"
	@echo "  build      - Build the binary"
	@echo "  build-all  - Build for multiple platforms"
	@echo "  clean      - Clean build artifacts"
	@echo "  test       - Run tests"
	@echo "  test-short - Run tests in short mode"
	@echo "  run        - Build and run (use ARGS=\"...\" for arguments)"
	@echo "  install    - Install to $(INSTALL_BIN) (Homebrew prefix if available)"
	@echo "  uninstall  - Remove from $(INSTALL_BIN) (Homebrew prefix if available)"
	@echo "  dev        - Build for development"
	@echo "  deps       - Download dependencies"
	@echo "  fmt        - Format code"
	@echo "  vet        - Run go vet"
	@echo "  lint       - Lint code"
	@echo "  docs       - Generate documentation"
	@echo "  quick      - Format and build"
	@echo "  ci         - Run CI pipeline"
	@echo "  help       - Show this help"
	@echo "  release                - Build macOS tarballs + print sha256 (TAG=vX.Y.Z)"
	@echo "  release-create         - Create GitHub release + upload macOS tarballs (TAG=vX.Y.Z)"
	@echo "  release-upload         - Upload tarballs to an existing GitHub release (TAG=vX.Y.Z)"
	@echo "  release-clean          - Remove dist/ artifacts"

# -------------------- Release targets (macOS tarballs for Homebrew) --------------------

release-clean:
	@rm -rf $(DIST_DIR)
	@rm -f clanker_$(TAG)_darwin_arm64.tar.gz clanker_$(TAG)_darwin_amd64.tar.gz

release-build-macos: release-clean
	@echo "Building macOS binaries for $(TAG)..."
	@mkdir -p $(DIST_DIR)
	@echo "- darwin/arm64"
	@GOOS=darwin GOARCH=arm64 go build -o $(DIST_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@mv $(DIST_DIR)/$(BINARY_NAME) $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64
	@echo "- darwin/amd64"
	@GOOS=darwin GOARCH=amd64 go build -o $(DIST_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@mv $(DIST_DIR)/$(BINARY_NAME) $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64

release-tar-macos: release-build-macos
	@echo "Creating release tarballs..."
	@mkdir -p $(DIST_DIR)/tmp/darwin-arm64 $(DIST_DIR)/tmp/darwin-amd64
	@cp $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 $(DIST_DIR)/tmp/darwin-arm64/$(BINARY_NAME)
	@cp $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 $(DIST_DIR)/tmp/darwin-amd64/$(BINARY_NAME)
	@tar -C $(DIST_DIR)/tmp/darwin-arm64 -czf clanker_$(TAG)_darwin_arm64.tar.gz $(BINARY_NAME)
	@tar -C $(DIST_DIR)/tmp/darwin-amd64 -czf clanker_$(TAG)_darwin_amd64.tar.gz $(BINARY_NAME)
	@rm -rf $(DIST_DIR)/tmp
	@echo "Created: clanker_$(TAG)_darwin_arm64.tar.gz"
	@echo "Created: clanker_$(TAG)_darwin_amd64.tar.gz"

release-sha: release-tar-macos
	@echo "sha256 (paste into Homebrew formula):"
	@shasum -a 256 clanker_$(TAG)_darwin_arm64.tar.gz
	@shasum -a 256 clanker_$(TAG)_darwin_amd64.tar.gz

release: release-sha
	@echo "Done. Next: gh release create $(TAG) ..."

release-create: release-tar-macos
	@echo "Creating GitHub release $(TAG) and uploading assets..."
	@gh release create $(TAG) \
		clanker_$(TAG)_darwin_arm64.tar.gz \
		clanker_$(TAG)_darwin_amd64.tar.gz \
		--title $(TAG) --notes "" | cat

release-upload: release-tar-macos
	@echo "Uploading assets to existing GitHub release $(TAG)..."
	@gh release upload $(TAG) \
		clanker_$(TAG)_darwin_arm64.tar.gz \
		clanker_$(TAG)_darwin_amd64.tar.gz \
		--clobber | cat