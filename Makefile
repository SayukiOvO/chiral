.PHONY: proto build vet test

proto:
	buf lint && buf generate

build:
	go build -o bin/chiral-core ./core/cmd/core
	go build -o bin/chiral-agent ./agent/cmd/agent

vet:
	gofmt -l . && go vet ./...

test:
	go test ./...
