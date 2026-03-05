.PHONY: run build docker test lint clean install-hooks release-snapshot release-snapshot-docker

run:
	go run ./cmd/server

build:
	CGO_ENABLED=1 go build -o cloud-tasks-emulator ./cmd/server

docker:
	docker build -t cloud-tasks-emulator:latest .

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run

clean:
	rm -f cloud-tasks-emulator tasks.db

install-hooks:
	@command -v pre-commit >/dev/null 2>&1 || { echo "Install pre-commit: pip install pre-commit  OR  brew install pre-commit"; exit 1; }
	pre-commit install
	@echo "Pre-commit hooks installed. They will run on 'git commit'."

release-snapshot:
	@command -v goreleaser >/dev/null 2>&1 || { echo "Install goreleaser: brew install goreleaser  OR  go install github.com/goreleaser/goreleaser/v2@latest"; exit 1; }
	goreleaser release --snapshot --clean
	@echo "Snapshot built in dist/. Run this before tagging to catch release issues locally."

# Test release in Linux container (matches CI - use this on Mac/Windows to verify before tagging)
release-snapshot-docker:
	docker run --rm -v "$$(pwd):/app" -w /app -e HOME=/tmp golang:1.24-alpine sh -c "\
		apk add --no-cache gcc musl-dev sqlite-dev git && \
		go install github.com/goreleaser/goreleaser/v2@v2.11.0 && \
		$$(go env GOPATH)/bin/goreleaser release --snapshot --clean"
	@echo "Snapshot built in dist/. This matches the CI environment."
