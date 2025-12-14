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
```bash
docker compose up --build
```

Server endpoints:
- HTTP: http://localhost:8080
- gRPC: localhost:50051

---

## Run a Worker
```bash
go run ./cmd/worker \
  -addr localhost:50051 \
  -http http://localhost:8080 \
  -name worker1 \
  -token 23191ccdb291dc914483479e9d46259df20d4f50623dee3346eb4db09c740402
```

---

## Requirements
- Go 1.22+
- Docker + Docker Compose
- Kissat solver
- drat-trim for UNSAT verification

### Installing the Kissat and drat-trim (may be wrong)

```bash
docker compose up --build
```
2. Run the Worker (Inside WSL2)

A. Install Build Tools
```bash
sudo apt update
sudo apt install -y build-essential git
```

B. Compile Kissat
```bash
git clone https://github.com/arminbiere/kissat.git
cd kissat
./configure && make
sudo cp build/kissat /usr/local/bin/
```

C. Compile drat-trim
```bash
git clone https://github.com/marijnheule/drat-trim.git
cd drat-trim
make
sudo cp drat-trim /usr/local/bin/
```

D. Run the Worker Now kissat and drat-trim are in your path inside WSL.
```bash
# Navigate to your project
go run ./cmd/worker -addr localhost:50051 -http http://localhost:8080 -name wsl-worker
```

---

## License
MIT