.PHONY: check test build run clean

check:
	sh scripts/check.sh

test:
	go test -race -mod=readonly ./...

build:
	mkdir -p bin
	go build -mod=readonly -trimpath -o bin/nox-gateway ./cmd/nox-gateway

run:
	go run -mod=readonly ./cmd/nox-gateway

clean:
	rm -rf bin coverage.out
