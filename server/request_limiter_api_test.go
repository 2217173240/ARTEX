package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// These regressions require an explicitly selected disposable database. Never
// resolve the user's config file or ordinary database environment implicitly.
func requestLimiterAPIFixture(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	dsn := os.Getenv("ARTEX_RATE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("explicit disposable ARTEX_RATE_TEST_PG_DSN required")
	}
	t.Setenv("ARTEX_CONFIG", "/dev/null")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	m := &Manager{pg: d, dir: t.TempDir(), tasks: map[string]*Task{}}
	return &Server{m: m}, d
}

func TestProfileRequestRateAPIRejectsBeforePersistence(t *testing.T) {
	s, d := requestLimiterAPIFixture(t)
	p := &db.LLMProfile{Name: "rate-validation-fixture", Format: "openai", Model: "fixture", BaseURL: "http://example.invalid", APIKey: "fixture-only-not-a-secret", Streaming: true, RatePerMinute: 1}
	id, err := d.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	before, err := d.ProfileByID(id)
	if err != nil {
		t.Fatal(err)
	}
	live, _, ok := s.providerForProfile(id)
	if !ok {
		t.Fatal("could not build fixture provider")
	}
	generation := s.provCacheGen
	var initialCount int
	if err := d.QueryRow(`SELECT count(*) FROM llm_profiles`).Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	for _, profileID := range []int64{0, id} {
		for _, field := range []string{"rate_per_second", "rate_per_minute"} {
			for _, rate := range []float64{-1, 1e-300, 1e300} {
				payload := map[string]any{"id": profileID, "name": "invalid-rate-fixture", "format": "openai", "model": "fixture", field: rate}
				body, _ := json.Marshal(payload)
				rec := httptest.NewRecorder()
				s.pgSaveProfile(rec, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(body)))
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("id=%d field=%s rate=%g: status=%d", profileID, field, rate, rec.Code)
				}
				after, err := d.ProfileByID(id)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("invalid request changed the existing profile")
				}
				var count int
				if err := d.QueryRow(`SELECT count(*) FROM llm_profiles`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != initialCount || s.provCacheGen != generation || s.provByProfile[id].prov != live {
					t.Fatal("invalid request persisted a profile or changed the live provider/cache")
				}
			}
		}
	}
	// Valid zero and fractional limits remain accepted without provider keys or
	// a model connection. This handler validates quota values, not credentials.
	for i, rate := range []float64{0, .5} {
		body, _ := json.Marshal(map[string]any{"name": "valid-rate-fixture-" + strconv.Itoa(i), "format": "openai", "model": "fixture", "rate_per_second": rate, "rate_per_minute": rate})
		rec := httptest.NewRecorder()
		s.pgSaveProfile(rec, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("valid rate %g rejected: status=%d", rate, rec.Code)
		}
		var result struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, result.ID) })
		stored, err := d.ProfileByID(result.ID)
		if err != nil || stored == nil || stored.RatePerSecond != rate || stored.RatePerMinute != rate {
			t.Fatal("valid rate was not persisted")
		}
	}
}

func TestLegacySetLLMRetainsPersistedProfileQuota(t *testing.T) {
	s, d := requestLimiterAPIFixture(t)
	profiles, err := d.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.Name == "default" {
			t.Fatal("legacy regression requires disposable fixture without an existing default profile")
		}
	}
	previousActive, err := d.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"fixture failure"}}`)
	}))
	defer upstream.Close()
	p := &db.LLMProfile{Name: "default", Format: "openai", Model: "fixture", BaseURL: upstream.URL, APIKey: "fixture-only-not-a-secret", RatePerMinute: 1, Streaming: false, MaxTokens: 73, MaxTokensField: llm.MaxTokensFieldCompletion, SessionHeaderKey: "X-Fixture-Session", Priority: 7, PoolExclude: true, Retry: db.RetryOverride{Connect: db.RetryRule{Attempts: -1}, Empty: db.RetryRule{Attempts: -1}, Stream: db.RetryRule{Attempts: 1, IntervalMS: 17}}}
	id, err := d.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)
		if previousActive != nil {
			_ = d.SetActiveProfile(previousActive.ID)
		}
	})
	if err := d.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	cfg, ok := s.loadLLMConfig()
	if !ok {
		t.Fatal("could not load fixture")
	}
	if err := s.applyLLM(cfg); err != nil {
		t.Fatal(err)
	}
	req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("fixture")}}
	s.llmDirect.Complete(context.Background(), req)
	if requests.Load() != 1 {
		t.Fatal("initial request not sent")
	}
	body, _ := json.Marshal(map[string]any{"provider": "openai", "model": "fixture", "base_url": upstream.URL, "rate_per_minute": 1})
	rec := httptest.NewRecorder()
	s.setLLM(rec, httptest.NewRequest(http.MethodPost, "/api/llm", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy save failed: status=%d", rec.Code)
	}
	cached, loaded, ok := s.providerForProfile(id)
	if !ok || cached != s.llmDirect {
		t.Fatal("legacy save created a second global provider/quota")
	}
	if loaded.Stream || loaded.MaxTokens != 73 || loaded.MaxTokensField != llm.MaxTokensFieldCompletion || loaded.SessionHeaderKey != "X-Fixture-Session" || loaded.Retry != cfg.Retry || s.llmCfg != loaded {
		t.Fatal("legacy save did not apply preserved persisted configuration")
	}
	stored, err := d.ProfileByID(id)
	if err != nil || stored.Priority != 7 || !stored.PoolExclude || stored.Retry != p.Retry {
		t.Fatal("legacy save changed unrelated profile fields")
	}
	if _, exists := s.requestLimiters[0]; exists {
		t.Fatal("persisted legacy save allocated global key0 quota")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, _, _, err = s.llmDirect.Complete(ctx, req)
	if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 1 {
		t.Fatalf("legacy save discarded prior quota: %v, requests=%d", err, requests.Load())
	}
}

func TestProfileRequestRateValidationWithoutDatabase(t *testing.T) {
	// The existing settings-only fixture does not support profile writes. A 400
	// here proves invalid rates are rejected before any persistence dependency.
	s, _ := newAuthMemoryServer(t)
	rec := httptest.NewRecorder()
	s.pgSaveProfile(rec, httptest.NewRequest(http.MethodPost, "/api/llm/profiles", bytes.NewBufferString(`{"name":"fixture","rate_per_minute":-1}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid rate reached persistence: status=%d", rec.Code)
	}
}
