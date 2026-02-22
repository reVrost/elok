.PHONY: all dev air build run test ui build-ui ui-install clean

PROJECT_NAME := elok
TARGET_MAIN := ./cmd/elok
BINARY_PATH := ./tmp/$(PROJECT_NAME)
AIR ?= air

all: dev

dev:
	@command -v $(AIR) >/dev/null 2>&1 || (echo "air is not installed (go install github.com/air-verse/air@latest)" && exit 1)
	@$(AIR) -c .air.toml

air: dev

build:
	@mkdir -p tmp
	@go build -o $(BINARY_PATH) $(TARGET_MAIN)

run: build
	@$(BINARY_PATH) run

test:
	@go test ./...

ui-install:
	@cd ui && npm install

ui:
	@cd ui && npm run build

build-ui: ui

clean:
	@rm -rf tmp
