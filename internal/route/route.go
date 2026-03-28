package route

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/fitbase/fitbase/internal/models"
)

// Cell is a ~100m grid cell (0.001° resolution).
type Cell struct {
	Lat int
	Lng int
}

// Candidate is a stored route used for matching.
type Candidate struct {
	ID    string
	Cells []Cell
}

// ComputeCells extracts the set of unique grid cells from a GPS trace.
// Returns nil if fewer than 5 unique cells (too short to be a meaningful route).
func ComputeCells(streams []models.Stream) []Cell {
	seen := make(map[Cell]struct{})
	for _, s := range streams {
		if s.Lat == nil || s.Lng == nil {
			continue
		}
		c := Cell{
			Lat: int(math.Floor(*s.Lat * 1000)),
			Lng: int(math.Floor(*s.Lng * 1000)),
		}
		seen[c] = struct{}{}
	}
	if len(seen) < 5 {
		return nil
	}
	cells := make([]Cell, 0, len(seen))
	for c := range seen {
		cells = append(cells, c)
	}
	sortCells(cells)
	return cells
}

// CellsToString serializes a sorted cell slice for DB storage.
func CellsToString(cells []Cell) string {
	parts := make([]string, len(cells))
	for i, c := range cells {
		parts[i] = fmt.Sprintf("%d:%d", c.Lat, c.Lng)
	}
	return strings.Join(parts, ",")
}

// CellsFromString parses a stored cell string back into a slice.
func CellsFromString(s string) []Cell {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	cells := make([]Cell, 0, len(parts))
	for _, p := range parts {
		pair := strings.SplitN(p, ":", 2)
		if len(pair) != 2 {
			continue
		}
		lat, err1 := strconv.Atoi(pair[0])
		lng, err2 := strconv.Atoi(pair[1])
		if err1 != nil || err2 != nil {
			continue
		}
		cells = append(cells, Cell{Lat: lat, Lng: lng})
	}
	return cells
}

// CellSetID returns a deterministic ID for a sorted cell set (SHA-256, first 16 hex chars).
func CellSetID(cells []Cell) string {
	h := sha256.Sum256([]byte(CellsToString(cells)))
	return fmt.Sprintf("%x", h[:8])
}

// Similarity computes the Jaccard similarity between two sorted cell slices.
func Similarity(a, b []Cell) float64 {
	setA := make(map[Cell]struct{}, len(a))
	for _, c := range a {
		setA[c] = struct{}{}
	}
	intersection := 0
	for _, c := range b {
		if _, ok := setA[c]; ok {
			intersection++
		}
	}
	union := len(setA) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// MatchRoute finds the best matching route above the given threshold.
// Returns the route ID and similarity, or empty string if no match.
func MatchRoute(cells []Cell, candidates []Candidate, threshold float64) (string, float64) {
	var bestID string
	var bestSim float64
	for _, c := range candidates {
		sim := Similarity(cells, c.Cells)
		if sim >= threshold && sim > bestSim {
			bestSim = sim
			bestID = c.ID
		}
	}
	return bestID, bestSim
}

func sortCells(cells []Cell) {
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Lat != cells[j].Lat {
			return cells[i].Lat < cells[j].Lat
		}
		return cells[i].Lng < cells[j].Lng
	})
}
