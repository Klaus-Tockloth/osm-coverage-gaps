#!/bin/sh

# ------------------------------------
# Purpose:
# - Build binaries for all target systems from macOS.
#
# Releases:
# - v1.0.0 - 2026-11-22: Initial release.
# ------------------------------------

# set -o xtrace
set -o errexit
set -o verbose

# Update module dependencies and renew the vendor directory.
go mod tidy
go mod vendor

# Run static code linters.
golangci-lint run --no-config --enable gocritic
revive

# Run vulnerability and security scanners.
govulncheck ./...
gosec -exclude=G304 ./...

# Print the installed Go compiler version.
go version

# Run tests.
go test -race ./...

# Compile binaries for macOS (darwin).
env GOOS=darwin GOARCH=arm64 go build -v -o binaries/darwin-arm64/osm-coverage-gaps
env GOOS=darwin GOARCH=amd64 go build -v -o binaries/darwin-amd64/osm-coverage-gaps

# Compile binaries for Linux.
env GOOS=linux GOARCH=amd64 go build -v -o binaries/linux-amd64/osm-coverage-gaps
env GOOS=linux GOARCH=arm64 go build -v -o binaries/linux-arm64/osm-coverage-gaps

# Compile binaries for Windows.
env GOOS=windows GOARCH=amd64 go build -v -o binaries/windows-amd64/osm-coverage-gaps.exe
env GOOS=windows GOARCH=arm64 go build -v -o binaries/windows-arm64/osm-coverage-gaps.exe
