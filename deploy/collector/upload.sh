#!/usr/bin/env bash
# Collector sidecar: watches a local directory for per-pod JSON files
# written by kyanos and uploads completed files to GCS with optional gzip compression.
#
# Strategy: copy-then-truncate (zero data loss)
#   1. Copy pod.json -> .staging/pod.json
#   2. Truncate pod.json to 0 bytes
#      (kyanos holds fd with O_APPEND — next write goes to offset 0, safe on Linux)
#   3. Optionally gzip the staged file
#   4. Upload to GCS
#   5. Delete local staged file
#
# Environment variables:
#   GCS_BUCKET       — destination bucket (required)
#   GCS_PREFIX       — path prefix inside bucket (default: "stg")
#   DATA_DIR         — local directory to watch (default: /data/kyanos)
#   UPLOAD_INTERVAL  — seconds between upload sweeps (default: 60)
#   MIN_SIZE_BYTES   — only upload files larger than this (default: 1, i.e. non-empty)
#   COMPRESS         — set to "true" to gzip before uploading (default: "true")

set -euo pipefail

GCS_BUCKET="${GCS_BUCKET:?GCS_BUCKET is required}"
GCS_PREFIX="${GCS_PREFIX:-stg}"
DATA_DIR="${DATA_DIR:-/data/kyanos}"
UPLOAD_INTERVAL="${UPLOAD_INTERVAL:-60}"
MIN_SIZE_BYTES="${MIN_SIZE_BYTES:-1}"
COMPRESS="${COMPRESS:-true}"
STAGING_DIR="${DATA_DIR}/.staging"

echo "[collector] bucket=${GCS_BUCKET} prefix=${GCS_PREFIX} dir=${DATA_DIR} interval=${UPLOAD_INTERVAL}s compress=${COMPRESS}"

mkdir -p "${DATA_DIR}" "${STAGING_DIR}"

while true; do
    find "${DATA_DIR}" -maxdepth 1 -name '*.json' -type f | while read -r filepath; do
        filename=$(basename "${filepath}")
        filesize=$(stat -c%s "${filepath}" 2>/dev/null || echo 0)

        # Skip files below threshold
        if [ "${filesize}" -lt "${MIN_SIZE_BYTES}" ]; then
            continue
        fi

        staging_path="${STAGING_DIR}/${filename}"

        # Step 1: Copy the current data
        cp "${filepath}" "${staging_path}"

        # Step 2: Truncate the original (kyanos fd stays open, O_APPEND safe)
        truncate -s 0 "${filepath}"

        # Step 3: Compress if enabled
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

        # Step 4: Upload to GCS
        upload_size=$(stat -c%s "${staging_path}" 2>/dev/null || echo 0)
        echo "[collector] uploading ${staging_path} (${filesize}B -> ${upload_size}B) -> ${gcs_path}"
        if gsutil -q cp "${staging_path}" "${gcs_path}"; then
            rm -f "${staging_path}"
            echo "[collector] done: ${pod_name}"
        else
            echo "[collector] FAILED: ${staging_path} (kept for retry)"
        fi
    done

    # Retry any leftover staging files from previous failed uploads
    find "${STAGING_DIR}" \( -name '*.json' -o -name '*.json.gz' \) -type f 2>/dev/null | while read -r leftover; do
        filename=$(basename "${leftover}")
        # Strip .gz and .json to get pod name
        pod_name="${filename%.gz}"
        pod_name="${pod_name%.json}"
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
