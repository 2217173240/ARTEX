package server

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

const defaultScheduleTimezone = "Asia/Shanghai"

func validateTaskSchedule(s *db.TaskSchedule) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || len(s.Name) > 200 {
		return fmt.Errorf("name is required (maximum 200 bytes)")
	}
	if s.Timezone == "" {
		s.Timezone = defaultScheduleTimezone
	}
	if s.Timezone == "Local" || s.Timezone == "system" {
		return fmt.Errorf("timezone must be an explicit IANA timezone")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("invalid timezone")
	}
	for _, clock := range []string{s.StartTime, s.EndTime} {
		v, err := time.Parse("15:04", clock)
		if err != nil || v.Format("15:04") != clock {
			return fmt.Errorf("times must use HH:mm")
		}
	}
	switch s.Type {
	case "once":
		if v, err := time.Parse("2006-01-02", s.RunDate); err != nil || v.Format("2006-01-02") != s.RunDate {
			return fmt.Errorf("run_date must use YYYY-MM-DD")
		}
		if s.EndDate == "" {
			s.EndDate = s.RunDate
		}
		if v, err := time.Parse("2006-01-02", s.EndDate); err != nil || v.Format("2006-01-02") != s.EndDate {
			return fmt.Errorf("end_date must use YYYY-MM-DD")
		}
		if s.EndDate < s.RunDate || (s.EndDate == s.RunDate && s.EndTime <= s.StartTime) {
			return fmt.Errorf("end must be later than start")
		}
		loc, _ := time.LoadLocation(s.Timezone)
		if _, ok := civilInstant(s.RunDate, s.StartTime, loc); !ok {
			return fmt.Errorf("start time does not exist in timezone")
		}
		if _, ok := civilInstant(s.EndDate, s.EndTime, loc); !ok {
			return fmt.Errorf("end time does not exist in timezone")
		}
		s.Weekdays = []int{}
	case "weekly":
		if s.StartTime == s.EndTime {
			return fmt.Errorf("window must have positive duration")
		}
		seen := map[int]bool{}
		for _, d := range s.Weekdays {
			if d < 1 || d > 7 {
				return fmt.Errorf("weekdays must be ISO weekdays 1..7")
			}
			seen[d] = true
		}
		if len(seen) == 0 {
			return fmt.Errorf("weekly schedule needs weekdays")
		}
		s.Weekdays = []int{}
		for d := range seen {
			s.Weekdays = append(s.Weekdays, d)
		}
		sort.Ints(s.Weekdays)
		s.RunDate = ""
		s.EndDate = ""
	default:
		return fmt.Errorf("type must be once or weekly")
	}
	if len(s.TaskIDs) == 0 || len(s.TaskIDs) > maxBatchControlIDs {
		return fmt.Errorf("task_ids must contain 1..100 tasks")
	}
	ids := normalizeBatchTaskIDs(s.TaskIDs)
	s.TaskIDs = []string{}
	for _, id := range ids {
		if !id.valid {
			return fmt.Errorf("invalid task ID %q", id.id)
		}
		s.TaskIDs = append(s.TaskIDs, id.id)
	}
	return nil
}

// civilInstant resolves a wall time to the earliest matching instant in a DST
// fold. A nonexistent wall time is rejected rather than normalized silently.
func civilInstant(date, clock string, loc *time.Location) (time.Time, bool) {
	wall, err := time.Parse("2006-01-02 15:04", date+" "+clock)
	if err != nil {
		return time.Time{}, false
	}
	offsets := map[int]bool{}
	for h := -48; h <= 48; h += 6 {
		_, offset := wall.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[offset] = true
	}
	var first time.Time
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if candidate.In(loc).Format("2006-01-02 15:04") != date+" "+clock {
			continue
		}
		if first.IsZero() || candidate.Before(first) {
			first = candidate
		}
	}
	return first, !first.IsZero()
}

type calendarWindow struct{ start, end time.Time }

func scheduleWindows(s *db.TaskSchedule, now time.Time) []calendarWindow {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return nil
	}
	result := []calendarWindow{}
	add := func(date, endDate string) {
		start, ok := civilInstant(date, s.StartTime, loc)
		if !ok {
			return
		}
		end, ok := civilInstant(endDate, s.EndTime, loc)
		if ok && end.After(start) {
			result = append(result, calendarWindow{start, end})
		}
	}
	if s.Type == "once" {
		end := s.EndDate
		if end == "" {
			end = s.RunDate
		}
		add(s.RunDate, end)
		return result
	}
	if s.Type != "weekly" {
		return nil
	}
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	// The previous day covers overnight windows. Two weeks covers a skipped DST
	// occurrence while still finding the following valid weekly end.
	for i := -1; i <= 14; i++ {
		candidate := day.AddDate(0, 0, i)
		weekday := int(candidate.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		match := false
		for _, d := range s.Weekdays {
			match = match || d == weekday
		}
		if !match {
			continue
		}
		end := candidate
		if s.EndTime < s.StartTime {
			end = end.AddDate(0, 0, 1)
		}
		add(candidate.Format("2006-01-02"), end.Format("2006-01-02"))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].start.Before(result[j].start) })
	return result
}
func taskScheduleActive(s *db.TaskSchedule, now time.Time) bool {
	if !s.Enabled {
		return false
	}
	if s.ManualUntil != nil && now.Before(*s.ManualUntil) {
		return true
	}
	for _, window := range scheduleWindows(s, now) {
		if !now.Before(window.start) && now.Before(window.end) {
			return true
		}
	}
	return false
}
func nextScheduleWindow(s *db.TaskSchedule, now time.Time) (calendarWindow, bool) {
	for _, window := range scheduleWindows(s, now) {
		if now.Before(window.end) {
			return window, true
		}
	}
	return calendarWindow{}, false
}
func annotateTaskSchedule(s *db.TaskSchedule, now time.Time) {
	s.NextStart = nil
	s.NextEnd = nil
	if window, ok := nextScheduleWindow(s, now); ok {
		s.NextStart = &window.start
		s.NextEnd = &window.end
	}
	switch {
	case !s.Enabled:
		s.WindowStatus = "paused"
	case taskScheduleActive(s, now):
		s.WindowStatus = "running"
	case s.NextEnd == nil:
		s.WindowStatus = "expired"
	default:
		s.WindowStatus = "scheduled"
	}
}

func (s *Server) pauseTaskLocked(t *Task, origin string, cause error) error {
	current, exists := s.m.Task(t.ID)
	if !exists || current != t || s.engine.IsDeleting(t.ID) || !s.engine.beginTaskOperation(t.ID) {
		return fmt.Errorf("任务正在删除，无法控制")
	}
	defer s.engine.decInflight(t.ID)
	lifecycle := t.lifecycleSnapshot()
	if isTerminalStatus(lifecycle.Status) {
		return fmt.Errorf("终态任务不能执行暂停")
	}
	if lifecycle.Paused && !(origin == "manual" && lifecycle.PauseOrigin == "schedule") {
		return fmt.Errorf("任务已经暂停")
	}
	wasEnginePaused := s.engine.IsPaused(t.ID)
	s.engine.Pause(t.ID, cause)
	if err := s.m.ApplyTaskPauseOrigin(t.ID, origin); err != nil {
		if !wasEnginePaused && !lifecycle.Queued {
			s.engine.Resume(t)
		}
		return err
	}
	s.cancelTaskChat(t.ID, agent.AbortChatPausedWithTask)
	return nil
}

func (s *Server) recordCalendarResult(ids []int64, t *Task, action, status string, err error) {
	message := ""
	if err != nil {
		message = err.Error()
		status = "error"
	}
	for _, id := range ids {
		if e := s.m.pg.AddTaskScheduleRun(id, t.ID, action, status, message); e != nil {
			log.Printf("[calendar] history: %v", e)
		}
	}
}

// reconcileCalendarLocked must run before FIFO promotion under concMu. Union
// all enabled memberships first so one closed window cannot cancel another open
// one. Manual holds and terminal/deleted tasks always win.
func (s *Server) reconcileCalendarLocked(now time.Time) { s.reconcileCalendarModeLocked(now, true) }

// applySchedulesBeforeRestore only installs closed-window holds. Boot resets
// running intents before admitting any open-window task.
func (s *Server) applySchedulesBeforeRestore(now time.Time) {
	s.reconcileCalendarModeLocked(now, false)
}
func (s *Server) reconcileCalendarModeLocked(now time.Time, permitStart bool) {
	if s.m.pg == nil {
		return
	}
	schedules, err := s.m.pg.ListTaskSchedules()
	if err != nil {
		log.Printf("[calendar] list: %v", err)
		return
	}
	type membership struct {
		ids    []int64
		active bool
	}
	members := map[string]*membership{}
	for _, schedule := range schedules {
		if !schedule.Enabled {
			continue
		}
		active := taskScheduleActive(schedule, now)
		for _, id := range schedule.TaskIDs {
			m := members[id]
			if m == nil {
				m = &membership{}
				members[id] = m
			}
			m.ids = append(m.ids, schedule.ID)
			m.active = m.active || active
		}
	}
	for _, task := range s.m.List() {
		life := task.lifecycleSnapshot()
		if isTerminalStatus(life.Status) || s.engine.IsDeleting(task.ID) {
			continue
		}
		member := members[task.ID]
		if member != nil && !member.active {
			if life.Paused {
				continue
			}
			err := s.pauseTaskLocked(task, "schedule", agent.Causef("paused_by_schedule", "任务在日历窗口外暂停", "任务将在启用的日历窗口内自动继续"))
			s.recordCalendarResult(member.ids, task, "pause", "paused", err)
		} else if member == nil && life.Paused && life.PauseOrigin == "schedule" {
			if err := s.pauseTaskLocked(task, "manual", agent.AbortPausedByUser); err != nil {
				log.Printf("[calendar] release ownership: %v", err)
			}
		} else if permitStart && member != nil && member.active && life.Paused && life.PauseOrigin == "schedule" {
			queued, err := s.admitTaskLocked(task, "resume", true, false)
			ids := []int64{}
			if member != nil {
				ids = member.ids
			}
			status := "running"
			if queued {
				status = "queued"
			}
			s.recordCalendarResult(ids, task, "resume", status, err)
		}
	}
}

// scheduleAdmissionAllowed is evaluated under concMu alongside admission so
// automatic recovery/follow-up cannot revive a task outside its window.
func (s *Server) scheduleAdmissionAllowed(id string, now time.Time) (bool, error) {
	schedules, err := s.m.pg.ListTaskSchedules()
	if err != nil {
		return false, err
	}
	managed, active := false, false
	for _, item := range schedules {
		if !item.Enabled {
			continue
		}
		for _, taskID := range item.TaskIDs {
			if taskID == id {
				managed = true
				active = active || taskScheduleActive(item, now)
			}
		}
	}
	return !managed || active, nil
}
