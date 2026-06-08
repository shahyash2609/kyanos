#!/usr/bin/env bash
# Regenerate Python stubs from hello.proto.
# Run this once before starting the server or client.
set -e
cd "$(dirname "$0")"
pip3 install --quiet grpcio-tools
python3 -m grpc_tools.protoc \
    -I. \
    --python_out=. \
    --grpc_python_out=. \
    hello.proto
echo "Generated: hello_pb2.py  hello_pb2_grpc.py"
