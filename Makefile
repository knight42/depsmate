.PHONY: build test lint tidy

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run

tidy:
	go mod tidy
