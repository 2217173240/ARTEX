package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestCalendarWindows(t *testing.T) {
	cases := []struct {
		name string
		s    db.TaskSchedule
		now  string
		want bool
	}{
		{"continuous intermediate day", db.TaskSchedule{Type: "once", RunDate: "2026-10-10", EndDate: "2026-10-12", StartTime: "09:00", EndTime: "17:00"}, "2026-10-11T02:00:00+08:00", true},
		{"once end exclusive", db.TaskSchedule{Type: "once", RunDate: "2026-10-10", EndDate: "2026-10-12", StartTime: "09:00", EndTime: "17:00"}, "2026-10-12T17:00:00+08:00", false},
		{"weekly Monday", db.TaskSchedule{Type: "weekly", Weekdays: []int{1}, StartTime: "09:00", EndTime: "17:00"}, "2026-10-12T09:00:00+08:00", true},
		{"overnight Sunday spill", db.TaskSchedule{Type: "weekly", Weekdays: []int{7}, StartTime: "23:00", EndTime: "02:00"}, "2026-10-12T01:59:59+08:00", true},
		{"overnight end", db.TaskSchedule{Type: "weekly", Weekdays: []int{7}, StartTime: "23:00", EndTime: "02:00"}, "2026-10-12T02:00:00+08:00", false},
		{"DST skipped start", db.TaskSchedule{Type: "weekly", Timezone: "America/New_York", Weekdays: []int{7}, StartTime: "02:30", EndTime: "04:00"}, "2026-03-08T07:00:00Z", false},
		{"DST first repeated hour", db.TaskSchedule{Type: "weekly", Timezone: "America/New_York", Weekdays: []int{7}, StartTime: "01:00", EndTime: "02:00"}, "2026-11-01T05:30:00Z", true},
		{"DST second repeated hour", db.TaskSchedule{Type: "weekly", Timezone: "America/New_York", Weekdays: []int{7}, StartTime: "01:00", EndTime: "02:00"}, "2026-11-01T06:30:00Z", true},
		{"DST fold does not reopen", db.TaskSchedule{Type: "weekly", Timezone: "America/New_York", Weekdays: []int{7}, StartTime: "01:15", EndTime: "01:45"}, "2026-11-01T06:30:00Z", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.s.Enabled = true
			if tt.s.Timezone == "" {
				tt.s.Timezone = defaultScheduleTimezone
			}
			now, _ := time.Parse(time.RFC3339, tt.now)
			if got := taskScheduleActive(&tt.s, now); got != tt.want {
				t.Fatalf("active=%v want %v", got, tt.want)
			}
		})
	}
}
func TestCalendarValidation(t *testing.T) {
	base := db.TaskSchedule{Name: "window", Type: "once", RunDate: "2026-10-10", StartTime: "09:00", EndTime: "17:00", TaskIDs: []string{"1"}}
	if err := validateTaskSchedule(&base); err != nil {
		t.Fatal(err)
	}
	if base.Timezone != defaultScheduleTimezone || base.EndDate != base.RunDate {
		t.Fatalf("defaults: %+v", base)
	}
	for _, mutate := range []func(*db.TaskSchedule){func(s *db.TaskSchedule) { s.Timezone = "system" }, func(s *db.TaskSchedule) { s.Timezone = "Invalid/Zone" }, func(s *db.TaskSchedule) { s.StartTime = "9:00" }, func(s *db.TaskSchedule) { s.EndDate = "2026-10-09" }, func(s *db.TaskSchedule) { s.TaskIDs = []string{"bad"} }, func(s *db.TaskSchedule) { s.Type = "weekly"; s.Weekdays = []int{0} }, func(s *db.TaskSchedule) { s.EndTime = s.StartTime }} {
		s := base
		mutate(&s)
		if err := validateTaskSchedule(&s); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

// Calendar integration uses a disposable database rather than the developer's
// configured database. Set ARTEX_CALENDAR_TEST_ADMIN_DSN to use another fixture.
func calendarFixture(t *testing.T) (*Manager, *Server) {
	t.Helper()
	adminDSN := os.Getenv("ARTEX_CALENDAR_TEST_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("ARTEX_CALENDAR_TEST_ADMIN_DSN is not set")
	}
	parsed, err := url.Parse(adminDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("calendar admin DSN must be a PostgreSQL URL")
	}
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.Ping(); err != nil {
		admin.Close()
		t.Fatalf("isolated calendar postgres unavailable: %v", err)
	}
	name := fmt.Sprintf("artex_r9_calendar_%d", time.Now().UnixNano())
	if _, err = admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	parsed.RawPath = ""
	t.Setenv("ARTEX_PG_DSN", parsed.String())
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`)
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.Close()
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	s := newAdmissionTestServer(m, func(*Task) bool { return false })
	if err = m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}
	return m, s
}
func calendarRequest(s *Server, handler http.HandlerFunc, id, method string, payload any) *httptest.ResponseRecorder {
	var body []byte
	if item, ok := payload.(db.TaskSchedule); ok {
		payload = map[string]any{"name": item.Name, "enabled": item.Enabled, "type": item.Type, "timezone": item.Timezone, "run_date": item.RunDate, "end_date": item.EndDate, "weekdays": item.Weekdays, "start_time": item.StartTime, "end_time": item.EndTime, "task_ids": item.TaskIDs}
	}
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	r := httptest.NewRequest(method, "/api/schedules/"+id, bytes.NewReader(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}
func TestCalendarAPILifecycle(t *testing.T) {
	m, s := calendarFixture(t)
	task, err := m.CreateTask("calendar", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(time.FixedZone("+8", 8*3600)).Format("2006-01-02")
	item := db.TaskSchedule{Name: "closed", Enabled: true, Type: "once", RunDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02"), StartTime: "09:00", EndTime: "17:00", TaskIDs: []string{task.ID}}
	w := calendarRequest(s, s.createSchedule, "", "POST", item)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(item.ID, 10)
	if life := task.lifecycleSnapshot(); !life.Paused || life.Queued || life.PauseOrigin != "schedule" {
		t.Fatalf("outside window: %+v", life)
	}
	persisted, _ := m.pg.GetTask(calendarTaskID(task.ID))
	if !persisted.Paused || persisted.PauseOrigin != "schedule" {
		t.Fatalf("persistent hold: %+v", persisted)
	}
	if _, err = s.applyTaskControl(task, "pause"); err != nil {
		t.Fatal(err)
	}
	w = calendarRequest(s, s.pauseSchedule, id, "POST", nil)
	if w.Code != 200 {
		t.Fatalf("pause schedule: %s", w.Body)
	}
	if life := task.lifecycleSnapshot(); !life.Paused || life.PauseOrigin != "manual" {
		t.Fatalf("manual pause lost: %+v", life)
	}
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Results []batchControlItem `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Results) != 1 || !result.Results[0].OK || result.Results[0].Status != "queued" || !task.lifecycleSnapshot().Queued {
		t.Fatalf("run-now: %s", w.Body)
	}
	w = calendarRequest(s, s.resumeSchedule, id, "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if task.lifecycleSnapshot().PauseOrigin != "schedule" {
		t.Fatal("queue not held outside window")
	}
	// Another open enabled window wins over the closed window.
	open := db.TaskSchedule{Name: "open", Enabled: true, Type: "once", RunDate: today, EndDate: time.Now().In(time.FixedZone("+8", 8*3600)).AddDate(0, 0, 1).Format("2006-01-02"), StartTime: "00:00", EndTime: "23:59", TaskIDs: []string{task.ID}}
	w = calendarRequest(s, s.createSchedule, "", "POST", open)
	if w.Code != 201 {
		t.Fatalf("open: %s", w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &open)
	if life := task.lifecycleSnapshot(); life.Paused || !life.Queued {
		t.Fatalf("overlap union: %+v", life)
	}
	w = calendarRequest(s, s.deleteSchedule, strconv.FormatInt(open.ID, 10), "DELETE", nil)
	if w.Code != 200 || task.lifecycleSnapshot().PauseOrigin != "schedule" {
		t.Fatalf("remaining closed: %s %+v", w.Body, task.lifecycleSnapshot())
	}
	w = calendarRequest(s, s.deleteSchedule, id, "DELETE", nil)
	if w.Code != 200 || !task.lifecycleSnapshot().Paused || task.lifecycleSnapshot().PauseOrigin != "manual" {
		t.Fatalf("release last hold: %s %+v", w.Body, task.lifecycleSnapshot())
	}
	// Terminal rows cannot be selected or revived by explicit run-now.
	if err = m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	w = calendarRequest(s, s.createSchedule, "", "POST", item)
	if w.Code != 400 {
		t.Fatalf("terminal membership accepted: %s", w.Body)
	}
}
func calendarTaskID(id string) int64 { n, _ := strconv.ParseInt(id, 10, 64); return n }
func TestCalendarManualPauseRace(t *testing.T) {
	m, s := calendarFixture(t)
	task, err := m.CreateTask("race", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	item := &db.TaskSchedule{Name: "closed", Enabled: true, Type: "once", Timezone: defaultScheduleTimezone, RunDate: "2000-01-01", EndDate: "2000-01-01", StartTime: "09:00", EndTime: "17:00", Weekdays: []int{}, TaskIDs: []string{task.ID}}
	if err = m.pg.SaveTaskSchedule(item); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.concMu.Lock(); s.reconcileCalendarLocked(time.Now()); s.concMu.Unlock() }()
		go func() { defer wg.Done(); s.applyTaskControl(task, "pause") }()
	}
	wg.Wait()
	s.concMu.Lock()
	m.pg.SetTaskScheduleEnabled(item.ID, false)
	s.reconcileCalendarLocked(time.Now())
	s.concMu.Unlock()
	if life := task.lifecycleSnapshot(); !life.Paused || life.PauseOrigin != "manual" || life.Queued {
		t.Fatalf("human pause lost in race: %+v", life)
	}
}

func TestCalendarRestartOverrideAndGuards(t *testing.T) {
	m, s := calendarFixture(t)
	task, err := m.CreateTask("restart", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	item := db.TaskSchedule{Name: "future", Enabled: true, Type: "once", RunDate: future, StartTime: "09:00", EndTime: "17:00", TaskIDs: []string{task.ID}}
	w := calendarRequest(s, s.createSchedule, "", "POST", item)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &item)
	id := strconv.FormatInt(item.ID, 10)
	// Automatic admissions remain blocked outside the window, including recovery.
	if _, err = s.admitTask(task, "resume"); err == nil {
		t.Fatal("automatic admission escaped closed window")
	}
	// Run-now holds continuously until the next calendar end, including restart.
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, err := m.pg.GetTaskSchedule(item.ID)
	if err != nil || saved.ManualUntil == nil || !saved.Enabled {
		t.Fatalf("override not durable: %+v %v", saved, err)
	}
	s.reconcileConcurrency()
	if life := task.lifecycleSnapshot(); life.Paused || !life.Queued {
		t.Fatalf("override discarded by next tick: %+v", life)
	}
	until := *saved.ManualUntil
	w = calendarRequest(s, s.updateSchedule, id, "PATCH", map[string]any{"name": "renamed"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	saved, _ = m.pg.GetTaskSchedule(item.ID)
	if saved.ManualUntil == nil || !saved.ManualUntil.Equal(until) {
		t.Fatal("name edit discarded active override")
	}

	m.LoadExisting()
	loaded, ok := m.Task(task.ID)
	if !ok {
		t.Fatal("task lost on restart")
	}
	s.concMu.Lock()
	s.applySchedulesBeforeRestore(time.Now())
	s.concMu.Unlock()
	if loaded.lifecycleSnapshot().Paused {
		t.Fatal("boot discarded durable override")
	}
	// A human pause during an override stays human-owned on every automatic tick.
	if _, err = s.applyTaskControl(loaded, "pause"); err != nil {
		t.Fatal(err)
	}
	s.reconcileConcurrency()
	if life := loaded.lifecycleSnapshot(); !life.Paused || life.PauseOrigin != "manual" {
		t.Fatalf("override defeated manual pause: %+v", life)
	}
	// Server-computed / immutable fields cannot be injected through PATCH.
	w = calendarRequest(s, s.updateSchedule, id, "PATCH", map[string]any{"manual_until": time.Now().Add(100 * time.Hour)})
	if w.Code != 400 {
		t.Fatalf("read-only field accepted: %s", w.Body)
	}
	// Delete barriers block explicit run-now; no-success must retain prior override.
	s.engine.BeginDelete(loaded.ID)
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var output struct {
		Results []batchControlItem `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &output)
	if len(output.Results) != 1 || output.Results[0].OK || output.Results[0].Error == "" {
		t.Fatalf("deleting task claimed success: %s", w.Body)
	}
	s.engine.AbortDelete(loaded.ID, true)
	// Expired once cannot create an open-ended override.
	item.RunDate = "2000-01-01"
	item.EndDate = item.RunDate
	w = calendarRequest(s, s.updateSchedule, id, "PATCH", item)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 409 {
		t.Fatalf("expired plan run-now accepted: %s", w.Body)
	}
	detail := calendarRequest(s, s.getSchedule, id, "GET", nil)
	if detail.Code != 200 || !bytes.Contains(detail.Body.Bytes(), []byte(`"history":[`)) {
		t.Fatalf("history missing: %s", detail.Body)
	}
}

func TestCalendarAtomicMembershipAndArchiveCAS(t *testing.T) {
	m, s := calendarFixture(t)
	task, err := m.CreateTask("atomic", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	item := &db.TaskSchedule{Name: "original", Enabled: true, Type: "once", Timezone: defaultScheduleTimezone, RunDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02"), StartTime: "09:00", EndTime: "17:00", Weekdays: []int{}, TaskIDs: []string{task.ID}}
	if err = validateTaskSchedule(item); err != nil {
		t.Fatal(err)
	}
	if err = m.pg.SaveTaskSchedule(item); err != nil {
		t.Fatal(err)
	}
	broken := *item
	broken.Name = "must roll back"
	broken.TaskIDs = []string{task.ID, "999999999"}
	if err = m.pg.SaveTaskSchedule(&broken); err == nil {
		t.Fatal("missing member accepted")
	}
	saved, err := m.pg.GetTaskSchedule(item.ID)
	if err != nil || saved.Name != "original" || len(saved.TaskIDs) != 1 {
		t.Fatalf("membership transaction leaked: %+v %v", saved, err)
	}
	// Initial gating closes a previously queued row without ever promoting it.
	if err = m.EnqueueTask(task.ID, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	s.concMu.Lock()
	s.applySchedulesBeforeRestore(time.Now())
	s.concMu.Unlock()
	if life := task.lifecycleSnapshot(); !life.Paused || life.Queued || life.PauseOrigin != "schedule" {
		t.Fatalf("boot failed to gate queue: %+v", life)
	}
	// Archive request owns the paused row. Calendar admission cannot race it open.
	if _, err = m.pg.QueueTaskArchive(calendarTaskID(task.ID)); err != nil {
		t.Fatal(err)
	}
	w := calendarRequest(s, s.runScheduleNow, strconv.FormatInt(item.ID, 10), "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Results []batchControlItem `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Results) != 1 || result.Results[0].OK {
		t.Fatalf("archive queue raced open: %s", w.Body)
	}
	if life := task.lifecycleSnapshot(); !life.Paused || life.Queued {
		t.Fatalf("failed admission mutated lifecycle: %+v", life)
	}
	if err = m.pg.SaveTaskSchedule(item); err == nil {
		t.Fatal("pending archive member accepted")
	}
	saved, _ = m.pg.GetTaskSchedule(item.ID)
	if saved.ManualUntil != nil {
		t.Fatal("all-failed run-now retained override")
	}
}

func TestCalendarDoesNotReviveStaleTerminalTask(t *testing.T) {
	m, s := calendarFixture(t)
	task, err := m.CreateTask("terminal race", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	item := &db.TaskSchedule{Name: "future", Enabled: true, Type: "once", Timezone: defaultScheduleTimezone, RunDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02"), StartTime: "09:00", EndTime: "17:00", Weekdays: []int{}, TaskIDs: []string{task.ID}}
	if err = validateTaskSchedule(item); err != nil {
		t.Fatal(err)
	}
	if err = m.pg.SaveTaskSchedule(item); err != nil {
		t.Fatal(err)
	}
	s.reconcileConcurrency()
	// Deliberately leave the Manager snapshot stale, as an asynchronous terminal
	// completion could do just before committing the in-memory reflection.
	if _, err = m.pg.Exec(`UPDATE tasks SET status='done' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	w := calendarRequest(s, s.runScheduleNow, strconv.FormatInt(item.ID, 10), "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Results []batchControlItem `json:"results"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Results) != 1 || result.Results[0].OK {
		t.Fatalf("stale terminal revived: %s", w.Body)
	}
	row, err := m.pg.GetTask(calendarTaskID(task.ID))
	if err != nil || row.Status != "done" || !row.Paused || row.Queued {
		t.Fatalf("terminal CAS lost: %+v %v", row, err)
	}
}

func TestCalendarCancellationIsRecoverable(t *testing.T) {
	e := NewEngine(nil)
	ctx := e.execContextFor(context.Background(), "calendar")
	e.Pause("calendar", agent.Causef("paused_by_schedule", "calendar pause", "next window resumes"))
	if !taskExecutionPaused(context.Cause(ctx)) {
		t.Fatal("calendar cancellation was treated as blocked worker failure")
	}
	if code, _, _, ok := agent.AbortReason(ctx); !ok || code != "paused_by_schedule" {
		t.Fatalf("missing calendar cancellation cause: %q", code)
	}
}
func TestCalendarAdmissionCASPreservesExternalManualHold(t *testing.T) {
	m, s := calendarFixture(t)
	task, err := m.CreateTask("pause race", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	old := task.lifecycleSnapshot()
	if _, err = m.pg.Exec(`UPDATE tasks SET paused=true,pause_origin='manual' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.ApplyTaskAdmission(task.ID, old.Status, old.Status, true, "bootstrap", false, old); err == nil {
		t.Fatal("admission overwrote newer persisted manual pause")
	}
	row, err := m.pg.GetTask(calendarTaskID(task.ID))
	if err != nil || !row.Paused || row.PauseOrigin != "manual" || row.Queued {
		t.Fatalf("manual pause CAS lost: %+v %v", row, err)
	}
	// Avoid inventing a live engine on failed CAS.
	if s.engine.Started(task.ID) {
		t.Fatal("failed admission started engine")
	}
}

func TestCalendarRunNowBootstrapSingleFlightAndCanceledRestart(t *testing.T) {
	m, s := calendarFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.taskAgents = map[string]*taskAgentBundle{}
	entered := make(chan int, 10)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	unblockFirst := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	var calls atomic.Int32
	s.llmOn = true
	s.llmCfg = agent.Config{Stream: false}
	s.llmProv = retestProvider{complete: func(callCtx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		n := int(calls.Add(1))
		entered <- n
		if n == 1 {
			<-releaseFirst
			return llm.Message{}, "", llm.Usage{}, callCtx.Err()
		}
		<-callCtx.Done()
		return llm.Message{}, "", llm.Usage{}, callCtx.Err()
	}}
	// Override only Engine readiness; goal decomposition still uses the blocked
	// mock provider through the real task router and writes to isolated PostgreSQL.
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return new(agent.Planner), new(agent.Worker) })
	t.Cleanup(func() {
		cancel()
		unblockFirst()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			busy := false
			s.engine.bootstrapping.Range(func(_, _ any) bool { busy = true; return false })
			for _, task := range m.List() {
				if s.engine.inflightCount(task.ID) > 0 {
					busy = true
				}
			}
			if !busy {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("bootstrap did not drain")
	})
	task, err := m.CreateTask("bootstrap", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	item := db.TaskSchedule{Name: "future", Enabled: true, Type: "once", RunDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02"), StartTime: "09:00", EndTime: "17:00", TaskIDs: []string{task.ID}}
	w := calendarRequest(s, s.createSchedule, "", "POST", item)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &item)
	id := strconv.FormatInt(item.ID, 10)
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("mock goal provider did not start")
	}
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	select {
	case n := <-entered:
		unblockFirst()
		t.Fatalf("duplicate goal bootstrap call %d", n)
	case <-time.After(100 * time.Millisecond):
	}
	if s.engine.Started(task.ID) {
		unblockFirst()
		t.Fatal("worker loops started before goal bootstrap completed")
	}
	if _, err = s.applyTaskControl(task, "pause"); err != nil {
		unblockFirst()
		t.Fatal(err)
	}
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 200 {
		unblockFirst()
		t.Fatal(w.Body.String())
	}
	select {
	case n := <-entered:
		unblockFirst()
		t.Fatalf("new bootstrap started before canceled call drained: %d", n)
	case <-time.After(100 * time.Millisecond):
	}
	unblockFirst()
	select {
	case n := <-entered:
		if n != 2 {
			t.Fatalf("restart call = %d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resume during cancel drain stranded bootstrap")
	}
	cancel()
}

func TestCalendarResumeStartedEngineBeforeBootstrapCleanup(t *testing.T) {
	m, s := calendarFixture(t)
	task, err := m.CreateTask("started bootstrap", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	item := db.TaskSchedule{Name: "future", Enabled: true, Type: "once", RunDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02"), StartTime: "09:00", EndTime: "17:00", TaskIDs: []string{task.ID}}
	w := calendarRequest(s, s.createSchedule, "", "POST", item)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &item)
	// Model Run's real interval after it installs Started but before the final
	// synchronous HasActiveIntent query returns and bootstrap cleanup can run.
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return new(agent.Planner), new(agent.Worker) })
	s.engine.started.Store(task.ID, true)
	s.engine.bootstrapping.Store(task.ID, true)
	defer s.engine.bootstrapping.Delete(task.ID)
	w = calendarRequest(s, s.runScheduleNow, strconv.FormatInt(item.ID, 10), "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if life := task.lifecycleSnapshot(); life.Paused || life.Queued || s.engine.IsPaused(task.ID) {
		t.Fatalf("valid admission stranded already-started loops: %+v enginePaused=%v", life, s.engine.IsPaused(task.ID))
	}
	select {
	case <-task.notify:
	default:
		t.Fatal("started runtime was not nudged after resume")
	}
}

func TestCalendarCanceledBootstrapResumesPersistedGoals(t *testing.T) {
	m, s := calendarFixture(t)
	if err := m.SetConcurrency(false, 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.taskAgents = map[string]*taskAgentBundle{}
	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSecond) }) }
	var calls atomic.Int32
	s.llmOn = true
	s.llmCfg = agent.Config{Stream: false}
	s.llmProv = retestProvider{complete: func(callCtx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		n := calls.Add(1)
		if n == 1 {
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "calendar-goals", Name: "set_goals", Input: json.RawMessage(`{"text":"persist exactly once"}`)}}}, "tool_use", llm.Usage{}, nil
		}
		if n == 2 {
			close(secondEntered)
			<-releaseSecond
		} else {
			<-callCtx.Done()
		}
		return llm.Message{}, "", llm.Usage{}, callCtx.Err()
	}}
	task, err := m.CreateTask("persisted bootstrap", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		release()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			_, busy := s.engine.bootstrapping.Load(task.ID)
			if !busy && s.engine.inflightCount(task.ID) == 0 {
				s.engine.StopTask(task.ID)
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("persisted-goal bootstrap did not drain")
	})
	item := db.TaskSchedule{Name: "future", Enabled: true, Type: "once", RunDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02"), StartTime: "09:00", EndTime: "17:00", TaskIDs: []string{task.ID}}
	w := calendarRequest(s, s.createSchedule, "", "POST", item)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &item)
	id := strconv.FormatInt(item.ID, 10)
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("set_goals did not complete before blocked mock turn")
	}
	goals, err := task.Store.ListByKind(db.KindGoal, 10)
	if err != nil || len(goals) != 1 {
		t.Fatalf("mock did not persist goal: %+v %v", goals, err)
	}
	if _, err = s.applyTaskControl(task, "pause"); err != nil {
		t.Fatal(err)
	}
	w = calendarRequest(s, s.runScheduleNow, id, "POST", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	release()
	deadline := time.Now().Add(3 * time.Second)
	for !s.engine.Started(task.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.engine.Started(task.ID) {
		t.Fatal("persisted goals were decomposed again instead of resuming engine")
	}
	if s.engine.IsPaused(task.ID) || calls.Load() != 2 {
		t.Fatalf("persisted-goal resume: paused=%v providerCalls=%d", s.engine.IsPaused(task.ID), calls.Load())
	}
	goals, err = task.Store.ListByKind(db.KindGoal, 10)
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals duplicated after cancel drain: %+v %v", goals, err)
	}
}
