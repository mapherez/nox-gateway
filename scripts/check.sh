#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
formatted=$(gofmt -l cmd internal)
if [ -n "$formatted" ]; then
    printf '%s\n' "$formatted"
    exit 1
fi
go mod tidy -diff
go mod verify
go vet -mod=readonly ./...
node --test scripts/release.test.mjs
python3 -m unittest discover -s scripts -p 'test_*.py'
go test -race -mod=readonly -count=1 ./...
mkdir -p bin
go build -mod=readonly -trimpath -o bin/nox-gateway ./cmd/nox-gateway
python3 scripts/smoke.py --binary bin/nox-gateway
