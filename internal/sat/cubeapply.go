package sat

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ApplyCube takes an existing DIMACS CNF at origPath and produces a new CNF file
// which is logically "orig ∧ cube". This fixed version also updates the DIMACS
// header clause count so drat-trim and kissat see the same formula.
func ApplyCube(origPath string, cube []int32) (string, error) {
	in, err := os.Open(origPath)
	if err != nil {
		return "", fmt.Errorf("ApplyCube: open orig: %w", err)
	}
	defer in.Close()

	scanner := bufio.NewScanner(in)

	var (
		lines         []string
		headerIndex   = -1
		headerNumVars int
		actualClauses int
	)

	// --- First pass: read lines, locate header, count clauses ---
	for scanner.Scan() {
		line := scanner.Text()
		trim := strings.TrimSpace(line)

		// Stop at DIMACS trailer if present (may not exist if you pre-cleaned CNFs)
		if strings.HasPrefix(trim, "%") {
			break
		}

		// Detect header
		if headerIndex == -1 {
			fields := strings.Fields(trim)
			if len(fields) == 4 && fields[0] == "p" && fields[1] == "cnf" {
				headerIndex = len(lines)

				// Parse num_vars (keep safe — if it fails we leave header untouched)
				if v, err := strconv.Atoi(fields[2]); err == nil {
					headerNumVars = v
				}

				// Do *not* store clause count — we recompute it from the file
			}
		}

		// Count clauses by looking for lines ending in "0" that aren't comments/header
		if trim != "" && !strings.HasPrefix(trim, "c") {
			fields := strings.Fields(trim)
			if len(fields) > 0 && fields[len(fields)-1] == "0" && !strings.HasPrefix(trim, "p ") {
				actualClauses++
			}
		}

		lines = append(lines, line)
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("ApplyCube: scan: %w", err)
	}

	// --- Fix header: update clause count ---
	if headerIndex != -1 && headerNumVars > 0 {
		newClauseCount := actualClauses + len(cube)
		lines[headerIndex] = fmt.Sprintf("p cnf %d %d", headerNumVars, newClauseCount)
	}

	// --- Add cube as unit clauses ---
	for _, lit := range cube {
		lines = append(lines, fmt.Sprintf("%d 0", lit))
	}

	// --- Write output file ---
	out, err := os.CreateTemp("", "satfarm-cube-*.cnf")
	if err != nil {
		return "", fmt.Errorf("ApplyCube: CreateTemp: %w", err)
	}
	outPath := out.Name()
	defer out.Close()

	w := bufio.NewWriter(out)
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return "", fmt.Errorf("ApplyCube: write: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return "", fmt.Errorf("ApplyCube: flush: %w", err)
	}

	return outPath, nil
}
