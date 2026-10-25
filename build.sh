#!/bin/sh
# Builds the bridge. The goolm tag uses the pure Go Olm implementation, so libolm isn't needed.
set -e
MAUTRIX_VERSION=$(grep 'maunium.net/go/mautrix ' go.mod | awk '{ print $2 }')
GO_LDFLAGS="-s -w -X main.Tag=$(git describe --exact-match --tags 2>/dev/null || echo unknown) \
 -X main.Commit=$(git rev-parse HEAD 2>/dev/null || echo unknown) \
 -X 'main.BuildTime=$(date -Iseconds)' \
 -X 'maunium.net/go/mautrix.GoModVersion=$MAUTRIX_VERSION'"
go build -tags goolm -ldflags="$GO_LDFLAGS" -o matrix-fchat ./cmd/matrix-fchat "$@"
