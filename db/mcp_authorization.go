package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrMCPInvalid = errors.New("invalid MCP configuration")

var ErrMCPRevision = errors.New("MCP revision conflict; reload configuration")
var ErrMCPUnauthorized = errors.New("MCP configuration is disabled, unapproved, changed, or missing")

// SaveMCP saves a proposal. Only ApproveMCP can grant execution authority.
// Every existing-row write uses CAS; omitted env preserves stored credentials.
func (d *DB) SaveMCP(m *MCPServer) (int64, error) { return d.SaveMCPContext(context.Background(), m) }
func (d *DB) SaveMCPContext(ctx context.Context, m *MCPServer) (int64, error) {
	if err := validateMCP(m); err != nil {
		return m.ID, err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	args := string(m.Args)
	if args == "" {
		args = "[]"
	}
	env := string(m.Env)
	if env == "" {
		env = "{}"
	}
	if m.ID == 0 {
		err = tx.QueryRowContext(ctx, `INSERT INTO mcp_servers(name,transport,command,args,env,url,enabled,insecure) VALUES($1,$2,NULLIF($3,''),$4,$5,NULLIF($6,''),false,$7) RETURNING id,revision`, m.Name, m.Transport, m.Command, args, env, m.URL, m.Insecure).Scan(&m.ID, &m.Revision)
		m.Enabled = false
		m.Approved = false
		m.ApprovedRevision = 0
	} else {
		// JSONB equality makes key ordering irrelevant. Unchanged saves preserve the
		// operator's activation state, so a model cannot reverse an explicit disable.
		err = tx.QueryRowContext(ctx, `UPDATE mcp_servers SET name=$1,transport=$2,command=NULLIF($3,''),args=$4,env=COALESCE($5::jsonb,env),url=NULLIF($6,''),insecure=$7,
 enabled=CASE WHEN name=$1 AND transport=$2 AND COALESCE(command,'')=$3 AND args=$4::jsonb AND env=COALESCE($5::jsonb,env) AND COALESCE(url,'')=$6 AND insecure=$7 THEN enabled ELSE false END,
 approved_revision=CASE WHEN name=$1 AND transport=$2 AND COALESCE(command,'')=$3 AND args=$4::jsonb AND env=COALESCE($5::jsonb,env) AND COALESCE(url,'')=$6 AND insecure=$7 THEN approved_revision ELSE 0 END,
 revision=CASE WHEN name=$1 AND transport=$2 AND COALESCE(command,'')=$3 AND args=$4::jsonb AND env=COALESCE($5::jsonb,env) AND COALESCE(url,'')=$6 AND insecure=$7 THEN revision ELSE revision+1 END
 WHERE id=$8 AND revision=$9 RETURNING revision,approved_revision,enabled`, m.Name, m.Transport, m.Command, args, nullableMCPEnv(m.Env), m.URL, m.Insecure, m.ID, m.Revision).Scan(&m.Revision, &m.ApprovedRevision, &m.Enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return m.ID, ErrMCPRevision
		}
		if err != nil {
			return m.ID, err
		}
		m.Approved = m.Revision == m.ApprovedRevision
	}
	if err != nil {
		return m.ID, err
	}
	return m.ID, tx.Commit()
}
func nullableMCPEnv(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// SetMCPApproval advances the epoch even when repeating approval or disable.
func (d *DB) SetMCPApproval(id, revision int64, approve bool) (*MCPServer, error) {
	return d.SetMCPApprovalContext(context.Background(), id, revision, approve)
}
func (d *DB) SetMCPApprovalContext(ctx context.Context, id, revision int64, approve bool) (*MCPServer, error) {
	m := &MCPServer{ID: id}
	err := d.QueryRowContext(ctx, `UPDATE mcp_servers SET revision=revision+1,approved_revision=CASE WHEN $3 THEN revision+1 ELSE 0 END,enabled=$3 WHERE id=$1 AND revision=$2 RETURNING revision,approved_revision,enabled`, id, revision, approve).Scan(&m.Revision, &m.ApprovedRevision, &m.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMCPRevision
	}
	if err != nil {
		return nil, err
	}
	m.Approved = m.Revision == m.ApprovedRevision
	return m, nil
}

// AuthorizedMCP is the admission check. Calls admitted before a mutation may
// finish; subsequent calls and old clients fail closed, including on DB errors.
func (d *DB) AuthorizedMCP(id, revision int64) (*MCPServer, error) {
	return d.AuthorizedMCPContext(context.Background(), id, revision)
}
func (d *DB) AuthorizedMCPContext(ctx context.Context, id, revision int64) (*MCPServer, error) {
	m := &MCPServer{ID: id}
	var args, env []byte
	err := d.QueryRowContext(ctx, `SELECT name,transport,COALESCE(command,''),args,env,COALESCE(url,''),enabled,insecure,revision,approved_revision FROM mcp_servers WHERE id=$1 AND revision=$2 AND approved_revision=revision AND enabled`, id, revision).Scan(&m.Name, &m.Transport, &m.Command, &args, &env, &m.URL, &m.Enabled, &m.Insecure, &m.Revision, &m.ApprovedRevision)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMCPUnauthorized, err)
	}
	m.Args = args
	m.Env = env
	m.Approved = true
	return m, nil
}

// Validate shape without reflecting credential-bearing values into errors.
func validateMCP(m *MCPServer) error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w: name required", ErrMCPInvalid)
	}
	switch m.Transport {
	case "stdio", "http", "sse":
	default:
		return fmt.Errorf("%w: unsupported transport", ErrMCPInvalid)
	}
	if len(m.Args) > 0 {
		var args []any
		if err := json.Unmarshal(m.Args, &args); err != nil || args == nil {
			return fmt.Errorf("%w: args must be a string array", ErrMCPInvalid)
		}
		for _, v := range args {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%w: args must be a string array", ErrMCPInvalid)
			}
		}
	}
	if len(m.Env) > 0 {
		var env map[string]any
		if err := json.Unmarshal(m.Env, &env); err != nil || env == nil {
			return fmt.Errorf("%w: env must be a string map", ErrMCPInvalid)
		}
		for _, v := range env {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%w: env must be a string map", ErrMCPInvalid)
			}
		}
	}
	if m.URL != "" && m.Transport != "stdio" {
		u, err := url.Parse(m.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("%w: URL must use http or https", ErrMCPInvalid)
		}
	}
	return nil
}
