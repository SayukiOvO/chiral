.PHONY: proto build vet test

proto:
	buf lint && buf generate

build:
	go build -o bin/chiral-core ./core/cmd/core
	go build -o bin/chiral-agent ./agent/cmd/agent

vet:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		printf '%s\n' "$$files"; \
		exit 1; \
	fi
	go vet ./...

test:
	go test ./...
