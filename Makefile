.PHONY: build test eval lint

build:
	go build -o bin/sblame ./cmd/sblame

test:
	go test ./...

eval:
	@echo "eval: not implemented"

lint:
	golangci-lint run ./...
