package evidence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Autumn-27/artex/db"
)

// ObservationSnapshot resolves a task-owned observation to immutable CAS
// metadata. Callers cannot select a global snapshot ID through this API.
func (s *Store) ObservationSnapshot(ctx context.Context, taskID, nodeID int64) (out db.TrafficEvidenceSnapshot, err error) {
	err = s.DB.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT n.payload FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.deleted_at IS NULL AND n.id=$2 AND n.kind='observation'`, taskID, nodeID).Scan(&raw); err != nil {
			return err
		}
		var p db.ObservationPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if p.SourceKind != "traffic_snapshot" {
			return sql.ErrNoRows
		}
		if err := db.ValidateObservationSource(p); err != nil {
			return err
		}
		var recorded db.TrafficEvidenceSnapshot
		if err := json.Unmarshal(p.SourceSnapshot, &recorded); err != nil {
			return err
		}
		if recorded.ID != p.SourceID || db.TrafficSnapshotID(recorded) != p.SourceID || !p.SourceTime.Equal(time.Unix(recorded.CapturedAt, 0).UTC()) {
			return errors.New("observation snapshot identity/time mismatch")
		}
		if err := tx.QueryRowContext(ctx, `SELECT to_jsonb(s) FROM traffic_evidence_snapshots s WHERE s.id=$1`, p.SourceID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		if out.ID != recorded.ID || db.TrafficSnapshotID(out) != out.ID {
			return errors.New("persistent observation snapshot integrity failure")
		}
		return nil
	})
	return
}
