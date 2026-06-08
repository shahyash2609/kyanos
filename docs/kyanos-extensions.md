# Kyanos Extensions — Feature & Architecture Reference

This document covers all the features and architectural changes added on the `feat/grpc` branch.

---

## Table of Contents

1. [Feature Summary](#feature-summary)
2. [HTTP Enhancements](#http-enhancements)
3. [gRPC Capture & Reflection — Architecture](#grpc-capture--reflection--architecture)
4. [Per-Pod Classification & Output](#per-pod-classification--output)
5. [GCS Upload](#gcs-upload)
6. [Header Regex Filtering](#header-regex-filtering)
7. [Build & Deploy](#build--deploy)
8. [CLI Reference](#cli-reference)

---

## Feature Summary

| Feature | Flag(s) | Status |
|---------|---------|--------|
| HTTP gzip/deflate decompression | (automatic) | Done |
| HTTP header-based filtering (exact) | `--header` | Done |
| HTTP header-based filtering (regex) | `--header-regex` | Done |
| HTTP/2 protocol detection | (automatic) | Done |
| gRPC capture (HTTP/2 framing + HPACK) | `watch grpc` | Done |
| gRPC reflection decode (single server) | `--reflect host:port` | Done |
| gRPC auto-reflect (all pods on node) | `--auto-reflect` | Done |
| CRI-based pod endpoint discovery | (used by `--auto-reflect`) | Done |
| Per-pod JSON output (local files) | `--json-output-dir` | Done |
| GCS rolling-file upload | `--gcs-bucket` | Done |
| GCS per-pod buffered upload | `--gcs-bucket` + `--json-output-dir` | Done |
| Runtime PID-to-pod classification | (automatic when containerd available) | Done |
| PID namespace fallback for child processes | (automatic) | Done |
| Multi-container-name matching | `--container-name` | Done |
| Docker-based cross-compilation (macOS → Linux) | `make docker-build` | Done |

---

## HTTP Enhancements

### Response Decompression

Responses with `Content-Encoding: gzip` or `Content-Encoding: deflate` are automatically decompressed before display/output. Request bodies with `Content-Encoding: gzip` are also handled.

**Files:** `agent/protocol/http.go`

### Header Filtering

Two modes of header filtering, applied in `submitRecord()` (before render/output):

- **Exact match:** `--header 'Key: Value'` — header must have this exact value
- **Regex match:** `--header-regex 'Key:pattern'` — header value must match the regex

Multiple flags combine with AND logic. Available on both `watch http` and `watch grpc`.

**Example:** Filter only sampled traces:
```bash
kyanos watch http --header-regex 'Traceparent:.*-01$' --side server
```

---

## gRPC Capture & Reflection — Architecture

### Overview

```
  ┌─────────────────────────────────────────────────────────────────────┐
  │                        Kernel (eBPF)                                │
  │  syscall tracepoints capture raw TCP read/write data per-connection │
  └────────────────────────────┬────────────────────────────────────────┘
                               │ raw bytes
                               ▼
  ┌─────────────────────────────────────────────────────────────────────┐
  │                   Protocol Detection Layer                          │
  │  Detects HTTP/2 via connection preface "PRI * HTTP/2.0\r\n\r\nSM"  │
  │  Also detects on pre-existing connections via frame analysis        │
  └────────────────────────────┬────────────────────────────────────────┘
                               │ HTTP/2 frames
                               ▼
  ┌─────────────────────────────────────────────────────────────────────┐
  │                     gRPC Parser (parser.go)                         │
  │                                                                     │
  │  1. HTTP/2 Frame Parsing                                            │
  │     - HEADERS frames → HPACK decode (separate context per direction)│
  │     - DATA frames → accumulate body per stream ID                   │
  │     - Tracks stream lifecycle (HEADERS → DATA → END_STREAM)         │
  │                                                                     │
  │  2. Header Extraction                                               │
  │     - :method, :path, :authority, content-type, grpc-encoding       │
  │     - All headers stored in map for filter matching                 │
  │                                                                     │
  │  3. gRPC Frame Processing                                           │
  │     - Strips 5-byte length-prefixed frame header                    │
  │     - Decompresses gzip/deflate if grpc-encoding set                │
  │                                                                     │
  │  4. Protobuf Reflection Decode (if resolver available)              │
  │     - Looks up method descriptor by :path                           │
  │     - Decodes binary protobuf → human-readable JSON-like text       │
  │     - Falls back to tryAllMethods() for mid-stream captures         │
  └────────────────────────────┬────────────────────────────────────────┘
                               │ ParsedGrpcRequest / ParsedGrpcResponse
                               ▼
  ┌─────────────────────────────────────────────────────────────────────┐
  │                    Filter Pipeline (submitRecord)                    │
  │  Protocol filter → Latency filter → Size filter → Message filter   │
  │  (header exact match + header regex match applied here)             │
  └────────────────────────────┬────────────────────────────────────────┘
                               │ AnnotatedRecord (with ConnDesc.PodName)
                               ▼
  ┌─────────────────────────────────────────────────────────────────────┐
  │                      Output / Render Layer                          │
  │  TUI │ JSON file │ Per-pod JSONL │ GCS upload │ GCS per-pod upload  │
  └─────────────────────────────────────────────────────────────────────┘
```

### HPACK Decoding

HTTP/2 uses HPACK (RFC 7541) for header compression. The parser maintains **separate decoder contexts** for request and response directions, since HPACK is stateful (dynamic table per direction).

**Files:** `agent/protocol/grpc/parser.go` — `reqHpackDecoder` / `respHpackDecoder`

### Reflection Resolver

Each `ReflectionResolver` connects to a single gRPC server and fetches all proto descriptors via the gRPC Server Reflection API.

```
ReflectionResolver.Resolve()
  │
  ├─ 1. grpc.Dial(target, insecure, 10s timeout)
  │
  ├─ 2. ServerReflectionInfo() → bidirectional stream
  │
  ├─ 3. ListServices → ["pkg.ServiceA", "pkg.ServiceB", ...]
  │
  ├─ 4. For each service:
  │     FileContainingSymbol(service) → FileDescriptorProto bytes
  │     + recursively fetch all dependencies
  │
  ├─ 5. protodesc.NewFiles(descriptorSet) → protoregistry.Files
  │
  └─ 6. Index all methods:
        methods["/pkg.ServiceA/MethodX"] = {InputType, OutputType}
```

**Decoding:** Given a `:path` like `/pkg.ServiceA/MethodX`, looks up the method descriptor and uses `protowire` to decode the binary protobuf body into a human-readable field map.

**Fallback (tryAllMethods):** When `:path` is unknown (mid-stream capture), tries decoding with every known method's descriptor, scores by field count, picks the best match.

**Files:** `agent/protocol/grpc/reflection.go`, `agent/protocol/grpc/protodecode.go`

### Reflection Registry (Per-Pod Routing)

When `--auto-reflect` is used, multiple gRPC servers may exist on a node. The `ReflectionRegistry` maps authority → resolver:

```
ReflectionRegistry
  │
  ├─ map[":8080"] → ReflectionResolver (pod A)
  ├─ map[":9090"] → ReflectionResolver (pod B)
  └─ map[":50051"] → ReflectionResolver (pod C)

Lookup strategy:
  1. Exact match on "host:port" from :authority header
  2. Fallback to ":port" (port-only) match
  3. GetSole() — if exactly one resolver, use it for all
     (handles pre-existing connections where :authority was missed)
```

**Files:** `agent/protocol/grpc/reflection.go` (ReflectionRegistry)

### Auto-Reflect: Pod Endpoint Discovery

`--auto-reflect` discovers all listening gRPC servers on the node:

```
initAutoReflect()
  │
  ├─ Try CRI-based discovery (preferred):
  │   │
  │   ├─ 1. CRI ListPodSandbox → map[ns/name] → podIP
  │   │
  │   ├─ 2. Containerd ListContainers → map[ns/name] → rootPID
  │   │
  │   └─ 3. For each pod with IP + PID:
  │         Read /proc/{pid}/net/tcp → listening ports
  │         → PodEndpoint{IP: "10.0.1.5", Port: 8080}
  │
  ├─ Fallback: Host-level port scan
  │   Read /proc/1/net/tcp → all listening ports > 1024
  │   → endpoints on 127.0.0.1
  │
  └─ For each endpoint:
      resolver = NewReflectionResolver(ip:port)
      resolver.Resolve() → fetch proto descriptors
      if OK → registry.Register(":port", resolver)
```

**Files:** `agent/metadata/node_scanner.go`, `cmd/grpc.go`

### Pre-Existing Connection Handling

When kyanos attaches to an already-running system, some TCP connections are mid-stream. The initial HEADERS frame (with `:authority`) was sent before capture started.

**Solution:** `GetSole()` — if exactly one resolver is registered, it is used as default for connections with no known authority. This is the common case when monitoring a single-service node.

---

## Per-Pod Classification & Output

### PID-to-Pod Resolution

All captured traffic is classified by pod at analysis time, not at BPF level. This means:
- **No BPF filter needed** — capture everything, classify later
- **New pods** are automatically picked up via the live ContainerCache (containerd event watcher)
- **Classification** happens per-record in `stat.go` before the record reaches the output layer

```
Record arrives with PID (from TgidFd)
  │
  ├─ 1. ContainerCache.GetByPid(pid)
  │     Matches against container RootPid
  │
  ├─ 2. If no match (child/worker process):
  │     Read /proc/{pid}/ns/pid → pidNamespace
  │     ContainerCache.GetByPidNs(pidNamespace)
  │     Matches against container's PidNamespace
  │
  └─ 3. If container found:
        container.Pod() → reads k8s labels:
          io.kubernetes.pod.name → pod name
          io.kubernetes.pod.namespace → namespace
        ConnDesc.PodName = "{name}.{namespace}"
```

**Container Cache:** Initialized from containerd at startup, then watches events (`ContainerCreate`, `TaskStart`, `TaskCreate`) for new containers. This handles pods spinning up mid-capture.

**Files:**
- `agent/analysis/stat.go` — PID lookup + PID namespace fallback
- `agent/metadata/container.go` — ContainerCache wrapper (GetByPid, GetByPidNs)
- `agent/metadata/container/containerd/containerd.go` — containerd event watcher
- `bpf/loader/loader.go` — always-on ContainerCache initialization
- `common/namspace.go` — GetPidNamespaceFromPid helper

### Per-Pod JSONL Output

With `--json-output-dir /data/output`, each pod gets its own file:

```
/data/output/
  ├── env15066-edge-proxy-valmo-app-7c8dfc4997-lznzg.env15066.jsonl
  ├── env2-stg-edge-proxy-d798ff949-7pkzz.env2.jsonl
  ├── env8-gateway-service-56f6b88c9c-qp5j5.env8.jsonl
  └── unknown.jsonl   (traffic from unresolved PIDs)
```

Each line is a JSON object with `pod_name`, timing, request/response, etc.

---

## GCS Upload

### Mode 1: Single Rolling File (`--gcs-bucket` only)

```
GCSUploader
  │
  ├─ Writes all records to a single local temp file
  ├─ Every --gcs-upload-interval (default 3m):
  │   Close file → Upload to GCS → Open new file
  └─ GCS path: gs://bucket/{service}/{deployment}/primary/{date}/{ts}.jsonl
```

### Mode 2: Per-Pod Buffered Upload (`--gcs-bucket` + `--json-output-dir`)

```
GCSDirUploader
  │
  ├─ Per-pod local buffer files:
  │   /data/buffer/env15066-edge-proxy-...-1776423793.jsonl
  │
  ├─ Rotation triggers:
  │   - Size threshold: --gcs-buffer-size (default 10MB)
  │   - Time interval: --gcs-upload-interval (default 3m)
  │
  ├─ Rotation strategy (zero-downtime):
  │   1. Create new file
  │   2. Lock → swap file pointer → unlock
  │   3. Upload old file to GCS (non-blocking)
  │   4. Delete local file after upload
  │
  └─ GCS path: gs://bucket/{service}/{pod-name}/{date}/{ts}.jsonl
```

**Authentication:** Uses Application Default Credentials (ADC) by default. On GKE, this is the node's service account (note: `hostNetwork: true` bypasses Workload Identity). Alternatively, `--gcs-credentials` can point to a service account JSON key file.

**Files:** `agent/render/watch/gcs_uploader.go`, `agent/render/watch/gcs_dir_uploader.go`

---

## Header Regex Filtering

### Usage

```bash
# Only capture requests where traceparent ends with -01 (sampled)
kyanos watch http --header-regex 'Traceparent:.*-01$' --side server

# Combine exact + regex
kyanos watch http --header 'Content-Type: application/json' \
                  --header-regex 'X-Request-Id:^[a-f0-9]{32}$'

# Works on gRPC too
kyanos watch grpc --header-regex 'x-custom-header:foo.*bar'
```

### Pipeline Position

Filtering happens in `submitRecord()` in `agent/conn/record_processor.go`, which is **before** the record reaches the output/render layer. This means filtered-out records never get serialized to JSON, written to files, or uploaded to GCS — reducing CPU and I/O load.

```
BPF events → Protocol parse → submitRecord() → [FILTER HERE] → Output
                                    │
                                    ├─ Protocol filter
                                    ├─ Latency filter
                                    ├─ Size filter
                                    ├─ Header exact match
                                    └─ Header regex match ← NEW
```

**Files:** `agent/protocol/http.go` (HttpFilter), `agent/protocol/grpc/filter.go` (GrpcFilter), `cmd/http.go`, `cmd/grpc.go`

---

## Build & Deploy

### Docker-Based Cross-Compilation

For building a Linux amd64 binary from macOS:

```bash
make docker-build
# Output: ./kyanos (ELF 64-bit, statically linked)
```

This runs a three-step process inside Docker:
1. `make build-bpf` — compile eBPF C programs with clang
2. `go generate` — generate Go bindings
3. `go build -tags=static` — statically link the binary

### Debug Pod Deployment

A privileged debug pod manifest is provided for GKE nodes:

```bash
kubectl apply -f deploy/debug-pod-9949.yaml
kubectl cp ./kyanos kyanos/kyanos-debug-9949:/usr/local/bin/kyanos -c debug
kubectl exec -n kyanos kyanos-debug-9949 -c debug -- \
  /usr/local/bin/kyanos watch http \
    --side server \
    --gcs-bucket gcs-cntr-xcntr-ebpf-kyanos-stg \
    --gcs-service-name stg \
    --json-output-dir /data/kyanos-buffer
```

Required pod capabilities: `hostPID`, `hostNetwork`, `privileged`, mounts for `/sys/kernel`, `/sys/fs/bpf`, `/proc`, `/run/containerd`.

---

## CLI Reference

### Global Flags (on `watch` / `stat`)

| Flag | Description | Default |
|------|-------------|---------|
| `--side server\|client\|all` | Filter by connection side | `all` |
| `--json-output-dir <dir>` | Per-pod JSONL output directory | — |
| `--gcs-bucket <bucket>` | GCS bucket for rolling upload | — |
| `--gcs-service-name <name>` | Service name in GCS path | — |
| `--gcs-deployment-id <id>` | Deployment ID in GCS path | — |
| `--gcs-upload-interval <dur>` | Rolling upload interval | `3m` |
| `--gcs-buffer-size <bytes>` | Per-pod buffer rotation size | `10MB` |
| `--gcs-credentials <path>` | SA JSON key file | ADC |

### HTTP Subcommand (`watch http`)

| Flag | Description |
|------|-------------|
| `--method GET,POST` | Filter by HTTP method |
| `--path /foo/bar` | Exact path match |
| `--path-regex '/api/v\d+'` | Regex path match |
| `--path-prefix /api` | Path prefix match |
| `--host example.com` | Host header match |
| `--header 'Key: Value'` | Exact header value match (repeatable) |
| `--header-regex 'Key:pattern'` | Regex header value match (repeatable) |

### gRPC Subcommand (`watch grpc`)

| Flag | Description |
|------|-------------|
| `--reflect host:port` | Single reflection server for protobuf decode |
| `--auto-reflect` | Auto-discover all pods' gRPC endpoints on this node |
| `--header 'Key: Value'` | Exact header value match (repeatable) |
| `--header-regex 'Key:pattern'` | Regex header value match (repeatable) |
| `--path`, `--path-regex`, etc. | Same as HTTP |
