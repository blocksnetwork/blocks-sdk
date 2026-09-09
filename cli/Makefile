ifneq (,$(wildcard .env))
    include .env
    export
endif

# Each override below is guarded by ifdef, which is false for a variable that
# is defined but empty. An empty value therefore drops the ldflag and keeps the
# compiled default rather than overwriting it with an empty string.
LDFLAGS := -X github.com/pubnub/blocks-sdk/cli/cmd.Version=dev
ifdef BLOCKS_BACKEND_URL
LDFLAGS += -X github.com/pubnub/blocks-sdk/cli/cmd.defaultBackendURL=$(BLOCKS_BACKEND_URL)
endif
ifdef BLOCKS_CLI_CLIENT_ID
LDFLAGS += -X github.com/pubnub/blocks-sdk/cli/cmd.defaultCliClientID=$(BLOCKS_CLI_CLIENT_ID)
endif
ifdef BLOCKS_INSTANCE_DOMAIN
LDFLAGS += -X github.com/pubnub/blocks-sdk/cli/cmd.defaultInstanceDomain=$(BLOCKS_INSTANCE_DOMAIN)
endif

.PHONY: build install run clean

build:
	go build -ldflags "$(LDFLAGS)" -o blocks .
ifeq ($(shell uname),Darwin)
	codesign -s - -f blocks
endif

install: build
	mkdir -p ~/.blocks/bin
	cp blocks ~/.blocks/bin/blocks
ifeq ($(shell uname),Darwin)
	codesign -s - -f ~/.blocks/bin/blocks
endif

run:
	go run -ldflags "$(LDFLAGS)" . $(ARGS)

clean:
	rm -f blocks
