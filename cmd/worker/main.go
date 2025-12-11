package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "satfarm/internal/grpc/proto"
	"satfarm/internal/sat"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

type runningJob struct {
	jobID   int64
	shardID int64
	cancel  context.CancelFunc
}

type Worker struct {
	name       string
	solverType string
	token      string
	httpBase   string

	client pb.WorkerServiceClient
	stream pb.WorkerService_WorkClient

	httpClient *http.Client

	// baseCtx lives for the entire worker lifetime
	baseCtx context.Context

	mu         sync.Mutex
	currentJob *runningJob
}

func ensureKissatInstalled() error {
	_, err := exec.LookPath("kissat")
	if err != nil {
		return fmt.Errorf("kissat not found in PATH: %w", err)
	}
	return nil
}

func main() {
	addr := flag.String("addr", "localhost:50051", "gRPC address of SATFARM server (host:port)")
	token := flag.String("token", "", "API token to authenticate worker (required)")
	name := flag.String("name", "", "worker name (optional, defaults to hostname)")
	solver := flag.String("solver", "kissat", "solver type label to send in Hello message")
	httpBase := flag.String("http", "http://localhost:8080", "HTTP base URL of SATFARM server (for /worker/cnf and /worker/drat)")
	flag.Parse()

	if *token == "" {
		log.Fatalf("missing -token: you must pass an API token for worker authentication")
	}

	if err := ensureKissatInstalled(); err != nil {
		log.Fatalf("kissat check failed: %v\nPlease install kissat and make sure it is in your PATH.", err)
	}

	if *name == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			*name = "satfarm-worker"
		} else {
			*name = "worker-" + host
		}
	}

	// normalize http base (strip trailing slash)
	base := strings.TrimRight(*httpBase, "/")

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// gRPC client keepalive: keep long-lived connection healthy
	ka := keepalive.ClientParameters{
		Time:                30 * time.Second, // send pings if idle for 30s
		Timeout:             10 * time.Second, // wait 10s for ping ack
		PermitWithoutStream: true,
	}

	// Dial gRPC server
	conn, err := grpc.DialContext(
		baseCtx,
		*addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(ka),
	)
	if err != nil {
		log.Fatalf("failed to dial gRPC server %q: %v", *addr, err)
	}
	defer conn.Close()

	client := pb.NewWorkerServiceClient(conn)

	// Attach Authorization metadata to the Work stream
	md := metadata.Pairs("authorization", "Bearer "+*token)
	streamCtx := metadata.NewOutgoingContext(baseCtx, md)

	stream, err := client.Work(streamCtx)
	if err != nil {
		log.Fatalf("failed to create Work stream: %v", err)
	}

	w := &Worker{
		name:       *name,
		solverType: *solver,
		token:      *token,
		httpBase:   base,
		client:     client,
		stream:     stream,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		baseCtx: baseCtx,
	}

	log.Printf("Worker %s starting, gRPC=%s http=%s", w.name, *addr, w.httpBase)

	// Send initial Hello
	if err := w.sendHello(); err != nil {
		log.Fatalf("failed to send Hello: %v", err)
	}

	// Main receive loop
	for {
		msg, err := w.stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Printf("server closed stream (EOF)")
				return
			}
			log.Printf("stream recv error: %v", err)
			return
		}

		switch m := msg.Msg.(type) {
		case *pb.ServerMessage_AssignJob:
			assign := m.AssignJob
			log.Printf("received AssignJob: job=%d shard=%d cube=%v",
				assign.JobId, assign.ShardId, assign.CubeLiterals)

			// Start job in its own goroutine, with a cancellable context.
			w.startJob(assign)

		case *pb.ServerMessage_Ping:
			log.Printf("received Ping from server")

		case *pb.ServerMessage_CancelJob:
			log.Printf("received CancelJob for job=%s", m.CancelJob.JobId)
			jobID, err := strconv.ParseInt(m.CancelJob.JobId, 10, 64)
			if err != nil {
				log.Printf("invalid CancelJob job_id=%q: %v", m.CancelJob.JobId, err)
				continue
			}
			w.cancelJob(jobID)

		default:
			log.Printf("received unknown server message")
		}
	}
}

func (w *Worker) sendHello() error {
	return w.stream.Send(&pb.WorkerMessage{
		Msg: &pb.WorkerMessage_Hello{
			Hello: &pb.Hello{
				WorkerName: w.name,
				SolverType: w.solverType,
			},
		},
	})
}

// startJob creates a cancellable context for this job and runs handleAssignJob
// in its own goroutine. Only one job at a time is allowed per worker.
func (w *Worker) startJob(assign *pb.AssignJob) {
	w.mu.Lock()
	if w.currentJob != nil {
		log.Printf("startJob: already running job=%d shard=%d, ignoring new assign for job=%d shard=%d",
			w.currentJob.jobID, w.currentJob.shardID, assign.JobId, assign.ShardId)
		w.mu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(w.baseCtx)
	w.currentJob = &runningJob{
		jobID:   assign.JobId,
		shardID: assign.ShardId,
		cancel:  cancel,
	}
	w.mu.Unlock()

	go func() {
		err := w.handleAssignJob(ctx, assign)

		w.mu.Lock()
		// Clear currentJob if it still matches this assign
		if w.currentJob != nil &&
			w.currentJob.jobID == assign.JobId &&
			w.currentJob.shardID == assign.ShardId {
			w.currentJob = nil
		}
		w.mu.Unlock()

		if err != nil {
			// If the context was canceled, treat as a normal cancellation, not an error.
			if ctx.Err() == context.Canceled {
				log.Printf("job cancelled locally: job=%d shard=%d", assign.JobId, assign.ShardId)
				return
			}
			log.Printf("handleAssignJob error (job=%d shard=%d): %v",
				assign.JobId, assign.ShardId, err)
		}
	}()
}

// cancelJob cancels the currently running job if its jobID matches.
func (w *Worker) cancelJob(jobID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.currentJob == nil {
		log.Printf("cancelJob: no job running, ignoring CancelJob for job=%d", jobID)
		return
	}
	if w.currentJob.jobID != jobID {
		log.Printf("cancelJob: running job=%d shard=%d, ignoring CancelJob for job=%d",
			w.currentJob.jobID, w.currentJob.shardID, jobID)
		return
	}

	log.Printf("cancelJob: cancelling job=%d shard=%d", w.currentJob.jobID, w.currentJob.shardID)
	w.currentJob.cancel()
	// currentJob will be cleared by the goroutine once handleAssignJob returns
}

// handleAssignJob downloads the CNF, augments it with the cube, runs kissat
// on a normalized "solve CNF", and sends a JobResult back to the server.
// For UNSAT it also generates and uploads a DRAT proof, obtains drat_file_id,
// and reports that ID in JobResult.ModelOrCoreInfo.
// If ctx is cancelled, it stops and does NOT send a JobResult.
func (w *Worker) handleAssignJob(ctx context.Context, assign *pb.AssignJob) error {
	// 1) Download original CNF
	cnfPath, err := w.downloadCNF(ctx, assign.JobId)
	if err != nil {
		// Respect cancellation
		if ctx.Err() == context.Canceled {
			return ctx.Err()
		}
		return fmt.Errorf("downloadCNF: %w", err)
	}
	defer os.Remove(cnfPath)

	// 2) Apply cube as unit clauses to get CNF ∧ CUBE
	cubedPath, err := sat.ApplyCube(cnfPath, assign.CubeLiterals)
	if err != nil {
		return fmt.Errorf("ApplyCube: %w", err)
	}
	defer os.Remove(cubedPath)

	// 3) Normalize into "solve CNF" exactly once (strip '%' trailer etc.)
	solveCNF, err := sat.PrepareCNFForSolving(cubedPath)
	if err != nil {
		return fmt.Errorf("PrepareCNFForSolving: %w", err)
	}
	defer os.Remove(solveCNF)

	// Check cancellation before heavy solving
	if ctx.Err() == context.Canceled {
		return ctx.Err()
	}

	// 4) Run kissat in normal mode on the solve CNF
	satResult, modelStr, err := sat.RunKissat(ctx, solveCNF)
	if err != nil {
		// If cancelled while solving, just return
		if ctx.Err() == context.Canceled {
			return ctx.Err()
		}
		return fmt.Errorf("RunKissat: %w", err)
	}

	if ctx.Err() == context.Canceled {
		return ctx.Err()
	}

	if satResult {
		// SAT: send model and we're done. No proof needed.
		res := &pb.JobResult{
			JobId:           assign.JobId,
			ShardId:         assign.ShardId,
			Sat:             true,
			ModelOrCoreInfo: modelStr,
		}

		if err := w.stream.Send(&pb.WorkerMessage{
			Msg: &pb.WorkerMessage_JobResult{
				JobResult: res,
			},
		}); err != nil {
			// If stream context is cancelled, just stop
			if ctx.Err() == context.Canceled {
				return ctx.Err()
			}
			return fmt.Errorf("send SAT JobResult: %w", err)
		}

		log.Printf("sent SAT JobResult: job=%d shard=%d", assign.JobId, assign.ShardId)
		return nil
	}

	// 5) UNSAT: run kissat again on the *same* solve CNF to produce DRAT proof
	proofPath, err := sat.RunKissatDRAT(ctx, solveCNF)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return ctx.Err()
		}
		return fmt.Errorf("RunKissatDRAT: %w", err)
	}
	defer os.Remove(proofPath)

	if ctx.Err() == context.Canceled {
		return ctx.Err()
	}

	// 6) Upload DRAT proof via HTTP and get drat_file_id
	dratID, err := w.uploadDratProof(ctx, assign.JobId, assign.ShardId, proofPath)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return ctx.Err()
		}
		return fmt.Errorf("uploadDratProof: %w", err)
	}

	if ctx.Err() == context.Canceled {
		return ctx.Err()
	}

	// 7) Send UNSAT JobResult, carrying drat_file_id in ModelOrCoreInfo
	if err := w.stream.Send(&pb.WorkerMessage{
		Msg: &pb.WorkerMessage_JobResult{
			JobResult: &pb.JobResult{
				JobId:           assign.JobId,
				ShardId:         assign.ShardId,
				Sat:             false,
				ModelOrCoreInfo: dratID,
			},
		},
	}); err != nil {
		if ctx.Err() == context.Canceled {
			return ctx.Err()
		}
		return fmt.Errorf("send UNSAT JobResult: %w", err)
	}

	log.Printf("sent UNSAT JobResult: job=%d shard=%d (drat_file_id=%s)", assign.JobId, assign.ShardId, dratID)
	return nil
}

// downloadCNF builds the URL httpBase/worker/cnf/{jobID}, GETs it, and writes to a temp file.
func (w *Worker) downloadCNF(ctx context.Context, jobID int64) (string, error) {
	url := fmt.Sprintf("%s/worker/cnf/%d", w.httpBase, jobID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("NewRequest: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.token)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		// Respect cancellation
		if ctx.Err() == context.Canceled {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("HTTP GET %s: status %d: %s",
			url, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmp, err := os.CreateTemp("", fmt.Sprintf("satfarm-job-%d-*.cnf", jobID))
	if err != nil {
		return "", fmt.Errorf("CreateTemp: %w", err)
	}
	defer tmp.Close()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return "", fmt.Errorf("copy cnf: %w", err)
	}

	return tmp.Name(), nil
}

// uploadDratProof POSTs the DRAT file to /worker/drat with multipart/form-data,
// parses the JSON response, and returns drat_file_id as a string.
func (w *Worker) uploadDratProof(ctx context.Context, jobID, shardID int64, proofPath string) (string, error) {
	url := fmt.Sprintf("%s/worker/drat", w.httpBase)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	// Fields
	if err := form.WriteField("job_id", fmt.Sprint(jobID)); err != nil {
		return "", fmt.Errorf("WriteField job_id: %w", err)
	}
	if err := form.WriteField("shard_id", fmt.Sprint(shardID)); err != nil {
		return "", fmt.Errorf("WriteField shard_id: %w", err)
	}

	// File
	f, err := os.Open(proofPath)
	if err != nil {
		return "", fmt.Errorf("open proof: %w", err)
	}
	defer f.Close()

	part, err := form.CreateFormFile("proof", filepath.Base(proofPath))
	if err != nil {
		return "", fmt.Errorf("CreateFormFile: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", fmt.Errorf("copy proof: %w", err)
	}

	if err := form.Close(); err != nil {
		return "", fmt.Errorf("form.Close: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("NewRequest: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.token)
	req.Header.Set("Content-Type", form.FormDataContentType())

	resp, err := w.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("httpClient.Do: %w", err)
	}
	defer resp.Body.Close()

	// Non-200: read a small error body for logging
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("DRAT upload error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	// Parse JSON: { ok: true, drat_file_id: <id> }
	var respBody struct {
		Ok         bool   `json:"ok"`
		DratFileID int64  `json:"drat_file_id"`
		Error      string `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return "", fmt.Errorf("decode DRAT upload response: %w", err)
	}

	if !respBody.Ok || respBody.DratFileID == 0 {
		// Prefer server-sent error message if present
		msg := respBody.Error
		if msg == "" {
			msg = "DRAT upload response not ok or missing drat_file_id"
		}
		return "", fmt.Errorf(msg)
	}

	dratID := strconv.FormatInt(respBody.DratFileID, 10)
	log.Printf("DRAT proof uploaded for job=%d shard=%d (drat_file_id=%s)", jobID, shardID, dratID)
	return dratID, nil
}
