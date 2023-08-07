SHELL := bash

IMAGE_TAG = localshow-build

USER_ID=$(shell ((docker --version | grep -q podman) && echo "0" || id -u))
USER_GROUP=$(shell ((docker --version | grep -q podman) && echo "0" || id -g))
ROOTDIR=$(dir $(abspath $(lastword $(MAKEFILE_LIST))))
GOPATH ?= $(shell go env GOPATH)
VERSION ?= $(shell git describe --tags --match='v[0-9]*' --dirty --always)
GO ?= go


default: build

.PHONY : build-static build
build-static:
	@echo Building localshow
	docker build --tag $(IMAGE_TAG) -f Dockerfile.build-static .
	docker run --rm -e USER_ID=$(USER_ID) -e USER_GROUP=$(USER_GROUP) -v $(PWD):/build/localshow:z $(IMAGE_TAG) /build-static.sh
	@echo Binaries are available in $(PWD)/bin

build:
	@echo Building localshow ${VERSION}
	$(shell mkdir -p ./bin)
	@$(GO) build -ldflags "-s -w -X github.com/gabriel-samfira/localshow/cmd/localshowd/cmd.Version=${VERSION}" -tags osusergo,netgo -o bin/localshow ./cmd/localshowd
	@echo Binaries are available in $(PWD)/bin


