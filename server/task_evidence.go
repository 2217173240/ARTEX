package server

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

// taskEvidenceStore 只读取显式指定任务的图，避免把活动任务或继承来源混入证据检索。
func (s *Server) taskEvidenceStore(w http.ResponseWriter, r *http.Request) *db.ExplorationStore {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid task id")
		return nil
	}
	if s.m == nil || s.m.PG() == nil || s.m.PG().DB == nil {
		writePublicErr(w, http.StatusServiceUnavailable, "database unavailable")
		return nil
	}
	archive, err := s.m.PG().GetTaskArchiveByTask(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if archive != nil && archive.State != db.ArchiveQueued && archive.State != db.ArchiveFailed {
		writeErr(w, http.StatusConflict, "证据正在归档或已冷归档，请完成归档操作并还原任务后查看")
		return nil
	}
	task, err := s.m.PG().GetTask(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return nil
	}
	return s.m.PG().Exploration(task.ExplorationID)
}

func (s *Server) taskEvidenceList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && kind != db.KindObservation && kind != db.KindValidityGate && kind != db.KindFact {
		writeErr(w, http.StatusBadRequest, "invalid evidence kind")
		return
	}
	query := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(query) > 500 {
		writeErr(w, http.StatusBadRequest, "evidence query exceeds 500 characters")
		return
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeErr(w, http.StatusBadRequest, "evidence limit must be between 1 and 200")
			return
		}
		limit = value
	}
	var before int64
	if raw := q.Get("before"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeErr(w, http.StatusBadRequest, "invalid evidence cursor")
			return
		}
		before = value
	}
	store := s.taskEvidenceStore(w, r)
	if store == nil {
		return
	}
	page, err := store.ListEvidence(kind, query, limit, before)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Nodes      []TaskNodeDTO `json:"nodes"`
		NextBefore int64         `json:"next_before,omitempty"`
		Scope      string        `json:"scope"`
	}{taskNodeDTOs(page.Nodes), page.NextBefore, "task_only"})
}

func (s *Server) taskEvidenceDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("nodeID"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid evidence node id")
		return
	}
	store := s.taskEvidenceStore(w, r)
	if store == nil {
		return
	}
	detail, err := store.EvidenceDetail(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "evidence node not found in task")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node": taskNodeDTO(detail.Node), "nodes": taskNodeDTOs(detail.Nodes), "edges": edgeDTOs(detail.Edges),
	})
}

func (s *Server) taskEvidenceBody(w http.ResponseWriter, r *http.Request) {
	nodeID, err := strconv.ParseInt(r.PathValue("nodeID"), 10, 64)
	if err != nil || nodeID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid evidence node id")
		return
	}
	side := r.URL.Query().Get("side")
	if side == "" {
		side = "response"
	}
	if side != "request" && side != "response" {
		writeErr(w, http.StatusBadRequest, "side must be request or response")
		return
	}
	offset, length := int64(0), int64(8192)
	for name, value := range map[string]*int64{"offset": &offset, "length": &length} {
		if raw := r.URL.Query().Get(name); raw != "" {
			parsed, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr != nil || parsed < 0 {
				writeErr(w, http.StatusBadRequest, "invalid evidence body range")
				return
			}
			*value = parsed
		}
	}
	if s.taskEvidenceStore(w, r) == nil {
		return
	}
	taskID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	store := s.evidenceStore()
	snapshot, err := store.ObservationSnapshot(r.Context(), taskID, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "traffic observation not found in task")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Query().Get("download") == "1" {
		file, size, err := store.OpenBody(snapshot, side)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"observation-%d-%s.bin\"", nodeID, side))
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		_, _ = io.Copy(w, file)
		return
	}
	total := snapshot.RespLen
	if side == "request" {
		total = snapshot.ReqLen
	}
	if offset > total {
		writeErr(w, http.StatusBadRequest, "offset exceeds evidence body length")
		return
	}
	preview, err := readEvidencePreview(store, snapshot, side, offset, length)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
