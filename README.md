# SATFARM

SATFARM is a distributed SAT-solving system. Clients upload CNF files, the server creates shards, and workers solve them in parallel using Kissat. Workers return SAT results or DRAT-verified UNSAT proofs and earn credits.

---

## Run the Server

Start the server (HTTP + gRPC + PostgreSQL):

    docker compose up --build

Server endpoints (used by workers):
    HTTP endpoint:  http://YOUR_SERVER_HOST:8080
    gRPC endpoint:  YOUR_SERVER_HOST:50051

The server container includes drat-trim for UNSAT proof checking.

---

## Run a Worker

Workers solve SAT shards provided by the server.

To run a worker, you need the following information from the server operator:

- gRPC address
- HTTP address
- worker token

Build the worker image:

    docker build -t satfarm-worker -f Dockerfile.worker .

Run the worker (replace addresses and token):

    docker run --rm satfarm-worker \
      -addr GRPC_ADDRESS_HERE \
      -http HTTP_ADDRESS_HERE \
      -token YOUR_WORKER_TOKEN \
      -name worker1

Example:

    docker run --rm satfarm-worker \
      -addr example.com:50051 \
      -http http://example.com:8080 \
      -token abc123 \
      -name worker1

No special Docker networking options are required. The worker only makes outbound connections.

---

## Run a Worker Locally (Optional)

If you prefer to run without Docker, install Go and Kissat, then:

    go run ./cmd/worker \
      -addr GRPC_ADDRESS_HERE \
      -http HTTP_ADDRESS_HERE \
      -token YOUR_WORKER_TOKEN \
      -name worker1

---

## Requirements

Server:
    - Docker + Docker Compose
    - drat-trim (included in the server image)

Worker:
    - Recommended: Docker worker image (includes Kissat)
    - Optional local mode: Go 1.25+ and Kissat installed

---

# Developer Testing Notes (not required for third-party workers)

These notes are only for developers running both the server and the worker
on the same machine. Third-party workers do NOT need these instructions.

## When testing locally with Docker

Inside Docker containers, "localhost" refers to the container itself.
To let a Docker worker connect to your local server, use one of the methods below.

### Method 1 (recommended): Use your host machine's LAN IP

Find your LAN IP (Linux):

    ip a

Use the address (e.g. 192.168.x.x) when running the worker:

    docker run --rm satfarm-worker \
      -addr 192.168.x.x:50051 \
      -http http://192.168.x.x:8080 \
      -token TOKEN \
      -name worker1

This works on Linux, Mac, and Windows.

### Method 2 (Linux only): Use host networking

If you prefer to use "localhost" as the server address:

    docker run --rm --network host satfarm-worker \
      -addr localhost:50051 \
      -http http://localhost:8080 \
      -token TOKEN \
      -name worker1

This method only works on Linux and should not be used by third-party workers.

---

## License

MIT