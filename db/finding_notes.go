package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxFindingNoteRunes = 8000

var (
	ErrFindingNoteInvalid     = errors.New("note body must contain 1 to 8000 characters")
	ErrFindingNoteNotFound    = errors.New("finding note not found")
	ErrFindingReadOnly        = errors.New("inherited finding is read-only; edit it in its source task")
	ErrFindingContextNotFound = errors.New("context task not found")
	ErrFindingUnavailable     = errors.New("finding not available in task context")
)

type FindingNote struct {
	ID        int64
	FindingID int64
	Author    string
	Body      string
	CreatedAt time.Time
}

func normalizeFindingNote(body string) (string, error) {
	body = strings.TrimSpace(body)
	if !utf8.ValidString(body) || utf8.RuneCountInString(body) < 1 || utf8.RuneCountInString(body) > MaxFindingNoteRunes {
		return "", ErrFindingNoteInvalid
	}
	return body, nil
}

// CheckFindingContextTx checks current database provenance, rather than a cached
// task handle. A source-less finding is global-only. Only direct inheritance is
// visible, matching the task findings view.
func CheckFindingContextTx(tx *sql.Tx, findingID, contextTaskID int64, write bool) error {
	var source sql.NullInt64
	if err := tx.QueryRow(`SELECT task_id FROM findings WHERE id=$1`, findingID).Scan(&source); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFindingNotFound
		}
		return err
	}
	if contextTaskID == 0 {
		return nil
	}
	var live bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, contextTaskID).Scan(&live); err != nil {
		return err
	}
	if !live {
		return ErrFindingContextNotFound
	}
	if source.Valid && source.Int64 == contextTaskID {
		return nil
	}
	var inherited bool
	if source.Valid {
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_relations WHERE task_id=$1 AND source_task_id=$2)`, contextTaskID, source.Int64).Scan(&inherited); err != nil {
			return err
		}
	}
	if !inherited {
		return ErrFindingUnavailable
	}
	if write {
		return ErrFindingReadOnly
	}
	return nil
}

func lockFindingManualTx(tx *sql.Tx, findingID, contextTaskID int64) error {
	if err := LockFindingEvidenceTx(tx, findingID, nil); err != nil {
		return err
	}
	return CheckFindingContextTx(tx, findingID, contextTaskID, true)
}

func (d *DB) ListFindingNotes(ctx context.Context, findingID, contextTaskID, before int64, limit int) ([]FindingNote, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if err = CheckFindingContextTx(tx, findingID, contextTaskID, false); err != nil {
		return nil, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,finding_id,author,body,created_at FROM finding_notes WHERE finding_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, findingID, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	notes := []FindingNote{}
	for rows.Next() {
		var n FindingNote
		if err = rows.Scan(&n.ID, &n.FindingID, &n.Author, &n.Body, &n.CreatedAt); err != nil {
			return nil, false, err
		}
		notes = append(notes, n)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(notes) > limit
	if more {
		notes = notes[:limit]
	}
	return notes, more, nil
}

func (d *DB) AddFindingNote(ctx context.Context, findingID, contextTaskID int64, author, body string) (note *FindingNote, err error) {
	body, err = normalizeFindingNote(body)
	if err != nil {
		return nil, err
	}
	author = strings.TrimSpace(author)
	if author == "" {
		return nil, errors.New("note author is required")
	}
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		if err := lockFindingManualTx(tx, findingID, contextTaskID); err != nil {
			return err
		}
		note = &FindingNote{FindingID: findingID, Author: author, Body: body}
		return tx.QueryRowContext(ctx, `INSERT INTO finding_notes(finding_id,author,body) VALUES($1,$2,$3) RETURNING id,created_at`, findingID, author, body).Scan(&note.ID, &note.CreatedAt)
	})
	return
}

func (d *DB) DeleteFindingNote(ctx context.Context, findingID, contextTaskID, noteID int64) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		if err := lockFindingManualTx(tx, findingID, contextTaskID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM finding_notes WHERE id=$1 AND finding_id=$2`, noteID, findingID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrFindingNoteNotFound
		}
		return nil
	})
}

// FindingManualPatch contains only editable triage metadata. Reports and
// evidence remain immutable through this path.
type FindingManualPatch struct{ Status, Severity, Name, VulnClass *string }

func (d *DB) PatchFindingManual(ctx context.Context, findingID, contextTaskID int64, actor string, p FindingManualPatch) error {
	if p.Status != nil && !ValidFindingStatus(*p.Status) {
		return errors.New("invalid status")
	}
	if p.Severity != nil && !ValidSeverity(*p.Severity) {
		return errors.New("invalid severity")
	}
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		if err := lockFindingManualTx(tx, findingID, contextTaskID); err != nil {
			return err
		}
		var node sql.NullInt64
		if err := tx.QueryRow(`SELECT node_id FROM findings WHERE id=$1`, findingID).Scan(&node); err != nil {
			return err
		}
		for _, item := range []struct {
			column string
			value  *string
		}{{"severity", p.Severity}, {"name", p.Name}, {"vulnclass", p.VulnClass}} {
			if item.value == nil {
				continue
			}
			value := strings.TrimSpace(*item.value)
			if _, err := tx.Exec(`UPDATE findings SET `+item.column+`=$1 WHERE id=$2`, value, findingID); err != nil {
				return err
			}
			if node.Valid {
				if _, err := tx.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload, ARRAY[$1::text],to_jsonb($2::text)) WHERE id=$3`, item.column, value, node.Int64); err != nil {
					return err
				}
			}
		}
		if p.Status != nil {
			_, _, _, _, err := SetFindingManualStatusTx(ctx, tx, findingID, *p.Status, actor)
			return err
		}
		return nil
	})
}
