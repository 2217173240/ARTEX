package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// TaskSchedule describes a local calendar window for existing tasks.
type TaskSchedule struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Enabled      bool       `json:"enabled"`
	Type         string     `json:"type"`
	Timezone     string     `json:"timezone"`
	RunDate      string     `json:"run_date"`
	EndDate      string     `json:"end_date"`
	Weekdays     []int      `json:"weekdays"`
	StartTime    string     `json:"start_time"`
	EndTime      string     `json:"end_time"`
	TaskIDs      []string   `json:"task_ids"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ManualUntil  *time.Time `json:"manual_until"`
	NextStart    *time.Time `json:"next_start"`
	NextEnd      *time.Time `json:"next_end"`
	WindowStatus string     `json:"status"`
}

type TaskScheduleRun struct {
	ID        int64     `json:"id"`
	TaskID    string    `json:"task_id"`
	Action    string    `json:"action"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

const scheduleCols = `id,name,enabled,type,timezone,run_date,end_date,weekdays,start_time,end_time,created_at,updated_at,manual_until,
COALESCE((SELECT jsonb_agg(task_id::text ORDER BY task_id) FROM task_schedule_tasks WHERE schedule_id=task_schedules.id),'[]'::jsonb)`

func scanSchedule(row interface{ Scan(...any) error }) (*TaskSchedule, error) {
	var s TaskSchedule
	var days, ids []byte
	err := row.Scan(&s.ID, &s.Name, &s.Enabled, &s.Type, &s.Timezone, &s.RunDate, &s.EndDate, &days, &s.StartTime, &s.EndTime, &s.CreatedAt, &s.UpdatedAt, &s.ManualUntil, &ids)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(days, &s.Weekdays); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(ids, &s.TaskIDs); err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *DB) ListTaskSchedules() ([]*TaskSchedule, error) {
	rows, err := d.Query(`SELECT ` + scheduleCols + ` FROM task_schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskSchedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (d *DB) GetTaskSchedule(id int64) (*TaskSchedule, error) {
	s, err := scanSchedule(d.QueryRow(`SELECT `+scheduleCols+` FROM task_schedules WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

// SaveTaskSchedule checks live, nonterminal membership inside the transaction.
// Server serializes this with archive/delete and calendar evaluation via concMu.
func (d *DB) SaveTaskSchedule(s *TaskSchedule) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	days, _ := json.Marshal(s.Weekdays)
	if s.ID == 0 {
		err = tx.QueryRow(`INSERT INTO task_schedules(name,enabled,type,timezone,run_date,end_date,weekdays,start_time,end_time)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id,created_at,updated_at`, s.Name, s.Enabled, s.Type, s.Timezone, s.RunDate, s.EndDate, days, s.StartTime, s.EndTime).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	} else {
		err = tx.QueryRow(`UPDATE task_schedules SET name=$2,enabled=$3,type=$4,timezone=$5,run_date=$6,end_date=$7,weekdays=$8,start_time=$9,end_time=$10,updated_at=now(),manual_until=$11 WHERE id=$1 RETURNING created_at,updated_at`, s.ID, s.Name, s.Enabled, s.Type, s.Timezone, s.RunDate, s.EndDate, days, s.StartTime, s.EndTime, s.ManualUntil).Scan(&s.CreatedAt, &s.UpdatedAt)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM task_schedule_tasks WHERE schedule_id=$1`, s.ID); err != nil {
		return err
	}
	for _, id := range s.TaskIDs {
		var taskID int64
		err = tx.QueryRow(`SELECT id FROM tasks WHERE id=$1 AND deleted_at IS NULL AND archived_at IS NULL AND status NOT IN ('done','failed','timeout') AND NOT EXISTS (SELECT 1 FROM task_archives a WHERE a.task_id=tasks.id AND a.state IN ('archive_queued','archiving')) FOR UPDATE`, id).Scan(&taskID)
		if err == sql.ErrNoRows {
			return fmt.Errorf("task %s is missing, archived, or terminal", id)
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO task_schedule_tasks(schedule_id,task_id) VALUES($1,$2)`, s.ID, taskID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (d *DB) DeleteTaskSchedule(id int64) error {
	_, err := d.Exec(`DELETE FROM task_schedules WHERE id=$1`, id)
	return err
}
func (d *DB) SetTaskScheduleEnabled(id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE task_schedules SET enabled=$2,manual_until=NULL,updated_at=now() WHERE id=$1`, id, enabled)
	return err
}
func (d *DB) AddTaskScheduleRun(scheduleID int64, taskID, action, status, message string) error {
	_, err := d.Exec(`INSERT INTO task_schedule_runs(schedule_id,task_id,action,status,error) VALUES($1,$2,$3,$4,$5)`, scheduleID, taskID, action, status, message)
	return err
}
func (d *DB) ListTaskScheduleRuns(id int64) ([]TaskScheduleRun, error) {
	rows, err := d.Query(`SELECT id,task_id::text,action,status,error,created_at FROM task_schedule_runs WHERE schedule_id=$1 ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskScheduleRun{}
	for rows.Next() {
		var r TaskScheduleRun
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Action, &r.Status, &r.Error, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) SetTaskScheduleOverride(id int64, enabled bool, until *time.Time) error {
	_, err := d.Exec(`UPDATE task_schedules SET enabled=$2,manual_until=$3,updated_at=now() WHERE id=$1`, id, enabled, until)
	return err
}
