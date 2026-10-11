package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/db"
	"golang.org/x/crypto/bcrypt"
)

// This settings-only SQL fixture exercises production credential reads without
// a production fallback for servers whose DB or password was never configured.
type authFixtureStore struct {
	mu        sync.Mutex
	values    map[string]string
	err       error
	beforeCAS func()
}

type authFixtureConnector struct{ store *authFixtureStore }

func (c authFixtureConnector) Connect(context.Context) (driver.Conn, error) {
	return authFixtureConn{c.store}, nil
}
func (c authFixtureConnector) Driver() driver.Driver { return authFixtureDriver{} }

type authFixtureDriver struct{}

func (authFixtureDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use auth fixture connector")
}

type authFixtureConn struct{ store *authFixtureStore }

func (authFixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported fixture prepare")
}
func (authFixtureConn) Close() error { return nil }
func (authFixtureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unsupported fixture transaction")
}
func (c authFixtureConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	if c.store.err != nil {
		return nil, c.store.err
	}
	if !strings.HasPrefix(query, "SELECT value FROM settings") {
		return nil, errors.New("unexpected fixture query")
	}
	value, ok := c.store.values[args[0].Value.(string)]
	return &authFixtureRows{value: value, present: ok}, nil
}
func (c authFixtureConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	if c.store.err != nil {
		return nil, c.store.err
	}
	key := args[0].Value.(string)
	switch {
	case strings.Contains(query, "ON CONFLICT (key) DO NOTHING"):
		if _, exists := c.store.values[key]; exists {
			return driver.RowsAffected(0), nil
		}
		c.store.values[key] = args[1].Value.(string)
	case strings.HasPrefix(query, "UPDATE settings SET value=$3"):
		if c.store.beforeCAS != nil {
			c.store.beforeCAS()
		}
		if c.store.values[key] != args[1].Value.(string) {
			return driver.RowsAffected(0), nil
		}
		c.store.values[key] = args[2].Value.(string)
	case strings.Contains(query, "ON CONFLICT (key) DO UPDATE"):
		c.store.values[key] = args[1].Value.(string)
	case strings.HasPrefix(query, "DELETE FROM settings"):
		if len(args) > 1 && c.store.values[key] != args[1].Value.(string) {
			return driver.RowsAffected(0), nil
		}
		delete(c.store.values, key)
	default:
		return nil, errors.New("unexpected fixture statement")
	}
	return driver.RowsAffected(1), nil
}

type authFixtureRows struct {
	value   string
	present bool
}

func (*authFixtureRows) Columns() []string { return []string{"value"} }
func (*authFixtureRows) Close() error      { return nil }
func (r *authFixtureRows) Next(values []driver.Value) error {
	if !r.present {
		return io.EOF
	}
	values[0], r.present = r.value, false
	return nil
}

func newAuthMemoryServer(t *testing.T) (*Server, *authFixtureStore) {
	t.Helper()
	store := &authFixtureStore{values: map[string]string{}}
	conn := sql.OpenDB(authFixtureConnector{store})
	t.Cleanup(func() { conn.Close() })
	key := []byte("test-only-auth-base-key-32-characters")
	return &Server{m: &Manager{pg: &db.DB{DB: conn}}, jwtKey: key, setupToken: deriveSetupToken(key)}, store
}

func signAdminJWTForTest(t *testing.T, s *Server) (string, error) {
	t.Helper()
	if s.m == nil || s.m.pg == nil {
		return "", errors.New("test admin database unavailable")
	}
	hash, _, err := s.m.pg.GetSetting(authPassKey)
	if err != nil {
		return "", err
	}
	if hash == "" {
		generated, err := bcrypt.GenerateFromPassword([]byte("fixture-admin-password"), bcrypt.MinCost)
		if err != nil {
			return "", err
		}
		inserted, err := s.m.pg.InsertSettingIfAbsent(authPassKey, string(generated))
		if err != nil {
			return "", err
		}
		if inserted {
			t.Cleanup(func() {
				// Preserve a password another test deliberately changed meanwhile.
				_, _ = s.m.pg.Exec("DELETE FROM settings WHERE key=$1 AND value=$2", authPassKey, string(generated))
			})
		}
		hash, _, err = s.m.pg.GetSetting(authPassKey)
		if err != nil {
			return "", err
		}
	}
	return s.signAdminJWT(hash)
}
