// internal/sat/drat.go
package sat

import (
	"fmt"
	"os"
	"os/exec"
)

// dratTrimPath returns the path to drat-trim, using the DRAT_TRIM_PATH
// environment variable if set, otherwise falling back to "drat-trim".
func dratTrimPath() string {
	if path := os.Getenv("DRAT_TRIM_PATH"); path != "" {
		return path
	}
	return "drat-trim"
}

// VerifyDratProof runs `drat-trim <cnfPath> <dratPath>` and returns an error
// if verification fails. Any output from drat-trim is included in the error.
func VerifyDratProof(cnfPath, dratPath string) error {
	cmd := exec.Command(dratTrimPath(), cnfPath, dratPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("drat-trim failed: %w; output: %s", err, string(out))
	}
	return nil
}
