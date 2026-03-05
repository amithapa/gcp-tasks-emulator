.PHONY: run build docker test lint clean

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
