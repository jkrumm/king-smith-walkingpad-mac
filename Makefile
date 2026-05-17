# king-smith-walkingpad-mac — LaunchAgent + Raycast extension for the KingSmith WalkingPad P1
# See PRD.md for the full design; this Makefile is the operator interface.
#
# Naming: the repo (and Go module) keep the long form `king-smith-walkingpad-mac`
# for SEO; everything user-facing is just `WalkingPad` / `walkingpad`.

REPO          := king-smith-walkingpad-mac
BINARY        := walkingpad
APP_NAME      := WalkingPad
PKG           := github.com/jkrumm/$(REPO)
CMD_DIR       := ./cmd/$(REPO)
BIN_DIR       := ./bin
APP_BUNDLE    := $(BIN_DIR)/$(APP_NAME).app
APP_INSTALL   := /Applications/$(APP_NAME).app
PLIST_LABEL   := com.jkrumm.$(BINARY)
PLIST_TMPL    := ./scripts/$(PLIST_LABEL).plist.tmpl
PLIST_DST     := $(HOME)/Library/LaunchAgents/$(PLIST_LABEL).plist
LOG_OUT       := /tmp/$(BINARY).log
LOG_ERR       := /tmp/$(BINARY).err
DATA_DIR      := $(HOME)/Library/Application Support/$(APP_NAME)

VERSION       := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS       := -ldflags "-X main.Version=$(VERSION) -s -w"

.DEFAULT_GOAL := help

help: ## print available targets
	@awk 'BEGIN {FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*?##/ {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## compile the daemon binary to ./bin/
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) $(CMD_DIR)
	@echo "built $(BIN_DIR)/$(BINARY) ($(VERSION))"

run: build ## build and run locally in foreground (dev)
	$(BIN_DIR)/$(BINARY)

test: ## run all Go tests with race detector
	go test -race -count=1 ./...

test-cover: ## run tests with coverage report
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt: ## format Go source
	gofmt -s -w .
	go mod tidy

lint: ## run static analysis (requires golangci-lint)
	@which golangci-lint > /dev/null || (echo "install: brew install golangci-lint" && exit 1)
	golangci-lint run ./...

build-app: build ## wrap the binary into WalkingPad.app (required for macOS Bluetooth)
	@test -x ./scripts/build-app-bundle.sh || (echo "missing ./scripts/build-app-bundle.sh — write it first (see PRD §17.3 step 2)" && exit 1)
	./scripts/build-app-bundle.sh $(BIN_DIR)/$(BINARY) $(VERSION) $(APP_BUNDLE)
	@echo "built $(APP_BUNDLE)"

install: build-app ## build .app, copy to /Applications, install LaunchAgent
	rm -rf $(APP_INSTALL)
	cp -R $(APP_BUNDLE) $(APP_INSTALL)
	@echo "installed: $(APP_INSTALL)"
	@$(MAKE) install-agent

install-agent: ## install the LaunchAgent plist and load it
	@test -f $(PLIST_TMPL) || (echo "missing $(PLIST_TMPL) — generate it first" && exit 1)
	@test -x ./scripts/install-launch-agent.sh || (echo "missing ./scripts/install-launch-agent.sh" && exit 1)
	@mkdir -p "$(DATA_DIR)"
	@mkdir -p "$(HOME)/Library/LaunchAgents"
	./scripts/install-launch-agent.sh $(PLIST_TMPL) $(PLIST_DST)
	launchctl unload $(PLIST_DST) 2>/dev/null || true
	launchctl load -w $(PLIST_DST)
	@echo "LaunchAgent loaded: $(PLIST_LABEL)"

uninstall-agent: ## unload and remove the LaunchAgent plist
	launchctl unload $(PLIST_DST) 2>/dev/null || true
	rm -f $(PLIST_DST)
	@echo "LaunchAgent removed"

reload: ## kickstart the LaunchAgent (faster than unload/load)
	launchctl kickstart -k gui/$$(id -u)/$(PLIST_LABEL)
	@echo "kickstarted $(PLIST_LABEL)"

logs: ## tail the daemon stdout log
	@test -f $(LOG_OUT) && tail -f $(LOG_OUT) || echo "$(LOG_OUT) does not exist yet"

logs-err: ## tail the daemon stderr log
	@test -f $(LOG_ERR) && tail -f $(LOG_ERR) || echo "$(LOG_ERR) does not exist yet"

clean: ## remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

scan: build ## scan for nearby WalkingPad devices (dev tool, not the daemon)
	$(BIN_DIR)/$(BINARY) scan

raycast-dev: ## open Raycast extension dev loop
	cd raycast && npm install && npm run dev

hooks-install: ## install lefthook pre-commit + pre-push hooks (one-time per clone)
	@which lefthook > /dev/null || (echo "install: brew install lefthook" && exit 1)
	lefthook install

check: fmt lint test ## run the full local quality gate (fmt + lint + test)

.PHONY: help build build-app run test test-cover fmt lint check hooks-install install install-agent uninstall-agent reload logs logs-err clean scan raycast-dev
