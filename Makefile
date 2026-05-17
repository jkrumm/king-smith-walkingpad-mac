# king-smith-walkingpad-mac — single-target dev loop.
# Run `make` for the menu, `make up` for everything else.

REPO          := king-smith-walkingpad-mac
BINARY        := walkingpad
APP_NAME      := WalkingPad
CMD_DIR       := ./cmd/$(REPO)
BIN_DIR       := ./bin
APP_BUNDLE    := $(BIN_DIR)/$(APP_NAME).app
APP_INSTALL   := /Applications/$(APP_NAME).app
PLIST_LABEL   := com.jkrumm.$(BINARY)
PLIST_TMPL    := ./scripts/$(PLIST_LABEL).plist.tmpl
PLIST_DST     := $(HOME)/Library/LaunchAgents/$(PLIST_LABEL).plist
LOG_OUT       := /tmp/$(BINARY).log
DATA_DIR      := $(HOME)/Library/Application Support/$(APP_NAME)

VERSION       := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS       := -ldflags "-X main.Version=$(VERSION) -s -w"

.DEFAULT_GOAL := help

help: ## (default) print this menu
	@printf "WalkingPad \033[1m%s\033[0m\n\n" "$(VERSION)"
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} \
	  /^[a-zA-Z_-]+:.*?##/ {printf "  \033[36m%-7s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\n\033[1mNormal flow\033[0m\n"
	@printf "  1. \033[36mmake up\033[0m   — every time you change Go or Raycast code.\n"
	@printf "                It rebuilds the daemon, swaps /Applications/WalkingPad.app,\n"
	@printf "                kickstarts the LaunchAgent, prints the live version for\n"
	@printf "                confirmation, then starts the Raycast dev loop.\n"
	@printf "  2. \033[36mmake logs\033[0m — tail the daemon log in another terminal.\n"
	@printf "  3. \033[36mmake test\033[0m — full Go race suite + Raycast typecheck.\n"

up: ## rebuild daemon, deploy, reload, verify, start Raycast dev (the one command for everything)
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) $(CMD_DIR)
	./scripts/build-app-bundle.sh $(BIN_DIR)/$(BINARY) $(VERSION) $(APP_BUNDLE)
	@mkdir -p "$(DATA_DIR)" "$(HOME)/Library/LaunchAgents"
	@if [ ! -f $(PLIST_DST) ]; then \
	  echo "first-run: installing LaunchAgent plist"; \
	  ./scripts/install-launch-agent.sh $(PLIST_TMPL) $(PLIST_DST); \
	  launchctl load -w $(PLIST_DST); \
	fi
	rm -rf $(APP_INSTALL)
	cp -R $(APP_BUNDLE) $(APP_INSTALL)
	launchctl kickstart -k gui/$$(id -u)/$(PLIST_LABEL)
	@sleep 1
	@printf "daemon source : \033[1m%s\033[0m\n" "$(VERSION)"
	@printf "daemon live   : "
	@curl -fs http://127.0.0.1:7706/health | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("version","?"))' 2>/dev/null || echo "(no /health response yet — agent may still be booting)"
	@echo "---"
	@if ! node -v 2>/dev/null | grep -q '^v22\.'; then \
	  echo "node $$(node -v 2>/dev/null || echo missing) installed; raycast/.nvmrc pins 22.22.2."; \
	  echo "run \`nvm install 22.22.2 && nvm use\` (or volta/mise) then re-run \`make up\`."; \
	  echo "daemon is already deployed and live — only the Raycast dev loop is gated."; \
	else \
	  cd raycast && npm install && npm run dev; \
	fi

test: ## run all tests (Go race + Raycast typecheck)
	go test -race -count=1 ./...
	cd raycast && npx tsc --noEmit

logs: ## tail the daemon log
	@test -f $(LOG_OUT) && tail -f $(LOG_OUT) || echo "$(LOG_OUT) does not exist yet"

scan: ## list nearby WalkingPad BLE devices (dev tool, not the daemon)
	@mkdir -p $(BIN_DIR)
	@go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) $(CMD_DIR)
	$(BIN_DIR)/$(BINARY) scan

fmt: ## format Go + tidy modules
	gofmt -s -w .
	go mod tidy

lint: ## run golangci-lint (pre-commit hook also runs this)
	@which golangci-lint > /dev/null || (echo "install: brew install golangci-lint" && exit 1)
	golangci-lint run ./...

clean: ## remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

.PHONY: help up test logs scan fmt lint clean
