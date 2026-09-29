package eval

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/chatgpt"
	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
)

func TestScoringSeparatesReferenceCoverageAndValidity(t *testing.T) {
	c := Case{Suite: "martian", Expected: []Expected{{ID: "bug", Category: "bug"}, {ID: "missed", Category: "security"}, {ID: "style", Category: "style"}}}
	j := Judgment{Findings: []Finding{
		{ID: "f1", Verdict: "valid", Matches: []string{"bug"}},
		{ID: "f2", Verdict: "valid", Category: "maintainability", Matches: []string{}},
		{ID: "f3", Verdict: "unresolved", Matches: []string{}},
		{ID: "f4", Verdict: "valid", Matches: []string{"style"}},
		{ID: "f5", Verdict: "valid", DuplicateOf: "f1"},
	}}
	got := score(c, j)
	if got.TP != 1 || got.FP != 2 || got.FN != 1 || got.Valid != 3 || got.Unresolved != 1 || got.Duplicates != 1 || got.Maintainability != 1 {
		t.Fatalf("wrong counts: %+v", got)
	}
	c.Suite = "personal"
	c.Expected = nil
	got = score(c, j)
	if got.FP != 0 || got.FN != 0 || got.TP != 0 || got.Valid != 3 {
		t.Fatalf("unlabeled cases gained reference scores: %+v", got)
	}
}
func TestJudgmentRejectsInventedAndDuplicateMatches(t *testing.T) {
	c := Case{Expected: []Expected{{ID: "g1"}}}
	base := Finding{ID: "f1", Title: "defect", Evidence: "path:1 behavior", Category: "correctness", Verdict: "valid", Matches: []string{"g1"}}
	if err := validateJudgment(c, Judgment{Findings: []Finding{base}}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Finding){func(f *Finding) { f.Matches = []string{"invented"} }, func(f *Finding) { f.Verdict = "unresolved" }, func(f *Finding) { f.Category = "taste" }, func(f *Finding) { f.DuplicateOf = "missing" }} {
		f := base
		change(&f)
		if validateJudgment(c, Judgment{Findings: []Finding{f}}) == nil {
			t.Fatal("invalid judgment accepted")
		}
	}
	f := base
	f.ID = "f2"
	if validateJudgment(c, Judgment{Findings: []Finding{base, f}}) == nil {
		t.Fatal("double credit accepted")
	}
}
func TestReportShowsFailuresAndNoPersonalRecall(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "runs", "test")
	m := Manifest{Cases: []Case{{ID: "m1", Suite: "martian", URL: "https://github.com/a/b/pull/1", Archived: map[string]json.RawMessage{"other": json.RawMessage("[]")}}, {ID: "p1", Suite: "personal", URL: "https://github.com/u/r/pull/1"}, {ID: "dev", Suite: "personal", Cohort: "development"}}}
	mustWrite := func(path string, v any) {
		t.Helper()
		if err := writeJSON(path, v); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(dir, "manifest.json"), m)
	mustWrite(recordPath(run, "m1", "diffvouch"), Record{Status: "error", Error: "timeout"})
	mustWrite(recordPath(run, "p1", "diffvouch"), Record{Status: "complete", Judgment: &Judgment{Findings: []Finding{{Verdict: "valid", Category: "maintainability"}}}})
	if err := Report(dir, run); err != nil {
		t.Fatal(err)
	}
	var got ReportData
	if err := readJSON(filepath.Join(run, "report.json"), &got); err != nil {
		t.Fatal(err)
	}
	if got.Coverage["martian"]["diffvouch"].States["error"] != 1 || got.Coverage["martian"]["other"].States["not_run"] != 1 || got.DevelopmentCases != 1 {
		t.Fatalf("bad coverage: %+v", got)
	}
	for _, r := range []ToolReport{got.Suites["personal"]["diffvouch"], got.Repositories["u/r"]["diffvouch"]} {
		if r.F2 != nil || r.ReferenceRecall != nil || r.ReferencePrecision != nil {
			t.Fatal("invented personal reference score")
		}
	}
	if len(got.Comparisons) != 0 {
		t.Fatal("comparison included failed or missing records")
	}
}
func TestPairedResamplingUsesMatchedCases(t *testing.T) {
	a := []Counts{{TP: 1}, {TP: 2}}
	b := []Counts{{FN: 1}, {FN: 2}}
	d, l, h := pairedInterval(a, b)
	if d != 1 || l != 1 || h != 1 {
		t.Fatalf("wrong paired interval %v %v %v", d, l, h)
	}
}
func TestAnonymousReviewRemovesProviderMetadata(t *testing.T) {
	raw := json.RawMessage(`{"provider":"codex","model":"secret-model","findings":[{"title":"issue"}]}`)
	got := string(anonymousReview("diffvouch", raw))
	if strings.Contains(got, "secret-model") || !strings.Contains(got, "issue") {
		t.Fatal(got)
	}
}
func TestPatchComparisonIgnoresOnlyIndexMetadata(t *testing.T) {
	a := "diff --git a/x b/x\nindex ab..cd 100644\n-old\n+new"
	b := strings.Replace(a, "ab..cd", "abab..cdcd", 1)
	if normalizePatch(a) != normalizePatch(b) {
		t.Fatal("hash abbreviation mismatch")
	}
	if normalizePatch(a) == normalizePatch(strings.Replace(b, "+new", "+wrong", 1)) {
		t.Fatal("missed changed content")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBudgetStopsAndResetRetryKeepsIdempotency(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	session := chatgpt.Session{AccessToken: "test-access", RefreshToken: "test-refresh", AccountID: "test-account", ExpiresAt: time.Now().Add(time.Hour)}
	raw, _ := json.Marshal(session)
	ref, err := secret.Store("chatgpt-session", string(raw), "file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = config.UpdateGlobal(func(g *config.Global) error { g.ChatGPT = &ref; return nil }); err != nil {
		t.Fatal(err)
	}
	allowed := false
	applicable := 0
	attempts := 0
	requestID := ""
	creditExpiry := time.Now().Add(time.Hour)
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "chatgpt.com" || r.Header.Get("Authorization") != "Bearer test-access" {
			t.Fatal("unexpected account call")
		}
		value := any(nil)
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			value = map[string]any{"rate_limit": map[string]any{"allowed": allowed, "limit_reached": !allowed}, "rate_limit_reset_credits": map[string]any{"applicable_available_count": applicable}}
		case "/backend-api/wham/rate-limit-reset-credits":
			value = map[string]any{"credits": []map[string]any{{"id": "expiring", "status": "available", "reset_type": "codex_rate_limits", "expires_at": creditExpiry}}}
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			attempts++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["credit_id"] != "expiring" || body["redeem_request_id"] == "" {
				t.Fatal("bad reset")
			}
			if requestID != "" && requestID != body["redeem_request_id"] {
				t.Fatal("reset retry changed idempotency key")
			}
			requestID = body["redeem_request_id"]
			if attempts == 1 {
				return nil, errors.New("connection dropped after server processed reset")
			}
			allowed = true
			value = map[string]string{"code": "already_redeemed"}
		default:
			t.Fatal("unexpected endpoint")
		}
		raw, _ := json.Marshal(value)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	b := budget{dir: dir, client: client, resetBefore: time.Now().Add(2 * time.Hour)}
	if err = b.before(context.Background()); !errors.Is(err, chatgpt.ErrAllowance) || attempts != 0 {
		t.Fatal("exhaustion did not stop")
	}
	applicable = 1
	if err = b.before(context.Background()); !errors.Is(err, chatgpt.ErrAllowance) {
		t.Fatal("failed reset did not stop")
	}
	if err = b.before(context.Background()); err != nil || attempts != 2 {
		t.Fatalf("idempotent retry: %v", err)
	}
	allowed = false
	path := filepath.Join(dir, "resets", digest([]byte("expiring"))+".json")
	if err = os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = b.before(context.Background()); !errors.Is(err, chatgpt.ErrAllowance) || attempts != 2 {
		t.Fatal("corrupt reset state generated another redemption")
	}
}

func TestIncludedAllowanceDoesNotUseCreditBalance(t *testing.T) {
	yes := true
	u := chatgpt.Usage{}
	u.RateLimit.Allowed = &yes
	if !includedAllowed(u) {
		t.Fatal("available subscription blocked")
	}
	u.RateLimit.Primary = &chatgpt.Window{UsedPercent: 100}
	if includedAllowed(u) {
		t.Fatal("exhausted included window accepted")
	}
	u.RateLimit.Primary = nil
	u.RateLimit.Allowed = nil
	if includedAllowed(u) {
		t.Fatal("missing allowance state accepted")
	}
}

func TestGradeOnlySkipsMissingReviewsWithoutAuthentication(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	if err := writeJSON(filepath.Join(dir, "manifest.json"), Manifest{Version: 1, Cases: []Case{{ID: "unreviewed", Suite: "personal", URL: "https://github.com/u/r/pull/1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), RunOptions{Directory: dir, Phase: "grade", Tools: "none", Model: "reviewer", Judge: "judge"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "latest"))
	if err != nil {
		t.Fatal(err)
	}
	var data ReportData
	if err = readJSON(filepath.Join(string(raw), "report.json"), &data); err != nil {
		t.Fatal(err)
	}
	if data.States["not_run"] != 1 {
		t.Fatal("grade generated a missing review")
	}
}
func TestCompareRejectsDifferentFrozenCorpora(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := writeJSON(filepath.Join(a, "settings.json"), Settings{ManifestHash: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(b, "settings.json"), Settings{ManifestHash: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := Compare(dir, a, b); err == nil {
		t.Fatal("unmatched corpora accepted")
	}
}
