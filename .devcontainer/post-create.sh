#!/bin/bash

set -e

corepack enable
yarn install --frozen-lockfile
mkdir -p .go-tmp .go-buildcache .go-modcache bin
GOTMPDIR="$PWD/.go-tmp" GOCACHE="$PWD/.go-buildcache" GOMODCACHE="$PWD/.go-modcache" go build -mod=mod -o bin/paon ./cmd/paon
yarn build:development
