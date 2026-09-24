.PHONY: all ui build run dev test vet doctor deps clean help

all: ui build

help:          ## list the targets
	@grep -E '^[a-z]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/ —/'

doctor:        ## check the environment: required and optional tools (aria2c, Chrome…)
	@./scripts/doctor.sh

deps:          ## install everything PRISM can use via Homebrew (see Brewfile)
	brew bundle --file=Brewfile

ui:            ## build the Svelte UI into web/dist (embedded into the binary)
	cd web && npm install --no-audit --no-fund && npm run build

build:         ## build the backend binary (run `make ui` first for the embedded UI)
	go build -o bin/prism ./cmd/prism

run: all       ## build everything and start PRISM on http://127.0.0.1:7777
	./bin/prism

dev:           ## backend on :7777 and the Vite dev server on :5173 together (Ctrl-C stops both)
	@echo "backend http://127.0.0.1:7777 · UI (hot reload) http://localhost:5173"
	@trap 'kill 0' INT TERM EXIT; go run ./cmd/prism & (cd web && npm run dev) & wait

test:          ## Go tests (need a scratch Postgres: PRISM_TEST_DSN=postgres://user@host:port/postgres)
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin web/dist/assets web/node_modules
