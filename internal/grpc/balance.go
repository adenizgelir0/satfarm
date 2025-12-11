// internal/grpc/balance.go
package grpc

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// ===========================
//  ECONOMY CONSTANTS
// ===========================

// You should tune these numbers yourself.
// Right now they're placeholders to make the code compile and run.

const (
	// Constant penalty per bad behavior (e.g. invalid proof).
	// Must be "high" relative to rewards; set this to whatever you want.
	workerPenaltySatc int64 = 10_000

	// Reward per variable / clause in the CNF.
	// Reward = numVars * rewardPerVarSatc + numClauses * rewardPerClauseSatc.
	rewardPerVarSatc    int64 = 10
	rewardPerClauseSatc int64 = 1

	// Minimum balance a worker must have to be assigned any work.
	// Typically set equal to workerPenaltySatc so they can afford one penalty.
	minWorkerBalanceSatc int64 = workerPenaltySatc
)

// ===========================
//  DIMACS HEADER HELPERS
// ===========================

// readDimacsHeader opens a DIMACS CNF and returns (numVars, numClauses)
// from the "p cnf <vars> <clauses>" header.
func readDimacsHeader(path string) (numVars, numClauses int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("readDimacsHeader: open: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Allow reasonably long lines.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "c") {
			continue
		}
		if strings.HasPrefix(line, "p ") {
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[0] != "p" || fields[1] != "cnf" {
				return 0, 0, fmt.Errorf("line %d: bad header %q", lineNo, line)
			}
			vars, err1 := strconv.Atoi(fields[2])
			cls, err2 := strconv.Atoi(fields[3])
			if err1 != nil || vars <= 0 {
				return 0, 0, fmt.Errorf("line %d: invalid numVars %q", lineNo, fields[2])
			}
			if err2 != nil || cls < 0 {
				return 0, 0, fmt.Errorf("line %d: invalid numClauses %q", lineNo, fields[3])
			}
			return vars, cls, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("readDimacsHeader: scan: %w", err)
	}
	return 0, 0, fmt.Errorf("readDimacsHeader: no 'p cnf' header found")
}

// ===========================
//  LOW-BALANCE CHECK
// ===========================

// workerHasMinBalance returns true if the worker's owning user has
// balance_satc >= minWorkerBalanceSatc. If minWorkerBalanceSatc <= 0,
// workers are always considered eligible.
func (s *SimpleScheduler) workerHasMinBalance(workerID int64) (bool, error) {
	if minWorkerBalanceSatc <= 0 {
		return true, nil
	}

	userID, err := s.getWorkerUserID(workerID)
	if err != nil {
		return false, fmt.Errorf("workerHasMinBalance: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var balance int64
	if err := s.db.QueryRowContext(ctx, `
        SELECT balance_satc
        FROM users
        WHERE id = $1
    `, userID).Scan(&balance); err != nil {
		if err == sql.ErrNoRows {
			return false, fmt.Errorf("workerHasMinBalance: user %d not found", userID)
		}
		return false, fmt.Errorf("workerHasMinBalance: query: %w", err)
	}

	return balance >= minWorkerBalanceSatc, nil
}

// ===========================
//  GENERIC BALANCE MUTATORS
// ===========================

// changeUserBalance atomically updates balance_satc for a single user and
// inserts a transaction row. This mirrors web.changeUserBalance.
func (s *SimpleScheduler) changeUserBalance(
	ctx context.Context,
	userID int64,
	deltaSatc int64,
	reason string,
	meta any,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("changeUserBalance: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Debit with safety
	if deltaSatc < 0 {
		res, err := tx.ExecContext(ctx, `
            UPDATE users
            SET balance_satc = balance_satc + $1
            WHERE id = $2 AND balance_satc >= $3
        `, deltaSatc, userID, -deltaSatc)
		if err != nil {
			return fmt.Errorf("changeUserBalance: debit: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("insufficient balance")
		}
	} else {
		// Credit
		if _, err := tx.ExecContext(ctx, `
            UPDATE users
            SET balance_satc = balance_satc + $1
            WHERE id = $2
        `, deltaSatc, userID); err != nil {
			return fmt.Errorf("changeUserBalance: credit: %w", err)
		}
	}

	// meta → JSONB
	var metaJSON any
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("changeUserBalance: marshal meta: %w", err)
		}
		metaJSON = b
	}

	if _, err := tx.ExecContext(ctx, `
        INSERT INTO transactions (user_id, amount_satc, reason, meta)
        VALUES ($1, $2, $3, $4)
    `, userID, deltaSatc, reason, metaJSON); err != nil {
		return fmt.Errorf("changeUserBalance: insert txn: %w", err)
	}

	return tx.Commit()
}

// transferUserBalance atomically debits `fromUser` and credits `toUser`
// in a single SQL transaction, inserting symmetric transaction rows.
// If the client does not have enough balance, it returns "insufficient balance".
func (s *SimpleScheduler) transferUserBalance(
	ctx context.Context,
	fromUserID, toUserID int64,
	amountSatc int64,
	reason string,
	meta any,
) error {
	if amountSatc <= 0 {
		return fmt.Errorf("transferUserBalance: non-positive amount %d", amountSatc)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("transferUserBalance: begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1) Debit fromUser safely
	res, err := tx.ExecContext(ctx, `
        UPDATE users
        SET balance_satc = balance_satc - $1
        WHERE id = $2 AND balance_satc >= $1
    `, amountSatc, fromUserID)
	if err != nil {
		return fmt.Errorf("transferUserBalance: debit: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("insufficient balance")
	}

	// 2) Credit toUser
	if _, err := tx.ExecContext(ctx, `
        UPDATE users
        SET balance_satc = balance_satc + $1
        WHERE id = $2
    `, amountSatc, toUserID); err != nil {
		return fmt.Errorf("transferUserBalance: credit: %w", err)
	}

	// 3) Transactions rows
	var metaJSON any
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("transferUserBalance: marshal meta: %w", err)
		}
		metaJSON = b
	}

	// fromUser (debit)
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO transactions (user_id, amount_satc, reason, meta)
        VALUES ($1, $2, $3, $4)
    `, fromUserID, -amountSatc, reason+"_debit", metaJSON); err != nil {
		return fmt.Errorf("transferUserBalance: insert from txn: %w", err)
	}

	// toUser (credit)
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO transactions (user_id, amount_satc, reason, meta)
        VALUES ($1, $2, $3, $4)
    `, toUserID, amountSatc, reason+"_credit", metaJSON); err != nil {
		return fmt.Errorf("transferUserBalance: insert to txn: %w", err)
	}

	return tx.Commit()
}

// ===========================
//  PENALTIES
// ===========================

// penalizeWorker attempts to charge workerPenaltySatc from the worker's owner.
// If balance is insufficient, nothing happens (just a log line).
func (s *SimpleScheduler) penalizeWorker(workerID, jobID, shardID int64, why string) {
	userID, err := s.getWorkerUserID(workerID)
	if err != nil {
		log.Printf("penalizeWorker: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	meta := map[string]any{
		"kind":     "worker_penalty",
		"job_id":   jobID,
		"shard_id": shardID,
		"reason":   why,
	}

	err = s.changeUserBalance(ctx, userID, -workerPenaltySatc, "worker_penalty", meta)
	if err != nil {
		if strings.Contains(err.Error(), "insufficient balance") {
			log.Printf("penalizeWorker: user=%d insufficient balance for penalty=%d",
				userID, workerPenaltySatc)
			return
		}
		log.Printf("penalizeWorker: changeUserBalance failed: %v", err)
		return
	}

	log.Printf("penalizeWorker: user=%d penalized %d satc for job=%d shard=%d (%s)",
		userID, workerPenaltySatc, jobID, shardID, why)
}

// ===========================
//  REWARDS
// ===========================

// rewardWorkerForJob pays the worker who produced the *winning SAT shard*.
// It uses transferUserBalance so the client's debit and worker's credit are atomic.
// To avoid double-paying when multiple SAT shards race, it only rewards if
// there is exactly 1 SAT shard for the job and its shard_id == winningShardID.
func (s *SimpleScheduler) rewardWorkerForJob(jobID, shardID, workerID int64) {
	workerUserID, err := s.getWorkerUserID(workerID)
	if err != nil {
		log.Printf("rewardWorkerForJob: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 0) Has this particular shard already been rewarded?
	var alreadyPaid int
	if err := s.db.QueryRowContext(ctx, `
        SELECT COUNT(*)
        FROM transactions
        WHERE reason = 'worker_reward'
          AND meta->>'job_id'   = $1::text
          AND meta->>'shard_id' = $2::text
    `, fmt.Sprint(jobID), fmt.Sprint(shardID)).Scan(&alreadyPaid); err != nil {
		log.Printf("rewardWorkerForJob: check alreadyPaid failed job=%d shard=%d: %v",
			jobID, shardID, err)
		return
	}
	if alreadyPaid > 0 {
		log.Printf("rewardWorkerForJob: job=%d shard=%d already rewarded, skipping", jobID, shardID)
		return
	}

	// 1) Identify client and CNF path (per job)
	var (
		clientUserID int64
		cnfPath      string
	)

	if err := s.db.QueryRowContext(ctx, `
        SELECT j.user_id, cf.storage_path
        FROM jobs j
        JOIN cnf_files cf ON j.cnf_file_id = cf.id
        WHERE j.id = $1
    `, jobID).Scan(&clientUserID, &cnfPath); err != nil {
		log.Printf("rewardWorkerForJob: job lookup failed job=%d shard=%d: %v",
			jobID, shardID, err)
		return
	}

	// 2) Compute reward from DIMACS header (same for SAT & UNSAT)
	numVars, numClauses, err := readDimacsHeader(cnfPath)
	if err != nil {
		log.Printf("rewardWorkerForJob: readDimacsHeader failed job=%d shard=%d: %v",
			jobID, shardID, err)
		return
	}

	reward := int64(numVars)*rewardPerVarSatc + int64(numClauses)*rewardPerClauseSatc
	if reward <= 0 {
		log.Printf("rewardWorkerForJob: non-positive reward for job=%d shard=%d (vars=%d clauses=%d)",
			jobID, shardID, numVars, numClauses)
		return
	}

	meta := map[string]any{
		"kind":        "worker_reward",
		"job_id":      jobID,
		"shard_id":    shardID,
		"worker_user": workerUserID,
		"client_user": clientUserID,
		"num_vars":    numVars,
		"num_clauses": numClauses,
		"reward_satc": reward,
	}

	// 3) Do atomic transfer client → worker
	if err := s.transferUserBalance(ctx, clientUserID, workerUserID, reward, "worker_reward", meta); err != nil {
		if strings.Contains(err.Error(), "insufficient balance") {
			log.Printf("rewardWorkerForJob: client user=%d lacks funds for reward=%d job=%d shard=%d",
				clientUserID, reward, jobID, shardID)
			return
		}
		log.Printf("rewardWorkerForJob: transferUserBalance failed job=%d shard=%d: %v",
			jobID, shardID, err)
		return
	}

	log.Printf("rewardWorkerForJob: PAID job=%d shard=%d reward=%d satc (client=%d → worker_user=%d) vars=%d clauses=%d",
		jobID, shardID, reward, clientUserID, workerUserID, numVars, numClauses)
}
