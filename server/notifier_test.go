package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

type notifierFakeState struct {
	mu           sync.Mutex
	channels     []*db.NotificationChannel
	deliveries   []*db.NotificationDelivery
	digestDue    bool
	claimedLimit int
}

type notifierFakeDriver struct {
	state *notifierFakeState
}

func (d *notifierFakeDriver) Open(string) (driver.Conn, error) {
	return &notifierFakeConn{state: d.state}, nil
}
func (d *notifierFakeDriver) Connect(context.Context) (driver.Conn, error) { return d.Open("") }
func (d *notifierFakeDriver) Driver() driver.Driver                        { return d }

type notifierFakeConn struct{ state *notifierFakeState }
type notifierFakeTx struct{}

func (notifierFakeTx) Commit() error   { return nil }
func (notifierFakeTx) Rollback() error { return nil }

func (c *notifierFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused")
}
func (c *notifierFakeConn) Close() error              { return nil }
func (c *notifierFakeConn) Begin() (driver.Tx, error) { return notifierFakeTx{}, nil }
func (c *notifierFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return notifierFakeTx{}, nil
}

func (c *notifierFakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, "UPDATE notification_deliveries") {
		return driver.RowsAffected(1), nil
	}
	return driver.RowsAffected(0), nil
}

func (c *notifierFakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	switch {
	case strings.Contains(q, "FROM settings WHERE key=$1"):
		key, _ := args[0].Value.(string)
		switch key {
		case settingNotifyEnabled:
			return &notifierFakeRows{rows: [][]driver.Value{{"true"}}}, nil
		case settingNotifyDigestMinutes:
			return &notifierFakeRows{rows: [][]driver.Value{{"30"}}}, nil
		default:
			return &notifierFakeRows{}, nil
		}
	case strings.Contains(q, "FROM notification_channels WHERE enabled ORDER BY id"):
		return &notifierFakeRows{}, nil
	case strings.Contains(q, "FROM notification_events"):
		return &notifierFakeRows{}, nil
	case strings.Contains(q, "FROM notification_channels") && strings.Contains(q, "ORDER BY enabled DESC, id"):
		now := time.Now()
		var rows [][]driver.Value
		for _, ch := range c.state.channels {
			enabled := ch.IsEnabled()
			rows = append(rows, []driver.Value{
				ch.ID, ch.Name, ch.Kind, enabled, []byte(ch.Config), ch.Mode, []byte(ch.Filter), ch.RatePerMin, now, now,
			})
		}
		return &notifierFakeRows{rows: rows}, nil
	case strings.Contains(q, "SELECT EXISTS") && strings.Contains(q, "FROM notification_deliveries"):
		return &notifierFakeRows{rows: [][]driver.Value{{c.state.digestDue}}}, nil
	case strings.Contains(q, "SELECT dd.id FROM notification_deliveries dd"):
		chID, _ := args[0].Value.(int64)
		limit := int(args[len(args)-1].Value.(int64))
		c.state.claimedLimit = limit
		var rows [][]driver.Value
		for _, dl := range c.state.deliveries {
			if dl.ChannelID == chID && dl.State == db.NotifyStatePending && len(rows) < limit {
				rows = append(rows, []driver.Value{dl.ID})
			}
		}
		return &notifierFakeRows{rows: rows}, nil
	case strings.Contains(q, "FROM notification_deliveries d"):
		idSet := map[int64]bool{}
		for _, a := range args {
			if id, ok := a.Value.(int64); ok {
				idSet[id] = true
			}
		}
		var rows [][]driver.Value
		for _, dl := range c.state.deliveries {
			if !idSet[dl.ID] {
				continue
			}
			dl.State = db.NotifyStateSent
			var ch *db.NotificationChannel
			for _, candidate := range c.state.channels {
				if candidate.ID == dl.ChannelID {
					ch = candidate
					break
				}
			}
			if ch == nil {
				continue
			}
			rows = append(rows, []driver.Value{
				dl.ID, dl.EventID, dl.ChannelID, db.NotifyStateSending, dl.Attempts + 1, time.Now().Add(notifyLease),
				nil, nil, dl.CreatedAt, nil,
				[]byte(dl.Snapshot), notify.EventFindingCreated, dl.FindingID,
				ch.ID, ch.Name, ch.Kind, ch.IsEnabled(), []byte(ch.Config), ch.Mode, []byte(ch.Filter), ch.RatePerMin,
			})
		}
		return &notifierFakeRows{rows: rows}, nil
	case strings.Contains(q, "FROM assets WHERE id IN"):
		return &notifierFakeRows{}, nil
	default:
		return &notifierFakeRows{}, nil
	}
}

type notifierFakeRows struct {
	rows [][]driver.Value
	idx  int
}

func (r *notifierFakeRows) Columns() []string {
	if len(r.rows) == 0 {
		return []string{"col"}
	}
	cols := make([]string, len(r.rows[0]))
	for i := range cols {
		cols[i] = "c"
	}
	return cols
}
func (r *notifierFakeRows) Close() error { return nil }
func (r *notifierFakeRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func TestNotifierIdleTicksDoNotDrainTokensAndDeliveriesHonorRate(t *testing.T) {
	prevAllowLocal := os.Getenv(notify.AllowLocalTargetsEnv)
	_ = os.Setenv(notify.AllowLocalTargetsEnv, "1")
	t.Cleanup(func() {
		if prevAllowLocal == "" {
			_ = os.Unsetenv(notify.AllowLocalTargetsEnv)
		} else {
			_ = os.Setenv(notify.AllowLocalTargetsEnv, prevAllowLocal)
		}
	})

	var received atomic.Int64
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	cfgRaw, _ := json.Marshal(map[string]any{"url": webhookSrv.URL})
	enabled := true
	state := &notifierFakeState{
		channels: []*db.NotificationChannel{
			{ID: 1, Name: "rt", Kind: notify.KindWebhook, Mode: db.NotifyModeRealtime, Enabled: &enabled, RatePerMin: 5, Config: cfgRaw, Filter: json.RawMessage(`{}`)},
			{ID: 2, Name: "dg", Kind: notify.KindWebhook, Mode: db.NotifyModeDigest, Enabled: &enabled, RatePerMin: 1, Config: cfgRaw, Filter: json.RawMessage(`{}`)},
		},
	}
	sd := sql.OpenDB(&notifierFakeDriver{state: state})
	defer sd.Close()
	pg := &db.DB{DB: sd}
	n := &Notifier{pg: pg, buckets: map[int64]*notifyBucket{}}

	// Run 20 consecutive idle ticks (0 pending deliveries, digest not due).
	for range 20 {
		n.step(context.Background())
	}

	n.mu.Lock()
	rtTokens := n.buckets[1].tokens
	dgTokens := n.buckets[2].tokens
	n.mu.Unlock()
	if rtTokens < 4.99 {
		t.Fatalf("idle ticks drained realtime token bucket: got %.4f, want ~5", rtTokens)
	}
	if dgTokens < 0.99 {
		t.Fatalf("idle ticks drained digest token bucket: got %.4f, want ~1", dgTokens)
	}

	// Enqueue 7 realtime deliveries for channel 1 (rate_per_min=5, full bucket=5).
	snap, _ := json.Marshal(notify.Snapshot{Kind: notify.EventFindingCreated, FindingID: 1, Name: "SQLi", Severity: "high"})
	state.mu.Lock()
	for i := int64(1); i <= 7; i++ {
		state.deliveries = append(state.deliveries, &db.NotificationDelivery{
			ID:        i,
			EventID:   i,
			ChannelID: 1,
			State:     db.NotifyStatePending,
			Snapshot:  snap,
			FindingID: 1,
			CreatedAt: time.Now(),
		})
	}
	state.mu.Unlock()

	// First tick can send at most 5 (rate_per_min=5).
	n.step(context.Background())
	if got := received.Load(); got != 5 {
		t.Fatalf("first tick deliveries = %d, want 5 (rate_per_min=5)", got)
	}
	// Immediate second tick must be rate-limited (0 tokens left).
	n.step(context.Background())
	if got := received.Load(); got != 5 {
		t.Fatalf("immediate second tick bypassed rate limit: deliveries = %d, want 5", got)
	}

	// Partial claim (1 delivery when allow=5) should only consume 1 token and refund 4.
	state.mu.Lock()
	state.deliveries = []*db.NotificationDelivery{{
		ID:        99,
		EventID:   99,
		ChannelID: 1,
		State:     db.NotifyStatePending,
		Snapshot:  snap,
		FindingID: 1,
		CreatedAt: time.Now(),
	}}
	state.mu.Unlock()
	n.mu.Lock()
	n.buckets[1].tokens = 5
	n.buckets[1].lastFill = time.Now()
	n.mu.Unlock()

	n.step(context.Background())
	if got := received.Load(); got != 6 {
		t.Fatalf("partial claim deliveries = %d, want 6", got)
	}
	n.mu.Lock()
	remaining := n.buckets[1].tokens
	n.mu.Unlock()
	if remaining < 3.99 || remaining > 4.05 {
		t.Fatalf("partial claim refunded wrong tokens: remaining=%.4f, want ~4", remaining)
	}
}
