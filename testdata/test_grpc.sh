#!/usr/bin/env bash
# gRPC (HTTP/2) end-to-end test for kyanos.
#
# Usage:
#   ./testdata/test_grpc.sh <path-to-kyanos-binary>
#
# What it does:
#   1. Installs Python deps and generates proto stubs (once).
#   2. Starts the Python gRPC test server on port 50051.
#   3. Runs each test scenario:
#        test_grpc_unary         - plain unary RPCs, check path captured
#        test_grpc_gzip          - gzip-compressed RPCs, check decompression
#        test_grpc_path_filter   - verify --path flag filters correctly
#        test_grpc_stream        - server-streaming RPCs
#   4. Stops the server.

. "$(dirname "$0")/common.sh"
set -ex

CMD="$1"
if [ -z "$CMD" ]; then
    echo "Usage: $0 <path-to-kyanos-binary>" >&2
    exit 1
fi

GRPC_DIR="$(dirname "$0")/grpc"
FILE_PREFIX="/tmp/kyanos_grpc"
GRPC_PORT=50051
SERVER_PID=""

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------

function setup() {
    pip3 install --quiet -r "${GRPC_DIR}/requirements.txt"
    # Generate proto stubs if not present
    if [ ! -f "${GRPC_DIR}/hello_pb2.py" ]; then
        bash "${GRPC_DIR}/generate_proto.sh"
    fi
}

function start_server() {
    python3 "${GRPC_DIR}/server.py" --port "${GRPC_PORT}" &
    SERVER_PID=$!
    echo "gRPC server started (PID=${SERVER_PID})"
    sleep 2  # give the server time to bind
}

function stop_server() {
    if [ -n "${SERVER_PID}" ]; then
        kill "${SERVER_PID}" 2>/dev/null || true
        wait "${SERVER_PID}" 2>/dev/null || true
        echo "gRPC server stopped"
    fi
}

# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

function test_grpc_unary() {
    local log="${FILE_PREFIX}_unary.log"

    # Run kyanos in background, then drive traffic
    timeout 30 "${CMD}" watch --debug-output grpc \
        --host "localhost:${GRPC_PORT}" 2>&1 | tee "${log}" &
    sleep 3

    python3 "${GRPC_DIR}/client.py" \
        --host "localhost:${GRPC_PORT}" \
        --count 5 --delay 1

    wait
    cat "${log}"
    # kyanos should have captured the /hello.Greeter/SayHello path
    check_patterns_in_file "${log}" "SayHello"
}

function test_grpc_gzip() {
    local log="${FILE_PREFIX}_gzip.log"

    timeout 30 "${CMD}" watch --debug-output grpc \
        --host "localhost:${GRPC_PORT}" 2>&1 | tee "${log}" &
    sleep 3

    # Send gzip-compressed requests; kyanos should decompress and show body
    python3 "${GRPC_DIR}/client.py" \
        --host "localhost:${GRPC_PORT}" \
        --count 5 --delay 1 --gzip

    wait
    cat "${log}"
    check_patterns_in_file "${log}" "SayHello"
}

function test_grpc_path_filter() {
    local log="${FILE_PREFIX}_path_filter.log"

    # Filter on the exact gRPC method path
    timeout 30 "${CMD}" watch --debug-output grpc \
        --path "/hello.Greeter/SayHello" 2>&1 | tee "${log}" &
    sleep 3

    python3 "${GRPC_DIR}/client.py" \
        --host "localhost:${GRPC_PORT}" \
        --count 5 --delay 1

    wait
    cat "${log}"
    check_patterns_in_file "${log}" "SayHello"
}

function test_grpc_stream() {
    local log="${FILE_PREFIX}_stream.log"

    timeout 30 "${CMD}" watch --debug-output grpc \
        --host "localhost:${GRPC_PORT}" 2>&1 | tee "${log}" &
    sleep 3

    python3 "${GRPC_DIR}/client.py" \
        --host "localhost:${GRPC_PORT}" \
        --count 3 --delay 1 --stream

    wait
    cat "${log}"
    check_patterns_in_file "${log}" "SayHelloStream"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

function main() {
    setup
    start_server
    trap stop_server EXIT

    test_grpc_unary
    test_grpc_gzip
    test_grpc_path_filter
    test_grpc_stream

    echo "All gRPC tests passed."
}

main
