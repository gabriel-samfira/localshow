#!/bin/sh

LOCALSHOW_SOURCE="/build/localshow"
BIN_DIR="$LOCALSHOW_SOURCE/bin"
git config --global --add safe.directory "$LOCALSHOW_SOURCE"

[ ! -d "$BIN_DIR" ] && mkdir -p "$BIN_DIR"

export CGO_ENABLED=1
USER_ID=${USER_ID:-$UID}
USER_GROUP=${USER_GROUP:-$(id -g)}

mkdir -p $BIN_DIR/amd64 $BIN_DIR/arm64
cd $LOCALSHOW_SOURCE/cmd/localshowd
go build -mod vendor \
    -o $BIN_DIR/amd64/localshowd \
    -tags osusergo,netgo \
    -ldflags "-linkmode external -extldflags '-static' -s -w -X github.com/gabriel-samfira/localshow/cmd/localshowd/cmd.Version=$(git describe --tags --match='v[0-9]*' --dirty --always)" .
CC=aarch64-linux-musl-gcc GOARCH=arm64 go build \
    -mod vendor \
    -o $BIN_DIR/arm64/localshowd \
    -tags osusergo,netgo \
    -ldflags "-linkmode external -extldflags '-static' -s -w -X github.com/gabriel-samfira/localshow/cmd/localshowd/cmd.Version=$(git describe --tags --match='v[0-9]*' --dirty --always)" .
GOOS=windows CC=x86_64-w64-mingw32-cc go build -mod vendor \
    -o $BIN_DIR/amd64/localshowd.exe \
    -tags osusergo,netgo \
    -ldflags "-s -w -X github.com/gabriel-samfira/localshow/cmd/localshowd/cmd.Version=$(git describe --tags --match='v[0-9]*' --dirty --always)" .


chown $USER_ID:$USER_GROUP -R "$BIN_DIR"
