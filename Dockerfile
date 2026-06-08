# Stage 1: Build (reuses the existing Dockerfile.build toolchain)
FROM ubuntu:22.04 AS builder
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential pkgconf \
    libelf-dev zlib1g-dev libssl-dev \
    llvm-12 clang-12 \
    gcc-aarch64-linux-gnu \
    flex bison bc rsync wget git \
    && rm -rf /var/lib/apt/lists/*

RUN ln -sf /usr/bin/clang-12      /usr/bin/clang      \
 && ln -sf /usr/bin/llc-12        /usr/bin/llc        \
 && ln -sf /usr/bin/llvm-strip-12 /usr/bin/llvm-strip

RUN wget -q https://go.dev/dl/go1.23.3.linux-amd64.tar.gz \
 && tar -C /usr/local -xzf go1.23.3.linux-amd64.tar.gz \
 && rm go1.23.3.linux-amd64.tar.gz

ENV PATH=/usr/local/go/bin:$PATH \
    GOPATH=/root/go \
    GOCACHE=/root/go/cache

WORKDIR /workspace
COPY . .
RUN git submodule update --init --recursive 2>/dev/null || true
RUN make clean && make build

# Stage 2: Minimal runtime image
FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /workspace/kyanos /usr/local/bin/kyanos
ENTRYPOINT ["kyanos"]
