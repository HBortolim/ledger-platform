.PHONY: up down up-obs down-obs up-chaos down-chaos chaos build test test-e2e load load-throughput load-spike clean lint fmt

up:
	docker compose up --build -d

down:
	docker compose down -v

COMPOSE_OBS := -f docker-compose.yml -f docker-compose.observability.yml

up-obs:
	docker compose $(COMPOSE_OBS) up --build -d

down-obs:
	docker compose $(COMPOSE_OBS) down -v

COMPOSE_CHAOS := $(COMPOSE_OBS) -f docker-compose.chaos.yml

# Stack with the observability overlay and a latency proxy on Wallet -> Ledger, for scripts/chaos.sh.
up-chaos:
	docker compose $(COMPOSE_CHAOS) up --build -d

down-chaos:
	docker compose $(COMPOSE_CHAOS) down -v

# Failure drills under load (SPEC.md 10.5). Needs `make up-chaos`; it stops and starts services, so run it on a dev stack.
chaos:
	scripts/chaos.sh

build:
	$(MAKE) -C services/wallet-service build
	cd services/ledger-service && go build ./...
	cd services/projection-service && go build ./...

test:
	$(MAKE) -C services/wallet-service test
	cd services/ledger-service && go test ./...
	cd services/projection-service && go test ./...
test-e2e:
	cd tests/e2e && go test -tags=e2e ./...

lint:
	$(MAKE) -C services/wallet-service lint
	cd services/ledger-service && golangci-lint run ./...
	cd services/projection-service && golangci-lint run ./...
	cd tests/e2e && go vet -tags=e2e ./...

fmt:
	$(MAKE) -C services/wallet-service fmt
	cd services/ledger-service && gofmt -w .
	cd services/projection-service && gofmt -w .

# Load tests (SPEC.md §10.6, NFR-PERF-4). Need a running stack (`make up`); no k6 install, it runs in Docker.
# Set BASE_URL (and LEDGER_URL, USER_TOKEN, ADMIN_TOKEN) to target a deployment instead of the local stack.
load:
	scripts/load.sh steady

load-throughput:
	scripts/load.sh throughput

load-spike:
	scripts/load.sh spike

clean:
	docker compose down -v --rmi local
	$(MAKE) -C services/wallet-service clean
	cd services/ledger-service && go clean ./...
	cd services/projection-service && go clean ./...
