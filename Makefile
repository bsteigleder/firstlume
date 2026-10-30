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

.PHONY: build test test-integration vet dev-db dev-env run-server run-agent helm-lint docker-build clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/firstlume

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
	FIRSTLUME_API_URL=http://$(DEV_ADDR) FIRSTLUME_AGENT_TOKEN=$(FIRSTLUME_BOOTSTRAP_AGENT_TOKEN) \
	FIRSTLUME_DRIVER=local $(BIN) agent

helm-lint:
	helm lint $(CHART)
	helm template test $(CHART) --set controlPlane.database.url=postgres://x > /dev/null

docker-build:
	docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(VERSION) -t $(IMAGE) .

clean:
	rm -rf bin
