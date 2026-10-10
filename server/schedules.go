package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
)

func scheduleID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid schedule ID")
	}
	return id, nil
}
func (s *Server) scheduleFromRequest(w http.ResponseWriter, r *http.Request) *db.TaskSchedule {
	id, err := scheduleID(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return nil
	}
	item, err := s.m.pg.GetTaskSchedule(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil
	}
	if item == nil {
		writeErr(w, 404, "schedule not found")
	}
	return item
}
func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	items, err := s.m.pg.ListTaskSchedules()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, item := range items {
		annotateTaskSchedule(item, time.Now())
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": items})
}
func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	item := s.scheduleFromRequest(w, r)
	if item == nil {
		return
	}
	history, err := s.m.pg.ListTaskScheduleRuns(item.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	annotateTaskSchedule(item, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{"schedule": item, "history": history})
}

type scheduleMutation struct {
	Name      *string   `json:"name"`
	Enabled   *bool     `json:"enabled"`
	Type      *string   `json:"type"`
	Timezone  *string   `json:"timezone"`
	RunDate   *string   `json:"run_date"`
	EndDate   *string   `json:"end_date"`
	Weekdays  *[]int    `json:"weekdays"`
	StartTime *string   `json:"start_time"`
	EndTime   *string   `json:"end_time"`
	TaskIDs   *[]string `json:"task_ids"`
}

func decodeScheduleRequest(r *http.Request, item *db.TaskSchedule) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var patch scheduleMutation
	if err := decoder.Decode(&patch); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected a single JSON object")
	}
	if patch.Name != nil {
		item.Name = *patch.Name
	}
	if patch.Enabled != nil {
		item.Enabled = *patch.Enabled
	}
	if patch.Type != nil {
		item.Type = *patch.Type
	}
	if patch.Timezone != nil {
		item.Timezone = *patch.Timezone
	}
	if patch.RunDate != nil {
		item.RunDate = *patch.RunDate
	}
	if patch.EndDate != nil {
		item.EndDate = *patch.EndDate
	}
	if patch.Weekdays != nil {
		item.Weekdays = *patch.Weekdays
	}
	if patch.StartTime != nil {
		item.StartTime = *patch.StartTime
	}
	if patch.EndTime != nil {
		item.EndTime = *patch.EndTime
	}
	if patch.TaskIDs != nil {
		item.TaskIDs = *patch.TaskIDs
	}
	return validateTaskSchedule(item)
}
func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request, create bool) {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	item := &db.TaskSchedule{Enabled: true, Timezone: defaultScheduleTimezone, Weekdays: []int{}}
	if !create {
		item = s.scheduleFromRequest(w, r)
		if item == nil {
			return
		}
	}
	id := item.ID
	previous := *item
	if err := decodeScheduleRequest(r, item); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	item.ID = id
	if !sameScheduleWindow(&previous, item) || !item.Enabled {
		item.ManualUntil = nil
	}
	for _, id := range item.TaskIDs {
		task, ok := s.m.Task(id)
		if !ok || s.engine.IsDeleting(id) || isTerminalStatus(task.lifecycleSnapshot().Status) {
			writeErr(w, 400, "task "+id+" is missing, archived, terminal, or deleting")
			return
		}
	}
	if err := s.m.pg.SaveTaskSchedule(item); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.reconcileCalendarLocked(time.Now())
	code := http.StatusOK
	if create {
		code = http.StatusCreated
	}
	annotateTaskSchedule(item, time.Now())
	writeJSON(w, code, item)
}
func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) { s.saveSchedule(w, r, true) }
func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) { s.saveSchedule(w, r, false) }
func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	item := s.scheduleFromRequest(w, r)
	if item == nil {
		return
	}
	if err := s.m.pg.DeleteTaskSchedule(item.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.reconcileCalendarLocked(time.Now())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (s *Server) setScheduleEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	item := s.scheduleFromRequest(w, r)
	if item == nil {
		return
	}
	if err := s.m.pg.SetTaskScheduleEnabled(item.ID, enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	item.Enabled = enabled
	item.ManualUntil = nil
	s.reconcileCalendarLocked(time.Now())
	annotateTaskSchedule(item, time.Now())
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) pauseSchedule(w http.ResponseWriter, r *http.Request) {
	s.setScheduleEnabled(w, r, false)
}
func (s *Server) resumeSchedule(w http.ResponseWriter, r *http.Request) {
	s.setScheduleEnabled(w, r, true)
}
func (s *Server) runScheduleNow(w http.ResponseWriter, r *http.Request) {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	item := s.scheduleFromRequest(w, r)
	if item == nil {
		return
	}
	window, ok := nextScheduleWindow(item, time.Now())
	if !ok {
		writeErr(w, http.StatusConflict, "schedule has no future window; edit its dates before running now")
		return
	}
	previousEnabled, previousUntil := item.Enabled, item.ManualUntil
	if err := s.m.pg.SetTaskScheduleOverride(item.ID, true, &window.end); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	success := false
	results := []batchControlItem{}
	for _, id := range item.TaskIDs {
		result := batchControlItem{ID: id}
		task, ok := s.m.Task(id)
		if !ok || s.engine.IsDeleting(id) {
			result.Error = "task is missing, archived, or deleting"
			results = append(results, result)
			continue
		}
		life := task.lifecycleSnapshot()
		var err error
		if isTerminalStatus(life.Status) {
			err = fmt.Errorf("terminal task cannot resume")
		} else {
			// Explicit run-now may release a human pause, just like task Resume. Existing
			// running tasks are admitted without creating a second execution loop.
			result.Queued, err = s.admitTaskLocked(task, s.resumeAdmissionMode(task), life.Paused, false)
		}
		if err != nil {
			result.Error = err.Error()
		} else {
			result.OK = true
			success = true
			result.Status = "running"
			if result.Queued {
				result.Status = "queued"
			}
		}
		s.recordCalendarResult([]int64{item.ID}, task, "run-now", result.Status, err)
		results = append(results, result)
	}
	if !success {
		if err := s.m.pg.SetTaskScheduleOverride(item.ID, previousEnabled, previousUntil); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results, "manual_until": previousUntil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "manual_until": window.end})
}

func sameScheduleWindow(a, b *db.TaskSchedule) bool {
	if a.Type != b.Type || a.Timezone != b.Timezone || a.RunDate != b.RunDate || a.EndDate != b.EndDate || a.StartTime != b.StartTime || a.EndTime != b.EndTime {
		return false
	}
	if len(a.Weekdays) != len(b.Weekdays) || len(a.TaskIDs) != len(b.TaskIDs) {
		return false
	}
	days := map[int]bool{}
	for _, day := range a.Weekdays {
		days[day] = true
	}
	for _, day := range b.Weekdays {
		if !days[day] {
			return false
		}
	}
	ids := map[string]bool{}
	for _, id := range a.TaskIDs {
		ids[id] = true
	}
	for _, id := range b.TaskIDs {
		if !ids[id] {
			return false
		}
	}
	return true
}
