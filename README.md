# SATFARM

SATFARM is a distributed SAT-solving system. Clients upload CNF files, the server creates shards, and workers solve them in parallel using Kissat. Workers return SAT results or DRAT-verified UNSAT proofs and earn credits. The system uses gRPC for scheduling and HTTP for file transfer.

---

## Features

- Upload CNF files via HTTP.
- Workers download CNFs, run Kissat, and report results.
- DRAT proof upload and verification for UNSAT.
- Credit-based reward system for workers.
- Simple gRPC scheduler for job assignment.

---

## Run the Server

docker compose up --build

Server endpoints:

- HTTP: http://localhost:8080
- gRPC: localhost:50051

The server container includes:

- drat-trim (used for verifying UNSAT proofs)
- PostgreSQL client tools
- Everything required to run the SATFARM core

No host-level installation is required.

---

## Run a Worker (Docker image – recommended)

Build the worker image (this automatically compiles Kissat inside the container):

docker build -t satfarm-worker -f Dockerfile.worker .

### If the server is running on the host machine

docker run --rm --network host satfarm-worker \
  -addr localhost:50051 \
  -http http://localhost:8080 \
  -token 23191ccdb291dc914483479e9d46259df20d4f50623dee3346eb4db09c740402 \
  -name worker1

### If the server is running inside Docker Compose

The default service name is satfarm-server on the satfarm_default network:

docker run --rm --network satfarm_default satfarm-worker \
  -addr satfarm-server:50051 \
  -http http://satfarm-server:8080 \
  -token 23191ccdb291dc914483479e9d46259df20d4f50623dee3346eb4db09c740402 \
  -name worker1

The -token flag is the worker’s API token issued by the server.
The -name flag is just a display name.

---

## Run a Worker (local, without Docker)

If you have Go and Kissat installed on your machine:

go run ./cmd/worker \
  -addr localhost:50051 \
  -http http://localhost:8080 \
  -token 23191ccdb291dc914483479e9d46259df20d4f50623dee3346eb4db09c740402 \
  -name worker1

In this mode:

- kissat must be on your PATH
- drat-trim is not required locally — only the server verifies DRAT proofs

---

## Requirements

### Server (Dockerized)

- Docker + Docker Compose
- No external dependencies; the server image includes:
  - drat-trim
  - required runtime tools
  - Go-built server binary

### Worker (choose one)

✔ Recommended: Docker Worker Image  
Contains Kissat compiled from source; no host dependencies.

Local Worker (no Docker):

- Go 1.25+
- Kissat installed on host

---

## License

MIT