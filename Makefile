.DEFAULT_GOAL := help

BIN := cagy
MODULE := github.com/kazimshah39/cagy

.PHONY: help
help: ## Display this help message
	@echo "cagy development targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build cagy binary
	go build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/cagy

.PHONY: install
install: ## Install cagy binary to GOBIN or GOPATH/bin
	go install -trimpath -ldflags "-s -w" ./cmd/cagy

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: test-race
test-race: ## Run unit tests with race detector
	go test -race ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format Go source files
	gofmt -w cmd internal

.PHONY: fmt-check
fmt-check: ## Check Go source code formatting
	@files="$$(gofmt -l cmd internal)"; \
	if [ -n "$$files" ]; then \
		echo "Files need formatting:"; \
		echo "$$files"; \
		exit 1; \
	fi

.PHONY: check-scripts
check-scripts: ## Verify shell script syntax
	bash -n install.sh
	sh -n install.sh
	bash -n scripts/test-installer.sh

.PHONY: test-installer
test-installer: ## Run end-to-end installer tests with local fake release server
	./scripts/test-installer.sh

.PHONY: mod-check
mod-check: ## Verify module integrity
	go mod verify

.PHONY: verify
verify: fmt-check mod-check vet test-race check-scripts test-installer ## Run all verification checks

VERSION ?= 0.1.0

.PHONY: release-build
release-build: ## Build release archives and checksums for all platforms (e.g. make release-build VERSION=0.1.0)
	@rm -rf dist
	@mkdir -p dist
	@for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
		os=$${target%/*}; \
		arch=$${target#*/}; \
		echo "Building $${os}/$${arch}..."; \
		build_dir="dist/build_$${os}_$${arch}"; \
		mkdir -p "$$build_dir"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o "$$build_dir/cagy" ./cmd/cagy; \
		cp LICENSE "$$build_dir/"; \
		cp README.md "$$build_dir/"; \
		tar -czf "dist/cagy_$(VERSION)_$${os}_$${arch}.tar.gz" -C "$$build_dir" cagy LICENSE README.md; \
		rm -rf "$$build_dir"; \
	done
	@cd dist && (command -v sha256sum >/dev/null 2>&1 && sha256sum cagy_*.tar.gz > checksums.txt || shasum -a 256 cagy_*.tar.gz > checksums.txt)
	@echo "Release assets generated in dist/ for version $(VERSION)"

.PHONY: clean
clean: ## Clean build and test artifacts
	rm -rf $(BIN) dist coverage.out coverage.html
