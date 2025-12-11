package sat

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// VerifySatAssignment checks whether a literal assignment satisfies:
//
//	(1) the cube literals
//	(2) the original CNF file at cnfPath
//
// Returns nil if satisfied; otherwise returns an error describing the failure.
//
// assignment is a slice of signed literals like: []int32{1, -3, 5, ...}
func VerifySatAssignment(cnfPath string, cube []int32, assignment []int32) error {
	// Build assignment map: var -> bool
	assign := make(map[int]bool, len(assignment))

	for _, lit := range assignment {
		if lit == 0 {
			continue
		}
		if lit > 0 {
			assign[int(lit)] = true
		} else {
			assign[int(-lit)] = false
		}
	}

	// --- 1. Verify cube literals --------------------------------------------
	for _, cl := range cube {
		if cl > 0 {
			// literal must be true
			if v, ok := assign[int(cl)]; ok && !v {
				return fmt.Errorf("cube literal %d violated", cl)
			}
		} else { // cl < 0
			// literal must be false
			if v, ok := assign[int(-cl)]; ok && v {
				return fmt.Errorf("cube literal %d violated", cl)
			}
		}
	}

	// --- 2. Verify each clause in the CNF ------------------------------------
	f, err := os.Open(cnfPath)
	if err != nil {
		return fmt.Errorf("VerifySatAssignment: open CNF: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		lineNo++

		// Skip comments, header, blank lines
		if line == "" || strings.HasPrefix(line, "c") {
			continue
		}
		if strings.HasPrefix(line, "p") {
			continue
		}
		if strings.HasPrefix(line, "%") {
			// DIMACS "EOF" marker
			break
		}

		// Clause
		toks := strings.Fields(line)
		satisfied := false

		for _, tok := range toks {
			lit, err := strconv.Atoi(tok)
			if err != nil {
				return fmt.Errorf("line %d: invalid literal %q", lineNo, tok)
			}
			if lit == 0 {
				break
			}

			abs := lit
			if abs < 0 {
				abs = -abs
			}

			val, ok := assign[abs]
			if !ok {
				// unassigned → treated as false but harmless
				continue
			}

			if (lit > 0 && val) || (lit < 0 && !val) {
				satisfied = true
				break
			}
		}

		if !satisfied {
			return fmt.Errorf("clause at line %d is unsatisfied by assignment", lineNo)
		}
	}

	if err := sc.Err(); err != nil {
		return fmt.Errorf("VerifySatAssignment: scanner error: %w", err)
	}

	return nil
}
