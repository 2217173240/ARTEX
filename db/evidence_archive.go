package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

type archivedEvidenceNode struct {
	ID            int64           `json:"id"`
	ExplorationID int64           `json:"exploration_id"`
	Kind          string          `json:"kind"`
	State         string          `json:"state"`
	Payload       json.RawMessage `json:"payload"`
	Version       int             `json:"content_version"`
}

type archivedEvidenceEdge struct {
	ExplorationID int64  `json:"exploration_id"`
	From          int64  `json:"src_id"`
	To            int64  `json:"dst_id"`
	Rel           string `json:"rel"`
}

// Validate the new evidence subgraph as a closed unit before committing a
// restore. Legacy derived_from edges may reference a directly related task;
// evidence supports/qualifies edges never acquire that cross-task authority.
func validateArchivedEvidence(snapshot *TaskArchiveSnapshot) error {
	var nodes []archivedEvidenceNode
	if raw := snapshot.Tables["exploration_nodes"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &nodes); err != nil {
			return err
		}
	}
	local := map[int64]archivedEvidenceNode{}
	for _, n := range nodes {
		if n.ExplorationID == snapshot.ExplorationID {
			local[n.ID] = n
		}
	}
	var edges []archivedEvidenceEdge
	if raw := snapshot.Tables["exploration_edges"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &edges); err != nil {
			return err
		}
	}
	links := map[string]bool{}
	key := func(from int64, rel string, to int64) string { return fmt.Sprintf("%d/%s/%d", from, rel, to) }
	for _, e := range edges {
		if e.Rel == RelSupports || e.Rel == RelQualifies {
			if e.ExplorationID != snapshot.ExplorationID || local[e.From].ID == 0 || local[e.To].ID == 0 {
				return errors.New("archived evidence edge references node outside task")
			}
		}
		links[key(e.From, e.Rel, e.To)] = true
	}
	observations := map[int64]ObservationPayload{}
	gates := map[int64]QualificationPayload{}
	for _, n := range nodes {
		if n.Kind != KindObservation && n.Kind != KindValidityGate {
			continue
		}
		if n.ExplorationID != snapshot.ExplorationID {
			return errors.New("archived evidence node exploration mismatch")
		}
		if n.Kind == KindObservation {
			var o ObservationPayload
			if err := json.Unmarshal(n.Payload, &o); err != nil {
				return err
			}
			if err := ValidateObservationSource(o); err != nil {
				return err
			}
			if local[o.IntentID].Kind != KindIntent || !links[key(o.IntentID, RelYields, n.ID)] {
				return errors.New("archived observation missing local intent binding")
			}
			observations[n.ID] = o
		} else {
			var p QualificationPayload
			if err := json.Unmarshal(n.Payload, &p); err != nil {
				return err
			}
			gates[n.ID] = p
		}
	}
	for id, p := range gates {
		if local[p.IntentID].Kind != KindIntent || !links[key(p.IntentID, RelYields, id)] {
			return errors.New("archived audit missing local intent binding")
		}
		in, err := normalizedClaim(QualificationInput{IntentID: p.IntentID, Summary: p.Summary, Detail: p.Detail, ConclusionKind: p.ConclusionKind, ContextKey: p.ContextKey, ObservationIDs: p.ObservationIDs})
		if err != nil {
			return err
		}
		refs := make([]ObservationVersion, 0, len(in.ObservationIDs))
		for _, oid := range in.ObservationIDs {
			o, ok := observations[oid]
			if !ok || o.IntentID != p.IntentID || o.ContextKey != p.ContextKey {
				return errors.New("archived audit observation context/task mismatch")
			}
			if !links[key(oid, RelSupports, id)] {
				return errors.New("archived audit missing observation support")
			}
			n := local[oid]
			refs = append(refs, ObservationVersion{ID: oid, Version: n.Version, Hash: evidenceHash(n.Payload), SourceTime: o.SourceTime})
		}
		if !reflect.DeepEqual(refs, p.Observations) || claimHash(in, refs) != p.ClaimHash || p.SemanticStatus != "unknown" || p.Qualification != local[id].State {
			return errors.New("archived audit claim/version/hash mismatch")
		}
	}
	type factPayload struct {
		Summary, Detail string
		ContextKey      string  `json:"context_key"`
		ConclusionKind  string  `json:"conclusion_kind"`
		ObservationIDs  []int64 `json:"observation_ids"`
		AuditID         int64   `json:"audit_id"`
		ClaimHash       string  `json:"claim_hash"`
		Qualification   string  `json:"qualification"`
		SemanticStatus  string  `json:"semantic_status"`
	}
	facts := map[int64]factPayload{}
	used := map[int64]bool{}
	for _, n := range nodes {
		if n.Kind != KindFact {
			continue
		}
		var p factPayload
		if err := json.Unmarshal(n.Payload, &p); err != nil {
			return err
		}
		if p.AuditID == 0 && len(p.ObservationIDs) == 0 && p.ConclusionKind == "" {
			continue
		}
		if n.ExplorationID != snapshot.ExplorationID {
			return errors.New("archived structured fact exploration mismatch")
		}
		gate, ok := gates[p.AuditID]
		if !ok || used[p.AuditID] {
			return errors.New("archived fact audit missing, foreign or reused")
		}
		used[p.AuditID] = true
		in, err := normalizedClaim(QualificationInput{IntentID: gate.IntentID, Summary: p.Summary, Detail: p.Detail, ConclusionKind: p.ConclusionKind, ContextKey: p.ContextKey, ObservationIDs: p.ObservationIDs})
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(in.ObservationIDs, gate.ObservationIDs) || claimHash(in, gate.Observations) != gate.ClaimHash || p.ClaimHash != gate.ClaimHash || p.Qualification != gate.Qualification || p.SemanticStatus != "unknown" {
			return errors.New("archived fact claim/audit binding mismatch")
		}
		if !links[key(p.AuditID, RelQualifies, n.ID)] || !links[key(gate.IntentID, RelYields, n.ID)] {
			return errors.New("archived fact missing audit/intent binding")
		}
		for _, oid := range in.ObservationIDs {
			if !links[key(oid, RelSupports, n.ID)] {
				return errors.New("archived fact missing observation support")
			}
		}
		facts[n.ID] = p
	}
	for _, e := range edges {
		switch e.Rel {
		case RelSupports:
			if _, ok := observations[e.From]; !ok {
				return errors.New("archived supports source is not an observation")
			}
			var ids []int64
			if gate, ok := gates[e.To]; ok {
				ids = gate.ObservationIDs
			} else if fact, ok := facts[e.To]; ok {
				ids = fact.ObservationIDs
			} else {
				return errors.New("archived supports destination is not a bound audit/fact")
			}
			found := false
			for _, id := range ids {
				if id == e.From {
					found = true
				}
			}
			if !found {
				return errors.New("archived supports edge is not declared by its destination")
			}
		case RelQualifies:
			fact, ok := facts[e.To]
			if !ok || fact.AuditID != e.From {
				return errors.New("archived qualifies edge is not bound to its fact")
			}
		}
	}
	return nil
}
