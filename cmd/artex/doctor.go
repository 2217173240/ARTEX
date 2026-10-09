package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type doctorReport struct {
	Checks []doctorCheck `json:"checks"`
}

func runDoctor(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	// Do not echo arbitrary argument values: they may contain credentials.
	fs.SetOutput(io.Discard)
	data := fs.String("data", filepath.Join(config.BaseDir(), "data"), "data directory")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(out, "Usage: artex doctor [-data directory] [-json]")
		return 2
	}
	r := inspectDoctor(*data)
	code := 0
	for _, c := range r.Checks {
		if c.Status == "FAIL" {
			code = 1
		}
	}
	if *asJSON {
		_ = json.NewEncoder(out).Encode(r)
	} else {
		fmt.Fprintln(out, "ARTEX doctor (read-only; no services or model calls)")
		for _, c := range r.Checks {
			fmt.Fprintf(out, "[%s] %s: %s\n", c.Status, c.Name, c.Detail)
		}
	}
	return code
}

func inspectDoctor(data string) doctorReport {
	r := doctorReport{}
	add := func(status, name, detail string) { r.Checks = append(r.Checks, doctorCheck{name, status, detail}) }
	b, err := os.ReadFile(config.Path())
	var cfg config.Config
	switch {
	case os.IsNotExist(err):
		add("WARN", "config", "file absent; database environment override may still be used")
	case err != nil:
		add("FAIL", "config", "file cannot be read")
	case json.Unmarshal(b, &cfg) != nil:
		add("FAIL", "config", "invalid JSON or field types")
	default:
		add("OK", "config", "file parsed")
	}
	checkDoctorDir(data, "data directory", add)
	skill := strings.TrimSpace(os.Getenv("ARTEX_SKILL_DIR"))
	if skill == "" {
		skill = strings.TrimSpace(cfg.SkillDir)
	}
	if skill == "" {
		skill = filepath.Join(config.BaseDir(), "skills")
	}
	checkDoctorDir(skill, "skills directory", add)
	for _, name := range []string{"node", "python3", "git", "playwright-cli"} {
		if _, e := exec.LookPath(name); e != nil {
			add("WARN", name, "optional dependency unavailable on PATH")
		} else {
			detail := "available on PATH; version and execution not tested"
			if name == "node" {
				detail += "; api-recon requires >=22.12.0"
			}
			add("OK", name, detail)
		}
	}
	for _, relative := range []string{"api-recon/SKILL.md", "api-recon/scripts/runtime_harvest.js", "api-recon/scripts/node_modules/puppeteer-core/package.json", "playwright-cli/SKILL.md"} {
		info, err := os.Stat(filepath.Join(skill, filepath.FromSlash(relative)))
		if err != nil || !info.Mode().IsRegular() {
			add("WARN", "optional skill asset "+relative, "absent or inaccessible; install the skill dependencies if using this capability")
		} else {
			add("OK", "optional skill asset "+relative, "present; runtime operation not tested")
		}
	}
	dsn, _, err := config.PostgresDSN()
	if err != nil {
		add("FAIL", "database", "no database connection configured")
		add("SKIP", "LLM profiles", "database unavailable")
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probeDoctorDatabase(ctx, dsn, add)
	return r
}

func checkDoctorDir(path, name string, add func(string, string, string)) {
	info, err := os.Stat(path)
	if err != nil {
		add("WARN", name, "directory absent or inaccessible; no directory created")
		return
	}
	if !info.IsDir() {
		add("FAIL", name, "configured path is not a directory")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		add("WARN", name, "directory cannot be opened for reading")
		return
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if err != nil && err != io.EOF {
		add("WARN", name, "directory entries cannot be read")
		return
	}
	add("OK", name, fmt.Sprintf("exists and readable; mode %04o; writability not tested", info.Mode().Perm()))
}

// Unlike db.Open, this connection cannot create a database, migrate or seed it.
// Only fixed catalog/aggregate SELECTs run inside an explicit read-only transaction.
// Driver errors are deliberately not printed: they can contain DSNs or secrets.
func probeDoctorDatabase(ctx context.Context, dsn string, add func(string, string, string)) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		add("FAIL", "database", "invalid connection configuration")
		add("SKIP", "LLM profiles", "database unavailable")
		return
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnectTimeout = 5 * time.Second
	conn := stdlib.OpenDB(*cfg)
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		add("FAIL", "database", "connection failed (credentials and driver details withheld)")
		add("SKIP", "LLM profiles", "database unavailable")
		return
	}
	defer tx.Rollback()
	add("OK", "database", "connected using a read-only transaction")
	var missing int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM unnest(ARRAY['companies','assets','explorations','exploration_nodes','tasks','task_archives','agents','settings','llm_profiles','mcp_servers','findings','conversations']) AS required(name) WHERE to_regclass(name) IS NULL`).Scan(&missing)
	if err != nil || missing != 0 {
		detail := "catalog check failed; no migration attempted"
		if err == nil {
			detail = fmt.Sprintf("%d required core tables absent; no migration attempted", missing)
		}
		add("FAIL", "schema", detail)
		add("SKIP", "LLM profiles", "schema unavailable")
		return
	}
	add("OK", "schema", "core tables present; full migration compatibility not verified")
	var total, defaults, keyed int
	err = tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE is_default), count(*) FILTER (WHERE COALESCE(api_key,'') <> '') FROM llm_profiles`).Scan(&total, &defaults, &keyed)
	if err != nil {
		add("FAIL", "LLM profiles", "profile metadata unavailable; schema may require migration")
		return
	}
	status := "OK"
	if total == 0 || defaults == 0 {
		status = "WARN"
	}
	add(status, "LLM profiles", fmt.Sprintf("%d configured, %d default, %d with credentials; provider availability not tested", total, defaults, keyed))
}
