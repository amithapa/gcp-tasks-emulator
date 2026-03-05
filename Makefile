.PHONY: run build docker test lint clean install-hooks

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
