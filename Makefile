.PHONY: all build test vet

all: vet test build

build:
	go build -o bin/fast ./cmd/fast

test:
	go test ./...

vet:
	go vet ./...
