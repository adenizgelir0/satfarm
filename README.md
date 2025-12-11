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

---

## Run a Worker
go run ./cmd/worker \
  -addr localhost:50051 \
  -http http://localhost:8080 \
  -token 23191ccdb291dc914483479e9d46259df20d4f50623dee3346eb4db09c740402 \
  -name worker1

---

## Requirements
- Go 1.22+
- Docker + Docker Compose
- Kissat solver
- drat-trim for UNSAT verification

---

## License
MIT