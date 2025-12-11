package sat

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// RunKissat runs `kissat --relaxed <cnfPath>` and parses SAT/UNSAT + model.
// IMPORTANT: cnfPath is assumed to already be preprocessed (e.g. via PrepareCNFForSolving).
// It respects ctx: cancelling ctx will kill the underlying process.
func RunKissat(ctx context.Context, cnfPath string) (bool, string, error) {
	cmd := exec.CommandContext(ctx, "kissat", "--relaxed", cnfPath)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	if err != nil {
		// If context was canceled, treat this as a cancellation, not a hard error.
		if ctx.Err() == context.Canceled {
			log.Printf("kissat cancelled for %s (duration=%s)", cnfPath, dur)
			return false, "", ctx.Err()
		}
		log.Printf("kissat finished with error: %v\noutput:\n%s", err, out.String())
	}

	status, model, perr := ParseKissatOutput(out.String())
	if perr != nil {
		return false, "", fmt.Errorf("parse kissat: %w", perr)
	}

	log.Printf("kissat(%s) finished in %s: %s", cnfPath, dur, status)

	switch status {
	case "sat":
		return true, model, nil
	case "unsat":
		return false, "", nil
	default:
		return false, "", fmt.Errorf("unknown status from kissat")
	}
}

// RunKissatDRAT runs `kissat --relaxed <cnfPath> <proofPath>` to produce a DRAT proof.
// It respects ctx: cancelling ctx will kill the underlying process.
// IMPORTANT: cnfPath is assumed to already be preprocessed (e.g. via PrepareCNFForSolving).
func RunKissatDRAT(ctx context.Context, cnfPath string) (string, error) {
	tmp, err := os.CreateTemp("", "satfarm-proof-*.drat")
	if err != nil {
		return "", fmt.Errorf("CreateTemp drat: %w", err)
	}
	proofPath := tmp.Name()
	tmp.Close()

	cmd := exec.CommandContext(ctx, "kissat", "--relaxed", cnfPath, proofPath)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		// If context was canceled, treat as cancellation
		if ctx.Err() == context.Canceled {
			log.Printf("kissat DRAT cancelled for %s", cnfPath)
			return "", ctx.Err()
		}
		log.Printf("kissat DRAT exited: %v output:\n%s", err, out.String())
	}

	if _, statErr := os.Stat(proofPath); statErr != nil {
		return "", fmt.Errorf("no proof generated: %w", statErr)
	}

	return proofPath, nil
}

func ParseKissatOutput(out string) (status string, model string, err error) {
	status = "unknown"

	lines := strings.Split(out, "\n")
	var modelLits []int

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "s ") {
			if strings.Contains(line, "UNSAT") {
				status = "unsat"
			} else if strings.Contains(line, "SAT") {
				status = "sat"
			}
		}

		if strings.HasPrefix(line, "v ") {
			fields := strings.Fields(line)[1:]
			for _, f := range fields {
				n, err := strconv.Atoi(f)
				if err == nil && n != 0 {
					modelLits = append(modelLits, n)
				}
			}
		}
	}

	if status == "unknown" {
		return "", "", fmt.Errorf("no s-line found in kissat output")
	}

	if status == "sat" {
		parts := make([]string, len(modelLits))
		for i, lit := range modelLits {
			parts[i] = strconv.Itoa(lit)
		}
		return "sat", strings.Join(parts, " "), nil
	}

	return "unsat", "", nil
}
