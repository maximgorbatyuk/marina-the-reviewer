.PHONY: build test verify snapshot

build:
	go build -trimpath -ldflags "-X github.com/maximgorbatyuk/marina-the-reviewer/internal/buildinfo.Version=$$(cat VERSION)-dev" -o bin/marinadoreview ./cmd/marinadoreview

test:
	go test ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'

verify:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test ./...
	go test -race ./...
	go build ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'

snapshot:
	goreleaser release --snapshot --clean
