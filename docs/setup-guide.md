# Kyanos Debug Pod Setup Guide

Deploy kyanos on a GKE node to capture HTTP + gRPC traffic per-pod and upload to GCS.

---

## Architecture

```
Node
├── Pod: kyanos-capture (hostPID + hostNetwork + privileged)
│   └── Runs kyanos → writes per-pod .json files to hostPath /var/log/kyanos/
│
├── Pod: kyanos-collector (pod network, Workload Identity SA)
│   └── Reads .json files → gzip compresses → uploads to GCS every 60s
│
└── Shared: hostPath /var/log/kyanos/ (DirectoryOrCreate)
```

GCS output structure:
```
gs://<bucket>/<prefix>/<pod-name>/<YYYY-MM-DD>/<timestamp>.json.gz
```

---

## Prerequisites

1. **kyanos binary** — Linux amd64 static binary (built from this repo with `make docker-build`)
2. **GCS bucket** — e.g., `gcs-cntr-xcntr-ebpf-kyanos-stg`
3. **Kubernetes SA** with GCS write access — e.g., `gcs-kyanos-ksa` in the `kyanos` namespace, bound via Workload Identity
4. **Target node name** — get it with:
   ```bash
   kubectl get nodes -o wide
   # or find the node running a specific pod:
   kubectl get pod <pod-name> -n <namespace> -o wide
   ```

---

## Step 1: Create the manifest

Save the following as `kyanos-deploy.yaml`. Replace:
- `NODE_NAME` — your target node hostname
- `GCS_BUCKET` — your GCS bucket name
- `GCS_PREFIX` — path prefix (e.g., `stg`, `prd`)

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: kyanos
---
apiVersion: v1
kind: Pod
metadata:
  name: kyanos-capture
  namespace: kyanos
  labels:
    app: kyanos
    component: capture
spec:
  serviceAccountName: gcs-kyanos-ksa
  hostPID: true
  hostNetwork: true
  nodeSelector:
    kubernetes.io/hostname: NODE_NAME
  tolerations:
    - operator: Exists
  containers:
    - name: debug
      image: ubuntu:22.04
      command: ["sleep", "infinity"]
      securityContext:
        privileged: true
      volumeMounts:
        - name: sys-kernel
          mountPath: /sys/kernel
        - name: sys-fs-bpf
          mountPath: /sys/fs/bpf
        - name: proc-host
          mountPath: /host/proc
          readOnly: true
        - name: run-containerd
          mountPath: /run/containerd
          readOnly: true
        - name: kyanos-data
          mountPath: /data/kyanos
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
        limits:
          cpu: "1"
          memory: 1Gi
  volumes:
    - name: sys-kernel
      hostPath:
        path: /sys/kernel
    - name: sys-fs-bpf
      hostPath:
        path: /sys/fs/bpf
    - name: proc-host
      hostPath:
        path: /proc
    - name: run-containerd
      hostPath:
        path: /run/containerd
    - name: kyanos-data
      hostPath:
        path: /var/log/kyanos
        type: DirectoryOrCreate
---
apiVersion: v1
kind: Pod
metadata:
  name: kyanos-collector
  namespace: kyanos
  labels:
    app: kyanos
    component: collector
spec:
  serviceAccountName: gcs-kyanos-ksa
  nodeSelector:
    kubernetes.io/hostname: NODE_NAME
  tolerations:
    - operator: Exists
  containers:
    - name: collector
      image: google/cloud-sdk:slim
      command: ["/bin/bash", "-c"]
      args:
        - |
          set -euo pipefail

          GCS_BUCKET="GCS_BUCKET_NAME"
          GCS_PREFIX="GCS_PREFIX_VALUE"
          DATA_DIR="/data/kyanos"
          UPLOAD_INTERVAL=60
          COMPRESS="true"
          STAGING_DIR="${DATA_DIR}/.staging"

          echo "[collector] bucket=${GCS_BUCKET} prefix=${GCS_PREFIX} compress=${COMPRESS}"
          mkdir -p "${DATA_DIR}" "${STAGING_DIR}"

          while true; do
            find "${DATA_DIR}" -maxdepth 1 -name '*.json' -type f | while read -r filepath; do
              filename=$(basename "${filepath}")
              filesize=$(stat -c%s "${filepath}" 2>/dev/null || echo 0)
              [ "${filesize}" -lt 1 ] && continue

              staging_path="${STAGING_DIR}/${filename}"
              cp "${filepath}" "${staging_path}"
              truncate -s 0 "${filepath}"

              pod_name="${filename%.json}"
              date_prefix=$(date -u +%Y-%m-%d)
              ts=$(date -u +%Y-%m-%dT%H-%M-%SZ)

              if [ "${COMPRESS}" = "true" ]; then
                gzip "${staging_path}"
                staging_path="${staging_path}.gz"
                gcs_path="gs://${GCS_BUCKET}/${GCS_PREFIX}/${pod_name}/${date_prefix}/${ts}.json.gz"
              else
                gcs_path="gs://${GCS_BUCKET}/${GCS_PREFIX}/${pod_name}/${date_prefix}/${ts}.json"
              fi

              upload_size=$(stat -c%s "${staging_path}" 2>/dev/null || echo 0)
              echo "[collector] uploading (${filesize}B -> ${upload_size}B) -> ${gcs_path}"
              if gsutil -q cp "${staging_path}" "${gcs_path}"; then
                rm -f "${staging_path}"
                echo "[collector] done: ${pod_name}"
              else
                echo "[collector] FAILED: ${staging_path} (kept for retry)"
              fi
            done

            find "${STAGING_DIR}" \( -name '*.json' -o -name '*.json.gz' \) -type f 2>/dev/null | while read -r leftover; do
              filename=$(basename "${leftover}")
              pod_name="${filename%.gz}"; pod_name="${pod_name%.json}"
              date_prefix=$(date -u +%Y-%m-%d)
              ts=$(date -u +%Y-%m-%dT%H-%M-%SZ)
              ext="${filename#"${pod_name}"}"
              gcs_path="gs://${GCS_BUCKET}/${GCS_PREFIX}/${pod_name}/${date_prefix}/${ts}-retry${ext}"
              echo "[collector] retrying ${leftover} -> ${gcs_path}"
              if gsutil -q cp "${leftover}" "${gcs_path}"; then
                rm -f "${leftover}"
              fi
            done

            sleep "${UPLOAD_INTERVAL}"
          done
      volumeMounts:
        - name: kyanos-data
          mountPath: /data/kyanos
      resources:
        requests:
          cpu: 50m
          memory: 64Mi
        limits:
          cpu: 200m
          memory: 256Mi
  volumes:
    - name: kyanos-data
      hostPath:
        path: /var/log/kyanos
        type: DirectoryOrCreate
```

---

## Step 2: Deploy

```bash
kubectl apply -f kyanos-deploy.yaml
```

Wait for both pods to be Running:
```bash
kubectl get pods -n kyanos -o wide
```

---

## Step 3: Copy kyanos binary and install CA certs

```bash
# Copy the binary
kubectl cp ./kyanos kyanos/kyanos-capture:/usr/local/bin/kyanos -c debug

# Make it executable
kubectl exec -n kyanos kyanos-capture -c debug -- chmod +x /usr/local/bin/kyanos

# Install CA certificates (needed for TLS/gRPC)
kubectl exec -n kyanos kyanos-capture -c debug -- \
  bash -c "apt-get update -qq && apt-get install -y -qq ca-certificates > /dev/null 2>&1 && update-ca-certificates > /dev/null 2>&1"
```

---

## Step 4: Start kyanos

Capture **both HTTP and gRPC** traffic (server-side), with per-pod JSON output:

```bash
kubectl exec -n kyanos kyanos-capture -c debug -- \
  bash -c "nohup /usr/local/bin/kyanos watch \
    --side server \
    --json-output-dir /data/kyanos \
    > /tmp/kyanos.log 2>&1 &"
```

> **Note:** `watch` without a subcommand captures all supported protocols (HTTP, gRPC, Redis, MySQL, etc.).
> Use `watch http` or `watch grpc` to capture only a specific protocol.

---

## Step 5: Verify

### Check kyanos is running
```bash
kubectl exec -n kyanos kyanos-capture -c debug -- tail -5 /tmp/kyanos.log
```

Expected: fentry/fexit fallback warnings (normal), then silence = running OK.

### Check per-pod files are being written
```bash
kubectl exec -n kyanos kyanos-capture -c debug -- ls -la /data/kyanos/
```

You should see files like:
```
svc-orders.namespace.json
svc-gateway.namespace.json
unknown.json
```

### Check collector is uploading to GCS
```bash
kubectl logs -n kyanos kyanos-collector --tail=20
```

Expected output:
```
[collector] uploading (1143246B -> 89234B) -> gs://bucket/stg/svc-orders.namespace/2026-04-24/...json.gz
[collector] done: svc-orders.namespace
```

### Check data in GCS
```bash
gsutil ls gs://GCS_BUCKET/GCS_PREFIX/
```

---

## Useful Commands

| Action | Command |
|--------|---------|
| View kyanos logs | `kubectl exec -n kyanos kyanos-capture -c debug -- tail -50 /tmp/kyanos.log` |
| Check disk usage | `kubectl exec -n kyanos kyanos-capture -c debug -- du -sh /data/kyanos/` |
| Stop kyanos | `kubectl exec -n kyanos kyanos-capture -c debug -- pkill kyanos` |
| Restart kyanos | Stop + re-run the Step 4 command |
| Check collector | `kubectl logs -n kyanos kyanos-collector --tail=30` |
| Delete everything | `kubectl delete -f kyanos-deploy.yaml` |

---

## Optional: Header-based filtering

Filter only requests with a specific header (e.g., only traced requests):

```bash
# Only HTTP requests with Traceparent header (any value)
kyanos watch http --side server --header-regex 'Traceparent:.*' --json-output-dir /data/kyanos

# Only sampled traces (Traceparent ending with -01)
kyanos watch http --side server --header-regex 'Traceparent:.*-01$' --json-output-dir /data/kyanos
```

---

## Optional: gRPC reflection (decode protobuf bodies)

If gRPC services support server reflection:

```bash
# Auto-discover all gRPC services on the node
kyanos watch grpc --side server --auto-reflect --json-output-dir /data/kyanos

# Or target a specific server
kyanos watch grpc --side server --reflect localhost:50051 --json-output-dir /data/kyanos
```

---

## Troubleshooting

| Issue | Fix |
|-------|-----|
| Pod stuck in Pending | Check node CPU: `kubectl describe node <name> \| grep -A5 "Allocated"` — reduce CPU requests if needed |
| `unknown flag: --json-output-dir` | You're using an old binary. Rebuild with `make docker-build` |
| GCS 403 error | Ensure `gcs-kyanos-ksa` SA exists and has Workload Identity binding. The **collector** pod must NOT use `hostNetwork` |
| No data files appearing | Run without filters first to verify capture works. Check `tail /tmp/kyanos.log` for errors |
| x509 certificate error | Run: `apt-get install ca-certificates && update-ca-certificates` in the capture pod |
| Pod names show as "unknown" | Ensure `/run/containerd` is mounted. The container cache needs containerd access |
