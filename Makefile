.PHONY: check build test race vet lint fuzz

FUZZTIME ?= 30s

# check calls each step in sequence, so the order holds under make -j too.
# Make stops at the first step that fails.
check:
	$(MAKE) vet
	$(MAKE) lint
	$(MAKE) test
	$(MAKE) race

build:
	go build -o bin/ ./cmd/...

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint is not installed. Install golangci-lint v2: https://golangci-lint.run/docs/welcome/install/local/" >&2; \
		exit 1; \
	}
	golangci-lint run

# go test -fuzz accepts one package and one target, so fuzz runs each target separately.
fuzz:
	@set -e; found=0; \
	for pkg in $$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...); do \
		list=$$(go test -list '^Fuzz' $$pkg); \
		for target in $$(echo "$$list" | grep '^Fuzz' || true); do \
			found=1; \
			echo "go test -run=^\$$ -fuzz=^$$target\$$ -fuzztime=$(FUZZTIME) $$pkg"; \
			go test -run='^$$' -fuzz="^$$target\$$" -fuzztime=$(FUZZTIME) $$pkg; \
		done; \
	done; \
	if [ $$found -eq 0 ]; then echo "no fuzz targets"; fi
