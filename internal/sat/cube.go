package sat

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readNumVars reads the "p cnf <vars> <clauses>" line and returns <vars>.
func readNumVars(cnfPath string) (int, error) {
	f, err := os.Open(cnfPath)
	if err != nil {
		return 0, fmt.Errorf("open cnf: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "p") {
			// p cnf <vars> <clauses>
			fields := strings.Fields(line)
			if len(fields) < 4 {
				return 0, fmt.Errorf("malformed p-line: %q", line)
			}
			return strconv.Atoi(fields[2]) // numVars
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no p-line found in %s", cnfPath)
}

// chooseDepthCeil picks depth such that 2^depth is the smallest power of two
// that is >= maxWorkers, but also depth <= numVars and depth <= maxDepthCap.
// If we hit numVars or maxDepthCap before reaching maxWorkers, we stop there.
func chooseDepthCeil(maxWorkers, numVars, maxDepthCap int) int {
	if maxWorkers <= 1 || numVars <= 0 {
		return 0
	}
	if maxDepthCap <= 0 {
		maxDepthCap = 30
	}

	depth := 0
	shards := 1

	for shards < maxWorkers {
		if depth+1 > numVars {
			// Can't branch on more variables, stop here.
			break
		}
		if depth+1 > maxDepthCap {
			// Depth cap reached, stop here.
			break
		}
		depth++
		shards <<= 1 // shards *= 2
	}

	return depth
}

// GenerateCubesForMaxWorkersCeil generates cubes so that the number of cubes
// is 2^depth where 2^depth is the smallest power of two >= maxWorkers,
// subject to depth <= numVars and depth <= maxDepthCap.
//
// If maxWorkers <= 1 (or not enough vars), returns a single empty cube.
func GenerateCubesForMaxWorkersCeil(cnfPath string, maxWorkers int) ([][]int32, error) {
	numVars, err := readNumVars(cnfPath)
	if err != nil {
		return nil, err
	}

	if maxWorkers <= 1 || numVars == 0 {
		// Single shard, no assumptions.
		return [][]int32{{}}, nil
	}

	const maxDepthCap = 20
	depth := chooseDepthCeil(maxWorkers, numVars, maxDepthCap)
	if depth == 0 {
		// Could not increase depth (e.g. numVars=0 or caps), single shard.
		return [][]int32{{}}, nil
	}

	numCubes := 1 << depth // 2^depth

	// Branch on variables 1..depth.
	vars := make([]int32, depth)
	for i := 0; i < depth; i++ {
		vars[i] = int32(i + 1)
	}

	cubes := make([][]int32, 0, numCubes)
	for mask := 0; mask < numCubes; mask++ {
		cube := make([]int32, 0, depth)
		for i, v := range vars {
			if (mask & (1 << i)) != 0 {
				cube = append(cube, v) // v = true
			} else {
				cube = append(cube, -v) // v = false
			}
		}
		cubes = append(cubes, cube)
	}

	return cubes, nil
}
