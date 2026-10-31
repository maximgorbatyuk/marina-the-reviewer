.PHONY: build test lint-config lint fmt verify snapshot

GOLANGCI_LINT ?= golangci-lint

build: lint
	go build -trimpath -ldflags "-X github.com/maximgorbatyuk/marina-the-reviewer/internal/buildinfo.Version=$$(cat VERSION)-dev" -o bin/marinadoreview ./cmd/marinadoreview

test:
	go test ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'

lint-config:
	$(GOLANGCI_LINT) config verify --config .golangci.yml

lint: lint-config
	$(GOLANGCI_LINT) run --config .golangci.yml ./...

fmt: lint-config
	$(GOLANGCI_LINT) fmt --config .golangci.yml

verify: lint
	go vet ./...
	go test ./...
	go test -race ./...
	go build ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'

snapshot:
	goreleaser release --snapshot --clean
