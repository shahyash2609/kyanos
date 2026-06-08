#!/usr/bin/env python3
"""
gRPC test server for kyanos HTTP/2 monitoring validation.

Implements:
  /hello.Greeter/SayHello        - unary RPC
  /hello.Greeter/SayHelloStream  - server-streaming RPC

Usage:
    python3 server.py [--port 50051]
"""

import argparse
import logging
import time
from concurrent import futures

import grpc
import hello_pb2
import hello_pb2_grpc


class GreeterServicer(hello_pb2_grpc.GreeterServicer):

    def SayHello(self, request, context):
        logging.info("SayHello: name=%s", request.name)
        return hello_pb2.HelloReply(
            message=f"Hello, {request.name}!",
            index=0,
        )

    def SayHelloStream(self, request, context):
        logging.info("SayHelloStream: name=%s", request.name)
        for i in range(5):
            yield hello_pb2.HelloReply(
                message=f"Hello #{i}, {request.name}!",
                index=i,
            )
            time.sleep(0.1)


def serve(port: int) -> None:
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    hello_pb2_grpc.add_GreeterServicer_to_server(GreeterServicer(), server)
    listen_addr = f"[::]:{port}"
    server.add_insecure_port(listen_addr)
    server.start()
    logging.info("gRPC server listening on %s", listen_addr)
    server.wait_for_termination()


if __name__ == "__main__":
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
    )
    parser = argparse.ArgumentParser(description="kyanos gRPC test server")
    parser.add_argument("--port", type=int, default=50051)
    args = parser.parse_args()
    serve(args.port)
