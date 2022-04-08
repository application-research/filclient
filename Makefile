SHELL=/usr/bin/env bash

GO_BUILD_IMAGE?=golang:1.16
COMMIT := $(shell git rev-parse --short=8 HEAD)

# GITVERSION is the nearest tag plus number of commits and short form of most recent commit since the tag, if any
GITVERSION=$(shell git describe --always --tag --dirty)

unexport GOFLAGS

CLEAN:=
BINS:=

GOFLAGS:=

.PHONY: all
all: build

# Once filclient has it's own version cmd add this back in
#ldflags=-X=github.com/application-research/filclient/version.GitVersion=$(GITVERSION)
#ifneq ($(strip $(LDFLAGS)),)
#	ldflags+=-extldflags=$(LDFLAGS)
#endif
#GOFLAGS+=-ldflags="$(ldflags)"

.PHONY: build
build: deps filclient

.PHONY: deps
deps: $(BUILD_DEPS)

.PHONY: filclient
filclient:
	go build

.PHONY: filc
filc: filclient
	make -C filc

.PHONY: clean
clean:
	rm -rf $(CLEAN) $(BINS)
	make -C filc clean

.PHONY: dist-clean
dist-clean:
	git clean -xdff
	git submodule deinit --all -f
