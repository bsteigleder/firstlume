BIN      := bin/firstlume
IMAGE    ?= firstlume:dev
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
CHART    := deploy/helm/firstlume

# Not 8080, which often collides with other local dev servers.
DEV_ADDR          := 127.0.0.1:8787
DEV_DATABASE_URL  := postgres://firstlume:firstlume@localhost:5432/firstlume?sslmode=disable
TEST_DATABASE_URL := postgres://firstlume:firstlume@localhost:5432/firstlume_test?sslmode=disable

# Local secrets created by `make dev-env`.
-include .env
export

.PHONY: build web web-dev test test-integration vet dev-db dev-env run-server run-agent helm-lint docker-build clean

build: web
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/firstlume

## Builds the UI into web/dist, which the binary embeds.
web: web/node_modules
	cd web && npm run build

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	@touch $@

## UI with hot reload on http://localhost:5173, proxying the API to `make run-server`.
web-dev: web/node_modules
	cd web && npm run dev

test:
	go test -race ./...

## Needs `make dev-db`.
test-integration:
	FIRSTLUME_TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -race -count=1 ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

dev-db:
	docker compose up -d --wait postgres

## Generates local secrets into .env (only once).
dev-env: build
	@test -f .env || { $(BIN) keygen > .env; echo "FIRSTLUME_DATABASE_URL=$(DEV_DATABASE_URL)" >> .env; echo "wrote .env"; }

run-server: build
	FIRSTLUME_LISTEN_ADDR=$(DEV_ADDR) $(BIN) server

## Runs the agent with the local driver (executors as subprocesses, no Kubernetes needed).
run-agent: build
	@FIRSTLUME_API_URL=http://$(DEV_ADDR) FIRSTLUME_AGENT_TOKEN=$(FIRSTLUME_BOOTSTRAP_AGENT_TOKEN) \
	FIRSTLUME_DRIVER=local $(BIN) agent

helm-lint:
	helm lint $(CHART)
	helm template test $(CHART) --set controlPlane.database.url=postgres://x > /dev/null

docker-build:
	docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(VERSION) -t $(IMAGE) .

clean:
	rm -rf bin web/node_modules
	find web/dist -mindepth 1 ! -name .gitkeep -delete
