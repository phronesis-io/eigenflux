package dal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SnapshotDir is the directory where dashboard snapshot files are stored.
var SnapshotDir = "data/dashboard"

// InitSnapshotDir creates the snapshot directory if it doesn't exist.
func InitSnapshotDir() error {
	return os.MkdirAll(SnapshotDir, 0o755)
}

// Snapshot represents a dashboard snapshot read from disk.
type Snapshot struct {
	SnapshotID int64           `json:"snapshot_id"`
	CreatedAt  int64           `json:"created_at"`
	Data       json.RawMessage `json:"data"`
}

// SnapshotSummary is a snapshot without the full data payload.
type SnapshotSummary struct {
	SnapshotID int64 `json:"snapshot_id"`
	CreatedAt  int64 `json:"created_at"`
}

// TrendPoint holds scalar metrics extracted from a snapshot for trend charts.
type TrendPoint struct {
	SnapshotID   int64   `json:"snapshot_id"`
	CreatedAt    int64   `json:"created_at"`
	TotalItems   int64   `json:"total_items"`
	ActiveItems  int64   `json:"active_items"`
	TotalUsers   int64   `json:"total_users"`
	AvgQuality   float64 `json:"avg_quality_score"`
	OverlapCount int     `json:"overlap_count"`
	SupplyOnly   int     `json:"supply_only_count"`
	DemandOnly   int     `json:"demand_only_count"`
}

// CreateSnapshot writes a snapshot JSON file. Returns the snapshot ID (= createdAt).
func CreateSnapshot(data json.RawMessage, createdAt int64) (int64, error) {
	if err := InitSnapshotDir(); err != nil {
		return 0, fmt.Errorf("init snapshot dir: %w", err)
	}
	path := filepath.Join(SnapshotDir, fmt.Sprintf("%d.json", createdAt))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, fmt.Errorf("write snapshot: %w", err)
	}
	return createdAt, nil
}

// GetLatestSnapshot returns the most recent snapshot.
func GetLatestSnapshot() (*Snapshot, error) {
	ids, err := listSnapshotIDs()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no snapshots found")
	}
	return readSnapshot(ids[len(ids)-1])
}

// GetSnapshotByID returns a single snapshot by its ID (timestamp).
func GetSnapshotByID(id int64) (*Snapshot, error) {
	return readSnapshot(id)
}

// ListSnapshotSummaries returns paginated snapshot summaries (no data field).
func ListSnapshotSummaries(page, pageSize int32) ([]SnapshotSummary, int64, error) {
	ids, err := listSnapshotIDs()
	if err != nil {
		return nil, 0, err
	}

	total := int64(len(ids))

	// Reverse for DESC order (newest first)
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}

	offset := int((page - 1) * pageSize)
	if offset >= len(ids) {
		return nil, total, nil
	}
	end := offset + int(pageSize)
	if end > len(ids) {
		end = len(ids)
	}

	summaries := make([]SnapshotSummary, 0, end-offset)
	for _, id := range ids[offset:end] {
		summaries = append(summaries, SnapshotSummary{
			SnapshotID: id,
			CreatedAt:  id,
		})
	}
	return summaries, total, nil
}

// GetTrendPoints returns scalar metrics from all snapshots for trend line charts.
func GetTrendPoints() ([]TrendPoint, error) {
	ids, err := listSnapshotIDs()
	if err != nil {
		return nil, err
	}

	points := make([]TrendPoint, 0, len(ids))
	for _, id := range ids {
		data, err := os.ReadFile(filepath.Join(SnapshotDir, fmt.Sprintf("%d.json", id)))
		if err != nil {
			continue
		}
		var parsed struct {
			Summary struct {
				TotalItems  int64   `json:"total_items"`
				ActiveItems int64   `json:"active_items"`
				TotalUsers  int64   `json:"total_users"`
				AvgQuality  float64 `json:"avg_quality_score"`
			} `json:"summary"`
			KeywordAnalysis struct {
				Overlap    []json.RawMessage `json:"overlap"`
				SupplyOnly []json.RawMessage `json:"supply_only"`
				DemandOnly []json.RawMessage `json:"demand_only"`
			} `json:"keyword_analysis"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		points = append(points, TrendPoint{
			SnapshotID:   id,
			CreatedAt:    id,
			TotalItems:   parsed.Summary.TotalItems,
			ActiveItems:  parsed.Summary.ActiveItems,
			TotalUsers:   parsed.Summary.TotalUsers,
			AvgQuality:   parsed.Summary.AvgQuality,
			OverlapCount: len(parsed.KeywordAnalysis.Overlap),
			SupplyOnly:   len(parsed.KeywordAnalysis.SupplyOnly),
			DemandOnly:   len(parsed.KeywordAnalysis.DemandOnly),
		})
	}
	return points, nil
}

// DeleteSnapshot removes a snapshot file.
func DeleteSnapshot(id int64) error {
	path := filepath.Join(SnapshotDir, fmt.Sprintf("%d.json", id))
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("snapshot not found")
		}
		return err
	}
	return nil
}

// listSnapshotIDs returns all snapshot IDs sorted ascending.
func listSnapshotIDs() ([]int64, error) {
	entries, err := os.ReadDir(SnapshotDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		id, err := strconv.ParseInt(name, 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func readSnapshot(id int64) (*Snapshot, error) {
	path := filepath.Join(SnapshotDir, fmt.Sprintf("%d.json", id))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("snapshot not found")
		}
		return nil, err
	}
	return &Snapshot{
		SnapshotID: id,
		CreatedAt:  id,
		Data:       json.RawMessage(data),
	}, nil
}
