// internal/sat/dimacs.go
package sat

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ValidateDIMACSFile opens the given path and checks that it contains a
// syntactically valid DIMACS CNF file. If not, it returns an error.
func ValidateDIMACSFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	return ValidateDIMACS(f)
}

func ValidateDIMACS(r io.Reader) error {
	sc := bufio.NewScanner(r)
	// Allow reasonably large lines; adjust if your CNFs are extreme.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		seenHeader  bool
		numVars     int
		numClauses  int
		seenClauses int
		lineNo      int
	)

	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())

		// Empty lines, 'c' comments, and '%' EOF/comment lines
		if line == "" || strings.HasPrefix(line, "c") || strings.HasPrefix(line, "%") {
			continue
		}

		if !seenHeader {
			// Expect a header line: p cnf <vars> <clauses>
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[0] != "p" || fields[1] != "cnf" {
				return fmt.Errorf("line %d: expected 'p cnf <vars> <clauses>', got %q", lineNo, line)
			}
			var err error
			numVars, err = strconv.Atoi(fields[2])
			if err != nil || numVars <= 0 {
				return fmt.Errorf("line %d: invalid num_vars %q", lineNo, fields[2])
			}
			numClauses, err = strconv.Atoi(fields[3])
			if err != nil || numClauses < 0 {
				return fmt.Errorf("line %d: invalid num_clauses %q", lineNo, fields[3])
			}
			seenHeader = true
			continue
		}

		// If we've already seen at least numClauses clauses, ignore any
		// additional clause-looking lines (some benchmarks have a trailing "0").
		if numClauses > 0 && seenClauses >= numClauses {
			continue
		}

		// Clause line: sequence of ints ending with 0
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		var hasTerminator bool
		for i, tok := range fields {
			val, err := strconv.Atoi(tok)
			if err != nil {
				return fmt.Errorf("line %d: non-integer token %q", lineNo, tok)
			}
			if val == 0 {
				hasTerminator = true
				if i != len(fields)-1 {
					return fmt.Errorf("line %d: extra tokens after 0", lineNo)
				}
				break
			}
			if val < 0 {
				val = -val
			}
			if val < 1 || val > numVars {
				return fmt.Errorf("line %d: literal %d out of range 1..%d", lineNo, val, numVars)
			}
		}
		if !hasTerminator {
			return fmt.Errorf("line %d: clause missing terminating 0", lineNo)
		}
		seenClauses++
	}

	if err := sc.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}
	if !seenHeader {
		return fmt.Errorf("missing 'p cnf' header")
	}
	// Only require that we have AT LEAST the declared number of clauses.
	if numClauses > 0 && seenClauses < numClauses {
		return fmt.Errorf("clause count mismatch: header says %d, saw %d", numClauses, seenClauses)
	}

	return nil
}

func PrepareCNFForSolving(origPath string) (string, error) {
	in, err := os.Open(origPath)
	if err != nil {
		return "", fmt.Errorf("PrepareCNFForSolving: open: %w", err)
	}
	defer in.Close()

	out, err := os.CreateTemp("", "satfarm-solve-*.cnf")
	if err != nil {
		return "", fmt.Errorf("PrepareCNFForSolving: CreateTemp: %w", err)
	}
	outPath := out.Name()
	defer out.Close()

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		trim := strings.TrimSpace(line)

		if strings.HasPrefix(trim, "%") {
			// Logical end-of-file: ignore '%' and everything after it.
			break
		}

		if _, err := fmt.Fprintln(out, line); err != nil {
			return "", fmt.Errorf("PrepareCNFForSolving: write: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("PrepareCNFForSolving: scan: %w", err)
	}

	return outPath, nil
}

// ReadDimacsHeader opens a DIMACS CNF file and returns (numVars, numClauses)
// as declared in the 'p cnf <vars> <clauses>' line.
func ReadDimacsHeader(path string) (numVars, numClauses int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("ReadDimacsHeader: open: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "c") {
			continue
		}
		if strings.HasPrefix(line, "p") {
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[0] != "p" || fields[1] != "cnf" {
				return 0, 0, fmt.Errorf("ReadDimacsHeader: line %d: malformed header %q", lineNo, line)
			}
			v, err := strconv.Atoi(fields[2])
			if err != nil || v <= 0 {
				return 0, 0, fmt.Errorf("ReadDimacsHeader: line %d: bad numVars %q", lineNo, fields[2])
			}
			c, err := strconv.Atoi(fields[3])
			if err != nil || c < 0 {
				return 0, 0, fmt.Errorf("ReadDimacsHeader: line %d: bad numClauses %q", lineNo, fields[3])
			}
			return v, c, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("ReadDimacsHeader: scanner: %w", err)
	}
	return 0, 0, fmt.Errorf("ReadDimacsHeader: no 'p cnf' header found")
}
