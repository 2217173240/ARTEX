package server

// 任务结果接口参考 BaaRai/ARTEX PR #102，作者 BaaRai，AGPL-3.0。
// 2026-10-19：适配可选数据库、固定公开错误和冷归档读取。

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
)

func (s *Server) taskOutcome(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid task id")
		return
	}
	if s.m == nil || s.m.PG() == nil || s.m.PG().DB == nil {
		writePublicErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	outcome, err := s.m.PG().TaskOutcome(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		log.Printf("[tasks] outcome query failed for %d: %v", id, err)
		writeErr(w, http.StatusInternalServerError, "outcome query failed")
		return
	}
	writeJSON(w, http.StatusOK, outcome)
}
