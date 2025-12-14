package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pb "satfarm/internal/grpc/proto"
	"satfarm/internal/sat"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// FailureMode configures how the test worker should fail
type FailureMode string

const (
	FailNone          FailureMode = "none"         // Normal behavior (no failures)
	FailCrashMidSolve FailureMode = "crash_mid"    // Crash while solving (disconnect)
	FailWrongSAT      FailureMode = "wrong_sat"    // Return incorrect SAT assignment
	FailWrongUNSAT    FailureMode = "wrong_unsat"  // Return invalid DRAT proof
	FailTimeout       FailureMode = "timeout"      // Simulate infinite loop (never return)
	FailRandomCrash   FailureMode = "random_crash" // Random crash with probability
)

type TestWorker struct {
	name            string
	token           string
	httpBase        string
	failMode        FailureMode
	failProb        float64 // Probability of failure for random modes (0.0-1.0)
	failAfterN      int     // Fail after N successful shards (0 = fail on first)
	shardsProcessed int

	client     pb.WorkerServiceClient
	stream     pb.WorkerService_WorkClient
	httpClient *http.Client
	baseCtx    context.Context
}

func main() {
	addr := flag.String("addr", "localhost:50051", "gRPC address of SATFARM server")
	token := flag.String("token", "", "API token (required)")
	name := flag.String("name", "test-worker", "Worker name")
	httpBase := flag.String("http", "http://localhost:8080", "HTTP base URL")
	failMode := flag.String("fail", "none", "Failure mode: none, crash_mid, wrong_sat, wrong_unsat, timeout, random_crash")
	failProb := flag.Float64("fail-prob", 0.5, "Failure probability for random modes (0.0-1.0)")
	failAfterN := flag.Int("fail-after", 0, "Number of successful shards before triggering failure (0=fail on first)")
	flag.Parse()

	if *token == "" {
		log.Fatalf("missing -token")
	}

	rand.Seed(time.Now().UnixNano())

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ka := keepalive.ClientParameters{
		Time:                30 * time.Second,
		Timeout:             10 * time.Second,
		PermitWithoutStream: true,
	}

	conn, err := grpc.DialContext(
		baseCtx,
		*addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(ka),
	)
	if err != nil {
		log.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	client := pb.NewWorkerServiceClient(conn)

	md := metadata.Pairs("authorization", "Bearer "+*token)
	streamCtx := metadata.NewOutgoingContext(baseCtx, md)

	stream, err := client.Work(streamCtx)
	if err != nil {
		log.Fatalf("failed to create stream: %v", err)
	}

	w := &TestWorker{
		name:       *name,
		token:      *token,
		httpBase:   strings.TrimRight(*httpBase, "/"),
		failMode:   FailureMode(*failMode),
		failProb:   *failProb,
		failAfterN: *failAfterN,
		client:     client,
		stream:     stream,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		baseCtx:    baseCtx,
	}

	log.Printf("TestWorker %s starting (failMode=%s, failProb=%.2f, failAfterN=%d)",
		w.name, w.failMode, w.failProb, w.failAfterN)

	// Send Hello
	if err := w.stream.Send(&pb.WorkerMessage{
		Msg: &pb.WorkerMessage_Hello{
			Hello: &pb.Hello{
				WorkerName: w.name,
				SolverType: "test-kissat",
			},
		},
	}); err != nil {
		log.Fatalf("failed to send Hello: %v", err)
	}

	// Main loop
	for {
		msg, err := w.stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Printf("server closed stream")
				return
			}
			log.Printf("stream recv error: %v", err)
			return
		}

		switch m := msg.Msg.(type) {
		case *pb.ServerMessage_AssignJob:
			assign := m.AssignJob
			log.Printf("received AssignJob: job=%d shard=%d", assign.JobId, assign.ShardId)
			w.handleAssignJob(assign)

		case *pb.ServerMessage_Ping:
			log.Printf("received Ping")

		case *pb.ServerMessage_CancelJob:
			log.Printf("received CancelJob: job=%s", m.CancelJob.JobId)

		default:
			log.Printf("received unknown message")
		}
	}
}

func (w *TestWorker) shouldFail() bool {
	if w.shardsProcessed < w.failAfterN {
		return false
	}

	switch w.failMode {
	case FailNone:
		return false
	case FailCrashMidSolve, FailWrongSAT, FailWrongUNSAT, FailTimeout:
		return true
	case FailRandomCrash:
		return rand.Float64() < w.failProb
	default:
		return false
	}
}

func (w *TestWorker) handleAssignJob(assign *pb.AssignJob) {
	ctx := w.baseCtx

	// Download CNF
	cnfPath, err := w.downloadCNF(ctx, assign.JobId)
	if err != nil {
		log.Printf("downloadCNF failed: %v", err)
		return
	}
	defer os.Remove(cnfPath)

	// Apply cube
	cubedPath, err := sat.ApplyCube(cnfPath, assign.CubeLiterals)
	if err != nil {
		log.Printf("ApplyCube failed: %v", err)
		return
	}
	defer os.Remove(cubedPath)

	// Prepare CNF
	solveCNF, err := sat.PrepareCNFForSolving(cubedPath)
	if err != nil {
		log.Printf("PrepareCNFForSolving failed: %v", err)
		return
	}
	defer os.Remove(solveCNF)

	// Check if we should fail
	if w.shouldFail() {
		w.executeFail(ctx, assign, solveCNF)
		return
	}

	// Normal solve
	satResult, modelStr, err := sat.RunKissat(ctx, solveCNF)
	if err != nil {
		log.Printf("RunKissat failed: %v", err)
		return
	}

	if satResult {
		w.sendSATResult(assign, modelStr)
	} else {
		w.sendUNSATResult(ctx, assign, solveCNF)
	}

	w.shardsProcessed++
	log.Printf("Successfully processed shard (total: %d)", w.shardsProcessed)
}

func (w *TestWorker) executeFail(ctx context.Context, assign *pb.AssignJob, solveCNF string) {
	log.Printf("EXECUTING FAILURE MODE: %s for job=%d shard=%d", w.failMode, assign.JobId, assign.ShardId)

	switch w.failMode {
	case FailCrashMidSolve, FailRandomCrash:
		// Simulate crash: just exit without sending result
		log.Printf("CRASH: Disconnecting mid-solve...")
		os.Exit(1)

	case FailWrongSAT:
		// Send a wrong SAT result with garbage assignment
		log.Printf("WRONG_SAT: Sending invalid SAT assignment...")
		w.stream.Send(&pb.WorkerMessage{
			Msg: &pb.WorkerMessage_JobResult{
				JobResult: &pb.JobResult{
					JobId:           assign.JobId,
					ShardId:         assign.ShardId,
					Sat:             true,
					ModelOrCoreInfo: "1 -2 3 -4 5 999999", // Garbage assignment
				},
			},
		})

	case FailWrongUNSAT:
		// Create an empty/invalid DRAT proof and upload it
		log.Printf("WRONG_UNSAT: Uploading invalid DRAT proof...")
		tmpProof, _ := os.CreateTemp("", "fake-proof-*.drat")
		tmpProof.WriteString("d 1 2 3 0\n") // Invalid DRAT content
		tmpProof.Close()
		defer os.Remove(tmpProof.Name())

		dratID, err := w.uploadDratProof(ctx, assign.JobId, assign.ShardId, tmpProof.Name())
		if err != nil {
			log.Printf("Failed to upload fake proof: %v", err)
			return
		}

		w.stream.Send(&pb.WorkerMessage{
			Msg: &pb.WorkerMessage_JobResult{
				JobResult: &pb.JobResult{
					JobId:           assign.JobId,
					ShardId:         assign.ShardId,
					Sat:             false,
					ModelOrCoreInfo: dratID,
				},
			},
		})

	case FailTimeout:
		// Simulate infinite loop
		log.Printf("TIMEOUT: Entering infinite loop (will never return)...")
		for {
			time.Sleep(time.Hour)
		}
	}
}

func (w *TestWorker) sendSATResult(assign *pb.AssignJob, modelStr string) {
	if err := w.stream.Send(&pb.WorkerMessage{
		Msg: &pb.WorkerMessage_JobResult{
			JobResult: &pb.JobResult{
				JobId:           assign.JobId,
				ShardId:         assign.ShardId,
				Sat:             true,
				ModelOrCoreInfo: modelStr,
			},
		},
	}); err != nil {
		log.Printf("failed to send SAT result: %v", err)
		return
	}
	log.Printf("sent SAT result for job=%d shard=%d", assign.JobId, assign.ShardId)
}

func (w *TestWorker) sendUNSATResult(ctx context.Context, assign *pb.AssignJob, solveCNF string) {
	proofPath, err := sat.RunKissatDRAT(ctx, solveCNF)
	if err != nil {
		log.Printf("RunKissatDRAT failed: %v", err)
		return
	}
	defer os.Remove(proofPath)

	dratID, err := w.uploadDratProof(ctx, assign.JobId, assign.ShardId, proofPath)
	if err != nil {
		log.Printf("uploadDratProof failed: %v", err)
		return
	}

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
		log.Printf("failed to send UNSAT result: %v", err)
		return
	}
	log.Printf("sent UNSAT result for job=%d shard=%d (drat_file_id=%s)", assign.JobId, assign.ShardId, dratID)
}

func (w *TestWorker) downloadCNF(ctx context.Context, jobID int64) (string, error) {
	url := fmt.Sprintf("%s/worker/cnf/%d", w.httpBase, jobID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+w.token)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	tmp, err := os.CreateTemp("", fmt.Sprintf("testworker-job-%d-*.cnf", jobID))
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return "", err
	}

	return tmp.Name(), nil
}

func (w *TestWorker) uploadDratProof(ctx context.Context, jobID, shardID int64, proofPath string) (string, error) {
	url := fmt.Sprintf("%s/worker/drat", w.httpBase)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	form.WriteField("job_id", fmt.Sprint(jobID))
	form.WriteField("shard_id", fmt.Sprint(shardID))

	f, err := os.Open(proofPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	part, err := form.CreateFormFile("proof", filepath.Base(proofPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	form.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+w.token)
	req.Header.Set("Content-Type", form.FormDataContentType())

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	var respBody struct {
		Ok         bool  `json:"ok"`
		DratFileID int64 `json:"drat_file_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return "", err
	}

	return strconv.FormatInt(respBody.DratFileID, 10), nil
}
