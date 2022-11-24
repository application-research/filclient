SHELL=/usr/bin/env bash

GO_BUILD_IMAGE?=golang:1.18
COMMIT := $(shell git rev-parse --short=8 HEAD)

# GITVERSION is the nearest tag plus number of commits and short form of most recent commit since the tag, if any
GITVERSION=$(shell git describe --always --tag --dirty)

unexport GOFLAGS

CLEAN:=
BINS:=

GOFLAGS:=

.PHONY: all
all: build

## FFI

FFI_PATH:=extern/filecoin-ffi/
FFI_DEPS:=.install-filcrypto
FFI_DEPS:=$(addprefix $(FFI_PATH),$(FFI_DEPS))

$(FFI_DEPS): build/.filecoin-install ;

build/.filecoin-install: $(FFI_PATH)
	@mkdir -p build
	$(MAKE) -C $(FFI_PATH) $(FFI_DEPS:$(FFI_PATH)%=%)
	@touch $@

MODULES+=$(FFI_PATH)
BUILD_DEPS+=build/.filecoin-install
CLEAN+=build/.filecoin-install

ffi-version-check:
	@[[ "$$(awk '/const Version/{print $$5}' extern/filecoin-ffi/version.go)" -eq 3 ]] || (echo "FFI version mismatch, update submodules"; exit 1)
BUILD_DEPS+=ffi-version-check

.PHONY: ffi-version-check

$(MODULES): build/.update-modules ;
# dummy file that marks the last time modules were updated
build/.update-modules:
	git submodule update --init --recursive
	@mkdir -p build
	touch $@

CLEAN+=build/.update-modules

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
