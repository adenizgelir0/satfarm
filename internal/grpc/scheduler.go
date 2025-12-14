// internal/grpc/scheduler.go
package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "satfarm/internal/grpc/proto"
	"satfarm/internal/sat"

	"github.com/lib/pq"
)

type Shard struct {
	JobID            int64
	ShardID          int64
	CubeLits         []int32
	AssignedWorkerID int64
	AssignedUserID   int64
}

type JobScheduler interface {
	MaybeAssignWork(workerID int64) error
	HandleJobResult(workerID int64, result *pb.JobResult) error
	EnqueueJob(jobID int64, cnfPath string, maxWorkers int)

	VerifyUnsatShard(jobID, shardID, dratFileID, userID int64, dratPath string) error
	ReenqueueShard(shardID int64) error
	HandleDisconnect(workerID int64)
}

type SimpleScheduler struct {
	mu sync.Mutex

	workers *WorkerRegistry

	// workerID -> shard currently assigned (nil if idle)
	workerShard map[int64]*Shard

	db      *sql.DB
	pending []*Shard

	// jobs that are logically finished (SAT found or completely UNSAT)
	completedJobs map[int64]bool
}

// getWorkerUserID maps a worker connection ID to its owning user_id.
func (s *SimpleScheduler) getWorkerUserID(workerID int64) (int64, error) {
	conn, ok := s.workers.Get(workerID)
	if !ok || conn == nil {
		return 0, fmt.Errorf("getWorkerUserID: no WorkerConn for workerID=%d", workerID)
	}
	return conn.Info.UserID, nil
}

func NewSimpleScheduler(db *sql.DB, reg *WorkerRegistry) *SimpleScheduler {
	return &SimpleScheduler{
		db:            db,
		workers:       reg,
		workerShard:   make(map[int64]*Shard),
		pending:       []*Shard{},
		completedJobs: make(map[int64]bool),
	}
}

func (s *SimpleScheduler) EnqueueJob(jobID int64, cnfPath string, maxWorkers int) {
	if maxWorkers <= 0 {
		maxWorkers = 1
	}

	// 1) Validate CNF
	if err := sat.ValidateDIMACSFile(cnfPath); err != nil {
		log.Printf("enqueue job %d: invalid CNF %q: %v", jobID, cnfPath, err)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if _, err2 := s.db.ExecContext(ctx, `
            UPDATE jobs
            SET status = 'failed'
            WHERE id = $1
        `, jobID); err2 != nil {
			log.Printf("enqueue job %d: failed to mark job as failed: %v", jobID, err2)
		}

		s.mu.Lock()
		s.completedJobs[jobID] = true
		s.mu.Unlock()

		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 2) Generate cubes via pure Go splitter
	cubes, err := sat.GenerateCubesForMaxWorkersCeil(cnfPath, maxWorkers)
	if err != nil {
		log.Printf("enqueue job %d: GenerateCubes failed: %v", jobID, err)
		return
	}
	if len(cubes) == 0 {
		log.Printf("enqueue job %d: GenerateCubes returned 0 cubes", jobID)
		return
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("enqueue job %d: begin tx: %v", jobID, err)
		return
	}
	defer tx.Rollback()

	var newShards []*Shard

	for _, cube := range cubes {
		var shardID int64
		if err := tx.QueryRowContext(ctx, `
            INSERT INTO job_shards (job_id, cube_literals, status)
            VALUES ($1, $2, 'pending')
            RETURNING id
        `, jobID, pq.Array(cube)).Scan(&shardID); err != nil {
			log.Printf("enqueue job %d: insert shard: %v", jobID, err)
			return
		}

		newShards = append(newShards, &Shard{
			JobID:    jobID,
			ShardID:  shardID,
			CubeLits: cube,
		})
	}

	if err := tx.Commit(); err != nil {
		log.Printf("enqueue job %d: commit: %v", jobID, err)
		return
	}

	// 3) Add to in-memory queue
	s.mu.Lock()
	s.pending = append(s.pending, newShards...)
	s.mu.Unlock()

	log.Printf("enqueue job %d: created %d shards", jobID, len(newShards))

	// 4) Nudge all workers so idle ones pick up shards immediately
	workerIDs := s.workers.IDs()
	for _, wid := range workerIDs {
		_ = s.MaybeAssignWork(wid)
	}
}

func (s *SimpleScheduler) MaybeAssignWork(workerID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.workerShard[workerID] != nil {
		return nil // worker already busy
	}

	// Pop first shard whose job is not completed
	var sh *Shard
	for len(s.pending) > 0 {
		candidate := s.pending[0]
		s.pending = s.pending[1:]

		if s.completedJobs[candidate.JobID] {
			continue
		}
		sh = candidate
		break
	}
	if sh == nil {
		return nil
	}

	// 🔹 NEW: check worker balance before assigning work.
	okBalance, err := s.workerHasMinBalance(workerID)
	if err != nil {
		log.Printf("MaybeAssignWork: balance check failed for worker=%d: %v", workerID, err)
		// Put shard back at the front so someone else can pick it up.
		s.pending = append([]*Shard{sh}, s.pending...)
		return nil
	}
	if !okBalance {
		log.Printf("MaybeAssignWork: worker=%d below min balance, not assigning shard=%d job=%d",
			workerID, sh.ShardID, sh.JobID)
		// Put shard back at the front so a richer worker can pick it up.
		s.pending = append([]*Shard{sh}, s.pending...)
		return nil
	}

	conn, ok := s.workers.Get(workerID)
	if !ok || conn.Stream == nil {
		// Worker vanished; put shard back at front
		s.pending = append([]*Shard{sh}, s.pending...)
		return nil
	}

	assign := &pb.AssignJob{
		JobId:        sh.JobID,
		ShardId:      sh.ShardID,
		CubeLiterals: sh.CubeLits,
	}

	if err := conn.Stream.Send(&pb.ServerMessage{
		Msg: &pb.ServerMessage_AssignJob{AssignJob: assign},
	}); err != nil {
		log.Printf("assign fail → worker=%d job=%d shard=%d err=%v",
			workerID, sh.JobID, sh.ShardID, err)
		s.pending = append([]*Shard{sh}, s.pending...)
		return err
	}

	// Store worker info in shard for penalty lookups (even if worker disconnects)
	sh.AssignedWorkerID = workerID
	if conn.Info != nil {
		sh.AssignedUserID = conn.Info.UserID
	}

	s.workerShard[workerID] = sh
	log.Printf("assigned job=%d shard=%d to worker=%d (user=%d)", sh.JobID, sh.ShardID, workerID, sh.AssignedUserID)

	// Record benchmark timestamps
	workerName := ""
	if conn.Info != nil {
		workerName = fmt.Sprintf("user_%d_conn_%d", conn.Info.UserID, workerID)
	}
	go s.markShardAssigned(sh.JobID, sh.ShardID, workerName)

	return nil
}

func (s *SimpleScheduler) HandleJobResult(workerID int64, res *pb.JobResult) error {
	// Capture userID from cached shard BEFORE clearing (for penalties if worker disconnects)
	s.mu.Lock()
	cachedShard := s.workerShard[workerID]
	var assignedUserID int64
	if cachedShard != nil {
		assignedUserID = cachedShard.AssignedUserID
	}
	s.workerShard[workerID] = nil
	s.mu.Unlock()

	log.Printf("Worker %d (user=%d) finished job=%d shard=%d sat=%v info=%q",
		workerID, assignedUserID, res.JobId, res.ShardId, res.Sat, res.ModelOrCoreInfo)

	if res.Sat {
		// ----------------------
		// SAT branch
		// ----------------------

		// Parse the model string sent by the worker.
		assignment, err := parseAssignment(res.ModelOrCoreInfo)
		if err != nil {
			log.Printf("HandleJobResult: invalid SAT assignment from worker=%d: %v", workerID, err)

			// Penalize for sending a malformed model and re-enqueue the shard.
			s.penalizeWorker(assignedUserID, res.JobId, res.ShardId, "invalid_sat_assignment_parse")
			if reErr := s.ReenqueueShard(res.ShardId); reErr != nil {
				log.Printf("HandleJobResult: failed to re-enqueue shard=%d: %v", res.ShardId, reErr)
			}
			return nil
		}

		// Load CNF path + cube literals for this shard
		var cnfPath string
		var cubeLits []int32

		err = s.db.QueryRow(`
        SELECT cf.storage_path, js.cube_literals
        FROM jobs j
        JOIN cnf_files cf ON j.cnf_file_id = cf.id
        JOIN job_shards js ON js.job_id = j.id
        WHERE j.id = $1 AND js.id = $2
    `, res.JobId, res.ShardId).Scan(&cnfPath, pq.Array(&cubeLits))
		if err != nil {
			log.Printf("HandleJobResult: db lookup failed for SAT result (job=%d shard=%d worker=%d): %v",
				res.JobId, res.ShardId, workerID, err)
			// This is a server-side issue; do NOT penalize the worker here.
			return nil
		}

		// Verify SAT result using sat.VerifySatAssignment
		if err := sat.VerifySatAssignment(cnfPath, cubeLits, assignment); err != nil {
			log.Printf("INVALID SAT MODEL from worker=%d for job=%d shard=%d: %v",
				workerID, res.JobId, res.ShardId, err)

			// Penalize for a model that does not satisfy CNF ∧ cube.
			s.penalizeWorker(assignedUserID, res.JobId, res.ShardId, "invalid_sat_assignment_unsatisfied")

			// Re-enqueue shard so another worker can try it.
			if reErr := s.ReenqueueShard(res.ShardId); reErr != nil {
				log.Printf("HandleJobResult: failed to re-enqueue shard=%d: %v", res.ShardId, reErr)
			}
			return nil
		}

		// SAT verified — commit to DB
		if err := s.storeSatShardResult(res.JobId, res.ShardId, assignment); err != nil {
			log.Printf("storeSatShardResult failed (job=%d shard=%d worker=%d): %v",
				res.JobId, res.ShardId, workerID, err)
			// Do not penalize; this is a DB/server failure after a valid result.
			return nil
		}

		// Mark job completed and cancel other shards of this job.
		s.markJobCompletedAndCancelOthers(res.JobId, workerID)

		// Reward the worker that produced the winning SAT shard.
		go s.rewardWorkerForJob(res.JobId, res.ShardId, workerID)

		return nil
	}

	// ----------------------
	// UNSAT branch
	// ----------------------

	// ModelOrCoreInfo contains the DRAT file ID as a string.
	dratIDStr := strings.TrimSpace(res.ModelOrCoreInfo)
	if dratIDStr == "" {
		log.Printf("HandleJobResult: UNSAT reported but ModelOrCoreInfo empty (job=%d shard=%d worker=%d)",
			res.JobId, res.ShardId, workerID)

		// Penalize for claiming UNSAT without providing a proof.
		s.penalizeWorker(assignedUserID, res.JobId, res.ShardId, "unsat_missing_drat_id")

		// Re-enqueue shard so another worker can handle it properly.
		if reErr := s.ReenqueueShard(res.ShardId); reErr != nil {
			log.Printf("HandleJobResult: failed to re-enqueue shard=%d: %v", res.ShardId, reErr)
		}
		return nil
	}

	dratID, err := strconv.ParseInt(dratIDStr, 10, 64)
	if err != nil {
		log.Printf("HandleJobResult: invalid drat_file_id %q (job=%d shard=%d worker=%d): %v",
			dratIDStr, res.JobId, res.ShardId, workerID, err)

		// Penalize for sending a non-integer proof ID.
		s.penalizeWorker(assignedUserID, res.JobId, res.ShardId, "unsat_invalid_drat_id")

		if reErr := s.ReenqueueShard(res.ShardId); reErr != nil {
			log.Printf("HandleJobResult: failed to re-enqueue shard=%d: %v", res.ShardId, reErr)
		}
		return nil
	}

	// Look up DRAT file path in drat_files.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var dratPath string
	if err := s.db.QueryRowContext(ctx, `
        SELECT storage_path
        FROM drat_files
        WHERE id = $1
    `, dratID).Scan(&dratPath); err != nil {
		if err == sql.ErrNoRows {
			log.Printf("HandleJobResult: drat_files row not found for id=%d (job=%d shard=%d worker=%d)",
				dratID, res.JobId, res.ShardId, workerID)

			// Penalize for referencing a non-existent proof.
			s.penalizeWorker(assignedUserID, res.JobId, res.ShardId, "unsat_drat_file_not_found")

			if reErr := s.ReenqueueShard(res.ShardId); reErr != nil {
				log.Printf("HandleJobResult: failed to re-enqueue shard=%d: %v", res.ShardId, reErr)
			}
			return nil
		}
		log.Printf("HandleJobResult: db error looking up drat_files.id=%d (job=%d shard=%d worker=%d): %v",
			dratID, res.JobId, res.ShardId, workerID, err)
		// Pure DB failure; do not penalize.
		return nil
	}

	log.Printf("UNSAT reported for job=%d shard=%d with drat_file_id=%d (path=%s); verifying...",
		res.JobId, res.ShardId, dratID, dratPath)

	// VerifyUnsatShard:
	//   - runs drat-trim
	//   - updates shard_results / job_shards / job_results / jobs
	//   - penalizes & re-enqueues on invalid proof
	if err := s.VerifyUnsatShard(res.JobId, res.ShardId, dratID, assignedUserID, dratPath); err != nil {
		log.Printf("HandleJobResult: VerifyUnsatShard failed (job=%d shard=%d worker=%d): %v",
			res.JobId, res.ShardId, workerID, err)
		return nil
	}

	// At this point the UNSAT proof is valid. If this shard contributed
	// to a fully UNSAT job, rewardWorkerForJob will pay the worker once
	// (it should be idempotent / per-job).
	go s.rewardWorkerForJob(res.JobId, res.ShardId, workerID)

	return nil
}

func (s *SimpleScheduler) markShardStatus(shardID int64, status string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `
        UPDATE job_shards
        SET status = $2, updated_at = NOW()
        WHERE id = $1
    `, shardID, status); err != nil {
		log.Printf("markShardStatus(%d,%q): %v", shardID, status, err)
	}
}

// markShardAssigned records assignment timestamp, worker name, and increments attempt count.
// Also triggers markJobStarted on first shard assignment.
func (s *SimpleScheduler) markShardAssigned(jobID, shardID int64, workerName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `
        UPDATE job_shards
        SET status = 'running',
            assigned_at = NOW(),
            worker_name = $2,
            attempt_count = attempt_count + 1,
            updated_at = NOW()
        WHERE id = $1
    `, shardID, workerName); err != nil {
		log.Printf("markShardAssigned(%d): %v", shardID, err)
	}

	// Mark job as started if this is the first shard assignment
	s.markJobStarted(jobID)
}

// markJobStarted sets jobs.started_at if not already set.
func (s *SimpleScheduler) markJobStarted(jobID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `
        UPDATE jobs
        SET started_at = NOW()
        WHERE id = $1 AND started_at IS NULL
    `, jobID); err != nil {
		log.Printf("markJobStarted(%d): %v", jobID, err)
	}
}

func parseAssignment(sval string) ([]int32, error) {
	sval = strings.TrimSpace(sval)
	if sval == "" {
		return nil, fmt.Errorf("empty assignment string")
	}
	parts := strings.Fields(sval)
	out := make([]int32, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("bad literal %q: %w", p, err)
		}
		out = append(out, int32(n))
	}
	return out, nil
}

func (s *SimpleScheduler) storeSatShardResult(jobID, shardID int64, assignment []int32) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
        INSERT INTO shard_results (
            shard_id,
            is_sat,
            assignment,
            drat_file_id,
            status
        )
        VALUES ($1, TRUE, $2, NULL, 'verified')
        ON CONFLICT (shard_id) DO UPDATE
        SET is_sat = EXCLUDED.is_sat,
            assignment = EXCLUDED.assignment,
            drat_file_id = EXCLUDED.drat_file_id,
            status = EXCLUDED.status,
            updated_at = NOW()
    `, shardID, pq.Array(assignment)); err != nil {
		return fmt.Errorf("insert shard_results: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
        UPDATE job_shards
        SET status = 'completed', completed_at = NOW(), updated_at = NOW()
        WHERE id = $1
    `, shardID); err != nil {
		return fmt.Errorf("update job_shards: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
        INSERT INTO job_results (job_id, is_sat, assignment)
        VALUES ($1, TRUE, $2)
        ON CONFLICT (job_id) DO NOTHING
    `, jobID, pq.Array(assignment)); err != nil {
		return fmt.Errorf("insert job_results: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
        UPDATE jobs
        SET status = 'completed', completed_at = NOW()
        WHERE id = $1
    `, jobID); err != nil {
		return fmt.Errorf("update jobs: %w", err)
	}

	return tx.Commit()
}

func (s *SimpleScheduler) markJobCompletedAndCancelOthers(jobID int64, winningWorkerID int64) {
	s.mu.Lock()
	s.completedJobs[jobID] = true

	type target struct {
		workerID int64
	}
	var targets []target

	for wid, sh := range s.workerShard {
		if sh == nil {
			continue
		}
		if sh.JobID != jobID {
			continue
		}
		if wid == winningWorkerID {
			continue
		}
		targets = append(targets, target{workerID: wid})
	}
	s.mu.Unlock()

	if len(targets) == 0 {
		return
	}

	jobIDStr := fmt.Sprint(jobID)

	for _, t := range targets {
		conn, ok := s.workers.Get(t.workerID)
		if !ok || conn.Stream == nil {
			continue
		}
		err := conn.Stream.Send(&pb.ServerMessage{
			Msg: &pb.ServerMessage_CancelJob{
				CancelJob: &pb.CancelJob{
					JobId: jobIDStr,
				},
			},
		})
		if err != nil {
			log.Printf("CancelJob send failed → worker=%d job=%d err=%v",
				t.workerID, jobID, err)
		} else {
			log.Printf("sent CancelJob to worker=%d for job=%d", t.workerID, jobID)
		}
	}
}

func (s *SimpleScheduler) ReenqueueShard(shardID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		jobID    int64
		cnfPath  string
		cubeLits []int32
	)

	if err := s.db.QueryRowContext(ctx, `
        SELECT js.job_id,
               cf.storage_path,
               js.cube_literals
        FROM job_shards js
        JOIN jobs j ON js.job_id = j.id
        JOIN cnf_files cf ON j.cnf_file_id = cf.id
        WHERE js.id = $1
    `, shardID).Scan(&jobID, &cnfPath, pq.Array(&cubeLits)); err != nil {
		return fmt.Errorf("ReenqueueShard: lookup shard %d: %w", shardID, err)
	}

	if _, err := s.db.ExecContext(ctx, `
        UPDATE job_shards
        SET status = 'pending', updated_at = NOW()
        WHERE id = $1
    `, shardID); err != nil {
		return fmt.Errorf("ReenqueueShard: update status: %w", err)
	}

	s.mu.Lock()
	s.pending = append(s.pending, &Shard{
		JobID:    jobID,
		ShardID:  shardID,
		CubeLits: cubeLits,
	})
	s.mu.Unlock()

	log.Printf("ReenqueueShard: re-enqueued shard=%d job=%d", shardID, jobID)

	workerIDs := s.workers.IDs()
	for _, wid := range workerIDs {
		_ = s.MaybeAssignWork(wid)
	}

	return nil
}

func (s *SimpleScheduler) VerifyUnsatShard(jobID, shardID, dratFileID, userID int64, dratPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// --- 1. Load original CNF path + cube literals for THIS shard ---------------------

	var (
		cnfPath  string
		cubeLits []int32
	)

	err := s.db.QueryRowContext(ctx, `
        SELECT cf.storage_path,
               js.cube_literals
        FROM jobs j
        JOIN cnf_files cf ON j.cnf_file_id = cf.id
        JOIN job_shards js ON js.job_id = j.id
        WHERE j.id = $1 AND js.id = $2
    `, jobID, shardID).Scan(&cnfPath, pq.Array(&cubeLits))
	if err != nil {
		return fmt.Errorf("VerifyUnsatShard: lookup CNF + cube: %w", err)
	}

	// --- 2. Rebuild CNF ∧ cube exactly like the worker, then normalize it ------------

	cubedPath, err := sat.ApplyCube(cnfPath, cubeLits)
	if err != nil {
		return fmt.Errorf("VerifyUnsatShard: ApplyCube: %w", err)
	}
	defer os.Remove(cubedPath)

	solveCNF, err := sat.PrepareCNFForSolving(cubedPath)
	if err != nil {
		return fmt.Errorf("VerifyUnsatShard: PrepareCNFForSolving: %w", err)
	}
	defer os.Remove(solveCNF)

	// --- 3. Verify DRAT proof with drat-trim against the exact same CNF --------------

	if err := sat.VerifyDratProof(solveCNF, dratPath); err != nil {
		log.Printf("VerifyUnsatShard: INVALID proof for job=%d shard=%d (user=%d): %v",
			jobID, shardID, userID, err)

		// Penalize for invalid DRAT proof
		s.penalizeWorker(userID, jobID, shardID, "invalid_drat_proof")

		// Re-enqueue shard so another worker can retry it
		if reErr := s.ReenqueueShard(shardID); reErr != nil {
			log.Printf("VerifyUnsatShard: failed to re-enqueue shard %d: %v", shardID, reErr)
		}
		return err
	}

	log.Printf("VerifyUnsatShard: VALID proof for job=%d shard=%d", jobID, shardID)

	// --- 4. Commit UNSAT shard result in DB (atomic) ---------------------------------

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("VerifyUnsatShard: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Save shard result
	_, err = tx.ExecContext(ctx, `
        INSERT INTO shard_results (
            shard_id, is_sat, assignment, drat_file_id, status
        )
        VALUES ($1, FALSE, NULL, $2, 'verified')
        ON CONFLICT (shard_id) DO UPDATE
        SET is_sat = FALSE,
            assignment = NULL,
            drat_file_id = EXCLUDED.drat_file_id,
            status = 'verified',
            updated_at = NOW()
    `, shardID, dratFileID)
	if err != nil {
		return fmt.Errorf("VerifyUnsatShard: upsert shard_results: %w", err)
	}

	// Mark the shard completed
	if _, err := tx.ExecContext(ctx, `
        UPDATE job_shards
        SET status = 'completed', completed_at = NOW(), updated_at = NOW()
        WHERE id = $1
    `, shardID); err != nil {
		return fmt.Errorf("VerifyUnsatShard: update job_shards: %w", err)
	}

	// --- 5. Check whether ALL shards completed & none SAT ----------------------------

	var totalShards, completedShards, satShards int

	err = tx.QueryRowContext(ctx, `
        SELECT
            COUNT(*) AS total,
            COUNT(*) FILTER (WHERE status = 'completed') AS completed
        FROM job_shards
        WHERE job_id = $1
    `, jobID).Scan(&totalShards, &completedShards)
	if err != nil {
		return fmt.Errorf("VerifyUnsatShard: count shards: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
        SELECT COUNT(*)
        FROM shard_results sr
        JOIN job_shards js ON sr.shard_id = js.id
        WHERE js.job_id = $1 AND sr.is_sat = TRUE
    `, jobID).Scan(&satShards)
	if err != nil {
		return fmt.Errorf("VerifyUnsatShard: count SAT shards: %w", err)
	}

	// --- 6. Complete job: UNSAT ------------------------------------------------------

	if totalShards > 0 && completedShards == totalShards && satShards == 0 {
		_, err = tx.ExecContext(ctx, `
            INSERT INTO job_results (job_id, is_sat, assignment)
            VALUES ($1, FALSE, NULL)
            ON CONFLICT (job_id) DO NOTHING
        `, jobID)
		if err != nil {
			return fmt.Errorf("VerifyUnsatShard: insert job_results: %w", err)
		}

		_, err = tx.ExecContext(ctx, `
            UPDATE jobs
            SET status = 'completed', completed_at = NOW()
            WHERE id = $1
        `, jobID)
		if err != nil {
			return fmt.Errorf("VerifyUnsatShard: update jobs: %w", err)
		}

		log.Printf("VerifyUnsatShard: job=%d is fully UNSAT", jobID)

		// Mark in-memory as completed so scheduler doesn't assign more shards
		s.mu.Lock()
		s.completedJobs[jobID] = true
		s.mu.Unlock()
	}

	return tx.Commit()
}

func (s *SimpleScheduler) HandleDisconnect(workerID int64) {
	s.mu.Lock()
	shard := s.workerShard[workerID]
	s.workerShard[workerID] = nil
	s.mu.Unlock()

	if shard == nil {
		log.Printf("HandleDisconnect: worker conn=%d disconnected (no shard assigned)", workerID)
		return
	}

	log.Printf("HandleDisconnect: worker conn=%d died while running shard=%d job=%d, re-enqueuing",
		workerID, shard.ShardID, shard.JobID)

	if err := s.ReenqueueShard(shard.ShardID); err != nil {
		log.Printf("HandleDisconnect: failed to re-enqueue shard=%d: %v", shard.ShardID, err)
	}
}
