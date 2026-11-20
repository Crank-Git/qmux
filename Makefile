.PHONY: check build test race vet lint fuzz

check: vet lint test race

build:
	go build -o bin/ ./cmd/...

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, skipping lint"; \
		exit 1; \
	fi

fuzz:
	FUZZTIME=${FUZZTIME:-30} go test -fuzz=Fuzz -fuzztime=${FUZZTIME}s ./...
