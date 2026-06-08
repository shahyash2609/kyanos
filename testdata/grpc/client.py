#!/usr/bin/env python3
"""
gRPC test client for kyanos HTTP/2 monitoring validation.

Sends repeated unary and streaming requests so kyanos has traffic to capture.

Usage:
    python3 client.py [options]

Examples:
    # plain unary RPCs every second (default)
    python3 client.py

    # use gzip compression (tests grpc-encoding decompression in kyanos)
    python3 client.py --gzip

    # server-streaming RPCs
    python3 client.py --stream

    # target a non-default address
    python3 client.py --host localhost:50051 --count 50 --delay 0.5
"""

import argparse
import logging
import time

import grpc
import hello_pb2
import hello_pb2_grpc


def run_unary(stub, name: str, compression) -> None:
    response = stub.SayHello(
        hello_pb2.HelloRequest(name=name),
        compression=compression,
    )
    logging.info("[unary]  %s", response.message)


def run_stream(stub, name: str, compression) -> None:
    responses = stub.SayHelloStream(
        hello_pb2.HelloRequest(name=name),
        compression=compression,
    )
    for resp in responses:
        logging.info("[stream] #%d  %s", resp.index, resp.message)


def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
    )
    parser = argparse.ArgumentParser(description="kyanos gRPC test client")
    parser.add_argument("--host",   default="localhost:50051")
    parser.add_argument("--count",  type=int,   default=10,
                        help="number of RPCs to send (0 = infinite)")
    parser.add_argument("--delay",  type=float, default=1.0,
                        help="seconds between RPCs")
    parser.add_argument("--gzip",   action="store_true",
                        help="enable gzip compression (grpc-encoding: gzip)")
    parser.add_argument("--stream", action="store_true",
                        help="use server-streaming RPC instead of unary")
    args = parser.parse_args()

    compression = grpc.Compression.Gzip if args.gzip else grpc.Compression.NoCompression

    with grpc.insecure_channel(args.host) as channel:
        stub = hello_pb2_grpc.GreeterStub(channel)
        i = 0
        while args.count == 0 or i < args.count:
            name = f"World-{i}"
            try:
                if args.stream:
                    run_stream(stub, name, compression)
                else:
                    run_unary(stub, name, compression)
            except grpc.RpcError as exc:
                logging.error("RPC failed: %s", exc)
            i += 1
            if args.delay > 0:
                time.sleep(args.delay)


if __name__ == "__main__":
    main()
