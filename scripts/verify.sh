#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
docker compose --profile tools run --rm verify
# A unique project owns only disposable acceptance data. The normal demo volume is untouched.
test_project="oceanai-verify-$$"
cleanup() {
    docker compose -p "$test_project" -f compose.yaml -f compose.verify.yaml down --volumes --remove-orphans
}
trap cleanup EXIT
trap 'exit 130' INT TERM
docker compose -p "$test_project" -f compose.yaml -f compose.verify.yaml run --build --rm acceptance
