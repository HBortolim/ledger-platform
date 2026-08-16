.PHONY: up down build test test-e2e load clean lint fmt

up:
	docker compose up --build -d

down:
	docker compose down -v

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

load:
	k6 run infrastructure/load/transfer_steady.js

clean:
	docker compose down -v --rmi local
	$(MAKE) -C services/wallet-service clean
	cd services/ledger-service && go clean ./...
	cd services/projection-service && go clean ./...
