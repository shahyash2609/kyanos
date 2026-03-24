V:=1
OUTPUT := .output

# Docker-based local build (works on macOS without Linux toolchain installed)
DOCKER_IMAGE    ?= kyanos-builder
DOCKER_GO_CACHE ?= kyanos-go-cache
HOST_UID        := $(shell id -u)
HOST_GID        := $(shell id -g)
CLANG ?= clang
LIBBPF_SRC := $(abspath ./libbpf/src)
BPFTOOL_SRC := $(abspath ./bpftool/src)
BPFTOOL_OUTPUT ?= $(abspath $(OUTPUT)/bpftool)
BPFTOOL ?= $(BPFTOOL_OUTPUT)/bootstrap/bpftool
LIBBPF_OBJ := $(abspath $(OUTPUT)/libbpf.a)
VMLINUX := ./vmlinux/$(ARCH)/vmlinux.h
INCLUDES := -I$(OUTPUT) -I./libbpf/include/uapi -I$(dir $(VMLINUX))
ARCH ?= $(shell uname -m | sed 's/x86_64/x86/' \
			 | sed 's/arm.*/arm/' \
			 | sed 's/aarch64/arm64/' \
			 | sed 's/ppc64le/powerpc/' \
			 | sed 's/mips.*/mips/' \
			 | sed 's/riscv64/riscv/' \
			 | sed 's/loongarch64/loongarch/')

CLANG_BPF_SYS_INCLUDES ?= $(shell $(CLANG) -v -E - </dev/null 2>&1 \
	| sed -n '/<...> search starts here:/,/End of search list./{ s| \(/.*\)|-idirafter \1|p }')
APPS = kyanos
CFLAGS := -O2 -Wall 
ALL_LDFLAGS := $(LDFLAGS) $(EXTRA_LDFLAGS)

ifeq ($(V),1)
	Q =
	msg =
else
	Q = @
	msg = @printf '  %-8s %s%s\n'					\
		      "$(1)"						\
		      "$(patsubst $(abspath $(OUTPUT))/%,%,$(2))"	\
		      "$(if $(3), $(3))";
	MAKEFLAGS += --no-print-directory
endif

$(call allow-override,CC,$(CROSS_COMPILE)cc)
$(call allow-override,LD,$(CROSS_COMPILE)ld)

.PHONY: all
all: $(APPS)

# Convenience target: compile BPF objects then build the Go binary in one step.
# Usage: make build                    (amd64 host, no BTF archive)
#        BUILD_ARCH=x86_64 ARCH_BPF_NAME=x86 make build   (with BTF archive)
.PHONY: build
build:
	$(MAKE) build-bpf
	$(if $(BUILD_ARCH),$(MAKE) btfgen BUILD_ARCH=$(BUILD_ARCH) ARCH_BPF_NAME=$(ARCH_BPF_NAME))
	$(MAKE) kyanos

# Build inside a Linux Docker container — works on macOS without any Linux toolchain installed.
# The kyanos binary is written to the current directory and owned by the calling user.
# Usage: make docker-build
.PHONY: docker-build
docker-build:
	@if [ ! -f libbpf/src/Makefile ]; then \
		echo "Initializing git submodules (needed for libbpf build inside container)..."; \
		git submodule update --init --recursive; \
	fi
	docker build --platform linux/amd64 -f Dockerfile.build -t $(DOCKER_IMAGE) .
	docker run --rm \
		--platform linux/amd64 \
		-v "$(CURDIR)":/workspace \
		-v "$(DOCKER_GO_CACHE)":/root/go \
		-w /workspace \
		$(DOCKER_IMAGE) \
		bash -c "make clean && make build && chown $(HOST_UID):$(HOST_GID) kyanos"
	@echo "=> ./kyanos is ready"

clean:
	$(call msg,CLEAN)
	$(Q)rm -rf $(OUTPUT) $(APPS) kyanos kyanos.log

$(OUTPUT) $(OUTPUT)/libbpf $(BPFTOOL_OUTPUT):
	$(call msg,MKDIR,$@)
	$(Q)mkdir -p $@

# Build libbpf
$(LIBBPF_OBJ): $(wildcard $(LIBBPF_SRC)/*.[ch] $(LIBBPF_SRC)/Makefile) | $(OUTPUT)/libbpf
	$(call msg,LIB,$@)
	$(Q)$(MAKE) -C $(LIBBPF_SRC) BUILD_STATIC_ONLY=1		      \
		    OBJDIR=$(dir $@)/libbpf DESTDIR=$(dir $@)		      \
		    INCLUDEDIR= LIBDIR= UAPIDIR=			      \
		    install

# Build bpftool
$(BPFTOOL): | $(BPFTOOL_OUTPUT)
	$(call msg,BPFTOOL,$@)
	$(Q)$(MAKE) ARCH= CROSS_COMPILE= OUTPUT=$(BPFTOOL_OUTPUT)/ -C $(BPFTOOL_SRC) bootstrap

GO_FILES := $(shell find $(SRC_DIR) -type f -name '*.go' | sort)  

.PHONY: build-bpf
build-bpf: $(LIBBPF_OBJ) $(wildcard bpf/*.[ch]) | $(OUTPUT)
	TARGET=amd64 go generate ./bpf/
	TARGET=arm64 go generate ./bpf/

kyanos: $(GO_FILES)
	$(call msg,BINARY,$@)
	CGO_ENABLED=1 go build -tags=static -ldflags="-linkmode external -extldflags '-static'" -o kyanos

.PHONY: kyanos-compress
kyanos-compress: $(GO_FILES)
	$(call msg,BINARY,$@)
	CGO_ENABLED=1 go build -tags=static -ldflags="-linkmode external -extldflags '-static'" -o kyanos && upx -9 kyanos


.PHONY: btfgen
btfgen:
	./bpf/btfgen.sh $(BUILD_ARCH) $(ARCH_BPF_NAME)

# delete failed targets
.DELETE_ON_ERROR:

# keep intermediate (.skel.h, .bpf.o, etc) targets
.SECONDARY:

.PHONY: test
test: test-go

.PHONY: test-go
test-go:
	go test -v ./...

.PHONY: format
format: format-go

.PHONY: format-go
format-go:
	goimports -w .
	gofmt -s -w .

.PHONY: format-md
format-md:
	find . -type f -name "*.md" | xargs npx prettier --write
	find docs/cn -type f -name "*.md" | xargs npx md-padding -i
	find . -type f -name "*_CN.md" | xargs npx md-padding -i

.PHONY: dlv
dlv:
	chmod +x kyanos && dlv --headless --listen=:2345 --api-version=2 --check-go-version=false exec ./kyanos 

.PHONY: kyanos-debug
kyanos-debug: $(GO_FILES)
	$(call msg,BINARY,$@)
	CGO_ENABLED=1 go build -tags=static -ldflags="-linkmode external -extldflags '-static'" -gcflags "all=-N -l" -o kyanos

.PHONY: remote-debug
remote-debug: build-bpf kyanos-debug dlv
