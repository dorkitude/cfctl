# cfctl developer targets. `make help` lists them.
.PHONY: help build install test race vet fmt fmt-check lint generate spec smoke smoke-sweep docs-check wrangler-doc clean

BIN ?= cfctl

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

build: ## Build ./cfctl
	go build -o $(BIN) .

install: ## go install into $(go env GOPATH)/bin
	go install .

test: ## Run the tests (fake Cloudflare API; no token or network)
	go test ./...

race: ## Run the tests with the race detector (slow on small machines)
	go test -race ./...

vet: ## go vet
	go vet ./...

fmt: ## gofmt every Go file in place
	gofmt -w $$(git ls-files '*.go')

fmt-check: ## Fail if any Go file isn't gofmt-ed
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "not gofmt-ed:"; echo "$$out"; exit 1; fi

lint: fmt-check vet ## fmt-check + vet, plus golangci-lint if installed
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed; ran fmt-check and vet only"; fi

generate: ## Regenerate internal/apispec/ops_gen.go from the vendored spec
	go generate ./...

spec: ## Refresh the vendored Cloudflare OpenAPI spec and regenerate
	./scripts/update-spec.sh

smoke: build ## Read-only smoke test against a real account (scripts/smoke.sh; needs a token)
	@if [ ! -x scripts/smoke.sh ]; then echo "scripts/smoke.sh not found"; exit 1; fi
	PATH="$(CURDIR):$$PATH" CFCTL_BIN="$(CURDIR)/$(BIN)" CFCTL_READONLY=1 ./scripts/smoke.sh

smoke-sweep: build ## Read-only sweep of every account/zone-level generated GET (cfctl api smoke; SMOKE_ZONE=example.com)
	@if [ -z "$(SMOKE_ZONE)" ]; then echo "set SMOKE_ZONE=<zone name>"; exit 1; fi
	CFCTL_READONLY=1 ./$(BIN) api smoke --zone "$(SMOKE_ZONE)"

docs-check: build ## Check every documented cfctl example against --help
	./scripts/docs-check --bin ./$(BIN)

wrangler-doc: ## Regenerate docs/cfctl-vs-wrangler.md tables from docs/wrangler-map/*.tsv
	./scripts/gen-wrangler-doc.py

clean: ## Remove the built binary
	rm -f $(BIN)
