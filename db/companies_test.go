package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// cleanup helpers to remove test data
func cleanupCompany(d *DB, id int64) {
	d.Exec(`DELETE FROM company_scope WHERE company_id = $1`, id)
	d.Exec(`DELETE FROM companies WHERE id = $1`, id)
}

func TestCompanyUpsertAndGet(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, created, err := cs.UpsertCompany("Test Corp", "https://example.com/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	if !created {
		t.Error("first upsert should report created=true")
	}

	// duplicate: same nkey, should not create new
	id2, created2, err := cs.UpsertCompany("Test Corp", "")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Errorf("dedup failed: %d != %d", id2, id)
	}
	if created2 {
		t.Error("second upsert should report created=false")
	}

	c, err := cs.GetCompany(id)
	if err != nil || c == nil {
		t.Fatalf("GetCompany: %v", err)
	}
	if c.Name != "Test Corp" {
		t.Errorf("name: %q", c.Name)
	}

	// GetCompanyByName
	c2, err := cs.GetCompanyByName("test corp") // normalised
	if err != nil || c2 == nil {
		t.Fatalf("GetCompanyByName: %v", err)
	}
	if c2.ID != id {
		t.Errorf("GetCompanyByName id mismatch: %d vs %d", c2.ID, id)
	}
}

func TestCreateCompanyWithScopeRejectsNormalizedDuplicate(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	stamp := time.Now().UnixNano()
	name := fmt.Sprintf("Strict Company %d", stamp)
	id, added, _, _, validationErrors, err := cs.CreateCompanyWithScope(name, "", []ScopeInput{
		{Kind: "domain", Value: fmt.Sprintf("strict-%d.example", stamp)},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	if added != 1 || len(validationErrors) != 0 {
		t.Fatalf("initial scope: added=%d errors=%v", added, validationErrors)
	}

	_, _, _, _, _, err = cs.CreateCompanyWithScope(
		"  "+strings.ToUpper(strings.ReplaceAll(name, " ", "   "))+"  ",
		"",
		[]ScopeInput{{Kind: "domain", Value: fmt.Sprintf("replacement-%d.example", stamp)}},
		"test",
	)
	if !errors.Is(err, ErrCompanyNameConflict) {
		t.Fatalf("duplicate create error=%v want ErrCompanyNameConflict", err)
	}
	scope, err := cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0].Domain != fmt.Sprintf("strict-%d.example", stamp) {
		t.Fatalf("duplicate create changed existing scope: %+v", scope)
	}
}

func TestCreateCompanyWithScopeRollsBackOnScopeWriteFailure(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	name := fmt.Sprintf("Atomic Create %d", time.Now().UnixNano())
	_, _, _, _, _, err = cs.CreateCompanyWithScope(name, "", []ScopeInput{
		{Kind: "keyword", Value: "invalid\x00postgres-text"},
	}, "test")
	if err == nil {
		t.Fatal("expected scope database write to fail")
	}
	company, getErr := cs.GetCompanyByName(name)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if company != nil {
		defer cleanupCompany(d, company.ID)
		t.Fatalf("company row survived failed initial scope transaction: %+v", company)
	}
}

func TestCompanyScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("ScopeTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	lines := []string{"example.com", "192.168.1.0/24", "10.0.0.1"}
	added, skipped, invalid, errs := cs.AddScope(id, lines, "test")
	if added != 3 {
		t.Errorf("want 3 added, got %d (errs: %v)", added, errs)
	}
	if skipped != 0 || invalid != 0 {
		t.Errorf("unexpected skipped=%d invalid=%d", skipped, invalid)
	}

	// Adding again should skip (duplicate)
	added2, skipped2, invalid2, _ := cs.AddScope(id, lines, "test")
	if added2 != 0 || skipped2 != 3 {
		t.Errorf("want 0 added 3 skipped, got %d added %d skipped %d invalid", added2, skipped2, invalid2)
	}

	scope, err := cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 3 {
		t.Errorf("want 3 scope rules, got %d", len(scope))
	}
}

func TestCompanyScopeInvalid(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("InvalidScopeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	// Explicitly typed TLD-only domains and overly broad CIDRs should be
	// rejected. Untyped plain text is intentionally classified as a keyword.
	inputs := []ScopeInput{
		{Kind: "domain", Value: "com"},
		{Kind: "cidr", Value: "1.2.3.4/8"},
	}
	added, _, invalid, _ := cs.AddScopeInputs(id, inputs, "test")
	if added != 0 {
		t.Errorf("want 0 added for invalid lines, got %d", added)
	}
	if invalid != 2 {
		t.Errorf("want 2 invalid, got %d", invalid)
	}
}

func TestResolveCompany(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("ResolveCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	cs.AddScope(id, []string{"resolve-test.io", "10.20.0.0/16"}, "test")

	// domain match
	cid, err := cs.ResolveCompany("resolve-test.io", "")
	if err != nil || cid == nil || *cid != id {
		t.Errorf("domain resolve: want %d, got %v (err %v)", id, cid, err)
	}

	// no match
	cid2, err := cs.ResolveCompany("notinscope.com", "")
	if err != nil || cid2 != nil {
		t.Errorf("no-match: want nil, got %v", cid2)
	}

	// IP/CIDR match
	cid3, err := cs.ResolveCompany("", "10.20.5.1")
	if err != nil || cid3 == nil || *cid3 != id {
		t.Errorf("cidr resolve: want %d, got %v (err %v)", id, cid3, err)
	}

	// IP outside CIDR
	cid4, err := cs.ResolveCompany("", "10.30.0.1")
	if err != nil || cid4 != nil {
		t.Errorf("cidr no-match: want nil, got %v", cid4)
	}
}

func TestUpdateScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("UpdateScopeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	cs.AddScope(id, []string{"old-domain.com"}, "initial")

	// UpdateScope replaces
	added, invalid, errs := cs.UpdateScope(id, []string{"new-domain.com"}, "replacement")
	if added != 1 || invalid != 0 || len(errs) != 0 {
		t.Errorf("UpdateScope: added=%d invalid=%d errs=%v", added, invalid, errs)
	}

	scope, _ := cs.GetScope(id)
	if len(scope) != 1 || scope[0].Domain != "new-domain.com" {
		t.Errorf("UpdateScope: expected new-domain.com only, got %+v", scope)
	}

	// Invalid replacement input must not turn a partial validation response into
	// a destructive replacement of the existing rules.
	added, invalid, validationErrors, err := cs.UpdateScopeInputsChecked(id, []ScopeInput{
		{Kind: "domain", Value: "co.uk"},
	}, "invalid replacement")
	var validationErr *CompanyScopeValidationError
	if added != 0 || invalid != 1 || len(validationErrors) != 1 || !errors.As(err, &validationErr) {
		t.Fatalf("invalid replacement: added=%d invalid=%d validation=%v err=%v", added, invalid, validationErrors, err)
	}
	scope, err = cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0].Domain != "new-domain.com" {
		t.Fatalf("invalid replacement changed existing scope: %+v", scope)
	}
}

func TestUpdateScopeRollsBackOnInsertFailure(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	name := fmt.Sprintf("Atomic Scope Update %d", time.Now().UnixNano())
	id, _, err := cs.UpsertCompany(name, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	oldDomain := fmt.Sprintf("old-%d.example", time.Now().UnixNano())
	added, _, invalid, addErrors := cs.AddScopeInputs(id, []ScopeInput{{Kind: "domain", Value: oldDomain}}, "initial")
	if added != 1 || invalid != 0 || len(addErrors) != 0 {
		t.Fatalf("seed scope: added=%d invalid=%d errors=%v", added, invalid, addErrors)
	}

	added, invalid, updateErrors := cs.UpdateScopeInputs(id, []ScopeInput{
		{Kind: "keyword", Value: "invalid\x00postgres-text"},
	}, "replacement")
	if added != 0 || invalid != 0 || len(updateErrors) == 0 {
		t.Fatalf("failed update result: added=%d invalid=%d errors=%v", added, invalid, updateErrors)
	}
	scope, err := cs.GetScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0].Domain != oldDomain {
		t.Fatalf("failed replacement did not preserve old scope: %+v", scope)
	}
}

func TestDeleteCompany(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("DeleteMeCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	cs.AddScope(id, []string{"deletetest.com"}, "test")

	if err := cs.DeleteCompany(id); err != nil {
		t.Fatal(err)
	}
	c, err := cs.GetCompany(id)
	if err != nil || c != nil {
		t.Error("expected company to be gone")
	}
	// scope should be cascade-deleted
	scope, _ := cs.GetScope(id)
	if len(scope) != 0 {
		t.Errorf("expected scope cascade-deleted, got %d rules", len(scope))
	}
}

func TestDeleteCompanyWithAssetsDeletesBoth(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	stamp := time.Now().UnixNano()
	id, _, err := cs.UpsertCompany(fmt.Sprintf("Delete Assets Company %d", stamp), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	var assetID int64
	domain := fmt.Sprintf("delete-assets-%d.example", stamp)
	if err := d.QueryRow(`
INSERT INTO assets(type, domain, root_domain, company_id, company_source)
VALUES ('root_domain', $1, $1, $2, 'explicit')
RETURNING id`, domain, id).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id = $1`, assetID) //nolint:errcheck

	deleted, err := cs.DeleteCompanyWithAssets(id, true)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("assets deleted=%d want 1", deleted)
	}
	company, err := cs.GetCompany(id)
	if err != nil {
		t.Fatal(err)
	}
	if company != nil {
		t.Fatalf("company still exists: %+v", company)
	}
	var assetsRemaining int
	if err := d.QueryRow(`SELECT COUNT(*) FROM assets WHERE id = $1`, assetID).Scan(&assetsRemaining); err != nil {
		t.Fatal(err)
	}
	if assetsRemaining != 0 {
		t.Fatalf("asset %d survived company deletion", assetID)
	}
}

func TestRecomputeAttribution(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()
	as := d.Assets()

	id, _, err := cs.UpsertCompany("AttributeTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)
	defer d.Exec(`DELETE FROM assets WHERE root_domain = 'attr-test.com'`)

	// insert asset before adding scope
	assetID, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "attr-test.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id = $1`, assetID)

	// asset should not be attributed yet
	var companyID *int64
	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, assetID).Scan(&companyID)
	if companyID != nil {
		t.Error("expected no company before scope added")
	}

	// add scope and recompute
	cs.AddScope(id, []string{"attr-test.com"}, "test")
	if err := cs.RecomputeAttribution(); err != nil {
		t.Fatal(err)
	}

	d.QueryRow(`SELECT company_id FROM assets WHERE id = $1`, assetID).Scan(&companyID)
	if companyID == nil || *companyID != id {
		t.Errorf("RecomputeAttribution: expected company %d, got %v", id, companyID)
	}
}

func TestListCompanies(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	id, _, err := cs.UpsertCompany("ListTestCorp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, id)

	companies, err := cs.ListCompanies()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range companies {
		if c.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("ListCompanies: created company not found")
	}
}

// 批量读取回归仅使用显式 DSN，并为每个用例创建独立 schema。
func companyListTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("ARTEX_COMPANY_TEST_DSN")
	if dsn == "" {
		t.Skip("set ARTEX_COMPANY_TEST_DSN to an isolated PostgreSQL database")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("company_list_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Errorf("clean up isolated schema: %v", err)
		}
	})
	cfg = cfg.Copy()
	cfg.RuntimeParams["search_path"] = schema
	cfg.RuntimeParams["timezone"] = "UTC"
	cfg.RuntimeParams["statement_timeout"] = "5000"
	sqlDB := stdlib.OpenDB(*cfg)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	d := &DB{sqlDB}
	if _, err := d.Exec(`
CREATE TABLE companies (
    id bigint PRIMARY KEY, name text NOT NULL, nkey text NOT NULL, logo text,
    created_at timestamptz NOT NULL DEFAULT '2026-10-21 00:00:00+00',
    updated_at timestamptz NOT NULL DEFAULT '2026-10-21 01:00:00+00'
);
CREATE TABLE assets (id bigint PRIMARY KEY, company_id bigint REFERENCES companies(id));
CREATE TABLE company_scope (
    id bigint PRIMARY KEY, company_id bigint REFERENCES companies(id), kind text NOT NULL,
    domain text, net cidr, value text, raw text NOT NULL, reason text
)`); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestListCompaniesBatchScopeContract(t *testing.T) {
	d := companyListTestDB(t)
	if _, err := d.Exec(`
INSERT INTO companies (id, name, nkey, logo) VALUES
    (4294967300, 'Zulu 公司', 'zulu', 'https://example.test/logo.png'),
    (7, 'Beta empty', 'beta', ''), (-9, 'Alpha 公司', 'alpha', NULL);
INSERT INTO assets (id, company_id) VALUES (1, -9), (2, -9), (3, 4294967300), (4, NULL);
INSERT INTO company_scope (id, company_id, kind, domain, net, value, raw, reason) VALUES
    (60, -9, 'keyword', NULL, NULL, '关键字', '原始关键字', '关键字原因'),
    (50, 4294967300, 'icp', NULL, NULL, '京icp123号', '京 ICP 123号', NULL),
    (30, 4294967300, 'cidr', NULL, '192.0.2.0/24', NULL, '192.0.2.0/24', '网段'),
    (20, -9, 'domain', 'example.test', NULL, NULL, '*.example.test', NULL),
    (10, 4294967300, 'ip', NULL, '2001:db8::1/128', NULL, '2001:db8::1', 'IPv6')`); err != nil {
		t.Fatal(err)
	}
	logo, emptyLogo := "https://example.test/logo.png", ""
	company := func(id int64, name, nkey string, logo *string) Company {
		return Company{ID: id, Name: name, NKey: nkey, Logo: logo,
			CreatedAt: "2026-10-21 00:00:00+00", UpdatedAt: "2026-10-21 01:00:00+00"}
	}
	want := []*CompanyWithScope{
		{Company: company(-9, "Alpha 公司", "alpha", nil), AssetCount: 2, Scope: []ScopeRule{
			{ID: 20, CompanyID: -9, Kind: "domain", Domain: "example.test", Raw: "*.example.test"},
			{ID: 60, CompanyID: -9, Kind: "keyword", Value: "关键字", Raw: "原始关键字", Reason: "关键字原因"},
		}},
		{Company: company(7, "Beta empty", "beta", &emptyLogo), Scope: []ScopeRule{}},
		{Company: company(4294967300, "Zulu 公司", "zulu", &logo), AssetCount: 1, Scope: []ScopeRule{
			{ID: 10, CompanyID: 4294967300, Kind: "ip", Net: "2001:db8::1/128", Raw: "2001:db8::1", Reason: "IPv6"},
			{ID: 30, CompanyID: 4294967300, Kind: "cidr", Net: "192.0.2.0/24", Raw: "192.0.2.0/24", Reason: "网段"},
			{ID: 50, CompanyID: 4294967300, Kind: "icp", Value: "京icp123号", Raw: "京 ICP 123号"},
		}},
	}
	got, err := d.Companies().ListCompanies()
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("ListCompanies JSON=%s, want %s", gotJSON, wantJSON)
	}
	for _, c := range got {
		scope, err := d.Companies().GetScope(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(c.Scope, scope) {
			t.Fatalf("company %d scope=%+v, GetScope=%+v", c.ID, c.Scope, scope)
		}
	}
	if stats := d.Stats(); stats.MaxOpenConnections != 1 || stats.InUse != 0 || stats.WaitCount != 0 {
		t.Fatalf("unexpected connection use: %+v", stats)
	}
}

func TestListCompaniesBatchEmpty(t *testing.T) {
	d := companyListTestDB(t)
	// 空公司列表无需读取 company_scope。
	if _, err := d.Exec(`DROP TABLE company_scope`); err != nil {
		t.Fatal(err)
	}
	got, err := d.Companies().ListCompanies()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || string(encoded) != "null" {
		t.Fatalf("empty list JSON=%s, err=%v; want null", encoded, err)
	}
}

func TestListCompaniesBatchErrors(t *testing.T) {
	cases := []struct {
		name, ddl, code, message string
	}{
		{"company query", `DROP TABLE assets`, "42P01", ""},
		{"scope query", `DROP TABLE company_scope`, "42P01", ""},
		{"company scan", `ALTER TABLE companies ALTER COLUMN name DROP NOT NULL; UPDATE companies SET name = NULL`, "", "converting NULL to string"},
		{"scope scan", `ALTER TABLE company_scope ALTER COLUMN kind DROP NOT NULL; INSERT INTO company_scope (id, company_id, raw) VALUES (1, 1, 'raw')`, "", "converting NULL to string"},
		{"scope iteration", `
DROP TABLE company_scope;
CREATE FUNCTION scope_raw(n bigint) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    IF n = 4096 THEN RETURN n::text::cidr::text; END IF;
    RETURN repeat('x', 128);
END $$;
CREATE TABLE scope_source (n bigint NOT NULL, company_id bigint NOT NULL, PRIMARY KEY (company_id, n));
INSERT INTO scope_source SELECT n, 1 FROM generate_series(1::bigint, 4096) AS n;
SET enable_seqscan = off;
SET enable_bitmapscan = off;
CREATE VIEW company_scope AS
SELECT n AS id, company_id, 'keyword'::text AS kind,
       NULL::text AS domain, NULL::cidr AS net, NULL::text AS value,
       scope_raw(n) AS raw, NULL::text AS reason
FROM scope_source`, "22P02", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := companyListTestDB(t)
			if _, err := d.Exec(`INSERT INTO companies (id, name, nkey) VALUES (1, 'Company', 'company')`); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(tc.ddl); err != nil {
				t.Fatal(err)
			}
			if tc.name == "scope iteration" {
				rows, err := d.Query(`
SELECT id, company_id, kind,
       COALESCE(domain,''), COALESCE(net::text,''), COALESCE(value,''), raw, COALESCE(reason,'')
FROM company_scope WHERE company_id = ANY($1) ORDER BY company_id, id`, []int64{1})
				if err != nil {
					t.Fatalf("iteration fixture failed before reading rows: %v", err)
				}
				read := 0
				for rows.Next() {
					read++
				}
				iterationErr := rows.Err()
				rows.Close()
				if read == 0 || iterationErr == nil {
					t.Fatalf("iteration fixture read=%d, err=%v; want rows before error", read, iterationErr)
				}
				t.Logf("PostgreSQL returned %d rows before iteration error: %v", read, iterationErr)
			}
			got, err := d.Companies().ListCompanies()
			if err == nil || got != nil {
				t.Fatalf("ListCompanies=%+v, err=%v; want nil and error", got, err)
			}
			if tc.code != "" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
					t.Fatalf("error=%v, want PostgreSQL SQLSTATE %s", err, tc.code)
				}
			} else if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error=%v, want %q", err, tc.message)
			}
			if stats := d.Stats(); stats.InUse != 0 {
				t.Fatalf("connection retained after error: %+v", stats)
			}
		})
	}
}
