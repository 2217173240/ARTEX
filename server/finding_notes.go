package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// Authentication identifies the shared ARTEX administrator, not an individual.
// Do not infer a human identity from client-submitted headers or author fields.
const findingManualActor = "ARTEX (shared admin)"

type FindingNoteDTO struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func findingNoteDTO(n db.FindingNote) FindingNoteDTO {
	return FindingNoteDTO{i64s(n.ID), n.Author, n.Body, rfc3339(n.CreatedAt)}
}

func (s *Server) registerFindingNotes(mux *http.ServeMux) {
	base := "/api/exploration/findings/{id}/notes"
	mux.HandleFunc("GET "+base, s.getFindingNotes)
	mux.HandleFunc("POST "+base, s.createFindingNote)
	mux.HandleFunc("DELETE "+base+"/{note_id}", s.deleteFindingNote)
}

func findingManualError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, db.ErrFindingReadOnly):
		status = http.StatusForbidden
	case errors.Is(err, db.ErrFindingNotFound), errors.Is(err, db.ErrFindingNoteNotFound), errors.Is(err, db.ErrFindingContextNotFound), errors.Is(err, db.ErrFindingUnavailable):
		status = http.StatusNotFound
	case errors.Is(err, db.ErrTaskArchiveState):
		status = http.StatusConflict
	case errors.Is(err, db.ErrFindingNoteInvalid):
		status = http.StatusBadRequest
	}
	writeErr(w, status, err.Error())
}

func findingManualIDs(w http.ResponseWriter, r *http.Request) (id, contextTask int64, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid finding id")
		return 0, 0, false
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("context_task")); raw != "" {
		contextTask, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || contextTask <= 0 {
			writeErr(w, 400, "invalid context task id")
			return 0, 0, false
		}
	}
	return id, contextTask, true
}

func (s *Server) getFindingNotes(w http.ResponseWriter, r *http.Request) {
	id, contextTask, ok := findingManualIDs(w, r)
	if !ok {
		return
	}
	limit := 50
	before := int64(0)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 100 {
			writeErr(w, 400, "limit must be between 1 and 100")
			return
		}
		limit = v
	}
	if raw := r.URL.Query().Get("before"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			writeErr(w, 400, "invalid before cursor")
			return
		}
		before = v
	}
	notes, more, err := s.m.pg.ListFindingNotes(r.Context(), id, contextTask, before, limit)
	if err != nil {
		findingManualError(w, err)
		return
	}
	out := struct {
		Notes      []FindingNoteDTO `json:"notes"`
		HasMore    bool             `json:"has_more"`
		NextBefore int64            `json:"next_before,omitempty"`
	}{Notes: []FindingNoteDTO{}, HasMore: more}
	for _, n := range notes {
		out.Notes = append(out.Notes, findingNoteDTO(n))
	}
	if more && len(notes) > 0 {
		out.NextBefore = notes[len(notes)-1].ID
	}
	writeJSON(w, 200, out)
}

func (s *Server) createFindingNote(w http.ResponseWriter, r *http.Request) {
	id, contextTask, ok := findingManualIDs(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&body); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	n, err := s.m.pg.AddFindingNote(r.Context(), id, contextTask, findingManualActor, body.Body)
	if err != nil {
		findingManualError(w, err)
		return
	}
	writeJSON(w, 201, findingNoteDTO(*n))
}

func (s *Server) deleteFindingNote(w http.ResponseWriter, r *http.Request) {
	id, contextTask, ok := findingManualIDs(w, r)
	if !ok {
		return
	}
	noteID, err := strconv.ParseInt(r.PathValue("note_id"), 10, 64)
	if err != nil || noteID <= 0 {
		writeErr(w, 400, "invalid note id")
		return
	}
	if err = s.m.pg.DeleteFindingNote(r.Context(), id, contextTask, noteID); err != nil {
		findingManualError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
