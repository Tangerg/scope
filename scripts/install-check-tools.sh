#!/usr/bin/env bash
# Owns the pinned versions of the tools scripts/check.sh runs. A linter that
# differs between CI and a developer machine makes the same code pass in one
# place and fail in the other.
set -euo pipefail

go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
