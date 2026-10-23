BINARY_NAME := rebind
BUILD_DIR := bin

.PHONY: all run build test clean tidy install help verify inspect

all: build

## run: Run the TUI application directly (supports optional URL="https://...")
run:
	@go run . $(URL)

## build: Compile the binary into ./bin/rebind
build:
	@mkdir -p $(BUILD_DIR)
	@go build -o $(BUILD_DIR)/$(BINARY_NAME) .
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME)"

## verify: Convert sample PDF headlessly and run full audit against Calibre baseline
verify: build
	@./$(BUILD_DIR)/$(BINARY_NAME) --headless -o sample/harry-potter-rebind.epub sample/harry-potter-original-pdf.pdf
	@go run ./cmd/verify audit --pdf sample/harry-potter-original-pdf.pdf --epub sample/harry-potter-rebind.epub --calibre sample/harry-potter-calibre.epub

## inspect: Inspect pipeline stages for specific pages (e.g. make inspect PAGES=15-16)
inspect:
	@go run ./cmd/verify inspect --pdf sample/harry-potter-original-pdf.pdf --pages $(or $(PAGES),15-16)

## start: Build and launch the compiled binary
start: build
	@./$(BUILD_DIR)/$(BINARY_NAME) $(URL)

## test: Run all unit tests
test:
	@go test -v ./...

## tidy: Tidy Go module dependencies
tidy:
	@go mod tidy

## install: Install rebind to $GOPATH/bin (or ~/go/bin)
install:
	@go install .
	@echo "Installed $(BINARY_NAME) to $$(go env GOPATH)/bin/$(BINARY_NAME)"

## clean: Remove build artifacts
clean:
	@rm -rf $(BUILD_DIR) $(BINARY_NAME)
	@echo "Cleaned build artifacts"

## help: Show available make commands
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@awk 'BEGIN {FS = ": "}; /^## / {sub(/^## /, ""); printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
