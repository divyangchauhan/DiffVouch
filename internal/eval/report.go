package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Counts struct {
	Cases           int `json:"cases"`
	TP              int `json:"reference_matches"`
	FP              int `json:"unmatched_claims"`
	FN              int `json:"unmatched_references"`
	Valid           int `json:"valid_findings"`
	Invalid         int `json:"invalid_findings"`
	Unresolved      int `json:"unresolved_findings"`
	Duplicates      int `json:"duplicates"`
	Maintainability int `json:"valid_maintainability_findings"`
}

func (a Counts) add(b Counts) Counts {
	return Counts{a.Cases + b.Cases, a.TP + b.TP, a.FP + b.FP, a.FN + b.FN, a.Valid + b.Valid, a.Invalid + b.Invalid, a.Unresolved + b.Unresolved, a.Duplicates + b.Duplicates, a.Maintainability + b.Maintainability}
}
func score(c Case, j Judgment) Counts {
	r := Counts{Cases: 1}
	refs := map[string]bool{}
	for _, e := range c.Expected {
		if e.Category != "style" && e.Category != "speculative" {
			refs[e.ID] = true
		}
	}
	matched := map[string]bool{}
	for _, f := range j.Findings {
		if f.DuplicateOf != "" {
			r.Duplicates++
			continue
		}
		switch f.Verdict {
		case "valid":
			r.Valid++
			if f.Category == "maintainability" {
				r.Maintainability++
			}
		case "invalid":
			r.Invalid++
		case "unresolved":
			r.Unresolved++
		}
		for _, id := range f.Matches {
			if refs[id] {
				matched[id] = true
			}
		}
		// A match to an excluded reference is neutral in Martian core scoring.
		if len(f.Matches) == 0 {
			r.FP++
		}
	}
	r.TP = len(matched)
	r.FN = len(refs) - len(matched)
	if c.Suite == "personal" {
		r.TP = 0
		r.FP = 0
		r.FN = 0
	}
	return r
}
func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
func f2(c Counts) float64 {
	den := 5*c.TP + 4*c.FN + c.FP
	if den == 0 {
		return 0
	}
	return float64(5*c.TP) / float64(den)
}

type ToolReport struct {
	Counts             Counts   `json:"counts"`
	ReferencePrecision *float64 `json:"reference_precision"`
	ReferenceRecall    *float64 `json:"reference_coverage"`
	FindingValidity    *float64 `json:"finding_validity"`
	F2                 *float64 `json:"reference_f2"`
}
type Comparison struct {
	Competitor string  `json:"competitor"`
	Cases      int     `json:"shared_completed_cases"`
	Delta      float64 `json:"diffvouch_minus_competitor_f2"`
	Low        float64 `json:"paired_pr_bootstrap_95_low"`
	High       float64 `json:"paired_pr_bootstrap_95_high"`
}
type Coverage struct {
	Expected int            `json:"expected"`
	States   map[string]int `json:"states"`
}
type ReportData struct {
	AdjudicationScoreChanges map[string]int                   `json:"adjudication_score_changes_by_tool"`
	Coverage                 map[string]map[string]*Coverage  `json:"coverage"`
	Adjudications            map[string]int                   `json:"adjudications_by_tool"`
	PublishedSources         map[string]string                `json:"published_sources"`
	Run                      string                           `json:"run"`
	ManifestCases            int                              `json:"manifest_cases"`
	DevelopmentCases         int                              `json:"development_cases"`
	PreparationExclusions    []string                         `json:"preparation_exclusions"`
	States                   map[string]int                   `json:"record_states"`
	Suites                   map[string]map[string]ToolReport `json:"suites"`
	Repositories             map[string]map[string]ToolReport `json:"repositories"`
	Comparisons              []Comparison                     `json:"martian_paired_comparisons"`
	Limitations              []string                         `json:"limitations"`
}

func Report(workdir, dir string) error {
	if dir == "" {
		raw, err := os.ReadFile(filepath.Join(workdir, "latest"))
		if err != nil {
			return err
		}
		dir = string(raw)
	}
	var manifest Manifest
	if err := readJSON(filepath.Join(workdir, "manifest.json"), &manifest); err != nil {
		return err
	}
	var settings Settings
	if err := readJSON(filepath.Join(dir, "settings.json"), &settings); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data := ReportData{Run: filepath.Base(dir), Coverage: map[string]map[string]*Coverage{}, Adjudications: map[string]int{}, AdjudicationScoreChanges: map[string]int{}, PublishedSources: manifest.Sources, ManifestCases: len(manifest.Cases), PreparationExclusions: manifest.Excluded, States: map[string]int{}, Suites: map[string]map[string]ToolReport{}, Repositories: map[string]map[string]ToolReport{}, Comparisons: []Comparison{}, Limitations: []string{
		"Competitor outputs are historical archives, not freshly run current products. Original published scores are preserved in the source snapshots, not replaced by these scores.",
		"Martian core excludes style/speculative references. Scores use a shared extraction/deduplication/judgment pipeline, not the original published judge; they are new rescored results.",
		"SWE-PRBench runs have repository access and are not comparable to its published frozen-context model scores. Human-reference coverage is not exhaustive bug recall.",
		"Personal PRs are unlabeled. Finding validity is model-judged; reference recall is unavailable. Unresolved findings and runtime failures are reported separately.",
		"Paired intervals resample PRs conditional on the frozen data and judgments. They do not capture judge error, shared repository dependence, or changing competitor versions. A larger personal corpus does not increase the competitor comparison sample.",
		"Missing and failed records are not clean reviews. Paired comparisons use only shared completed cases; inspect coverage before drawing conclusions.",
	}}
	counts := map[string]map[string]Counts{}
	repos := map[string]map[string]Counts{}
	paired := map[string]map[string]Counts{}
	unlabeledRepos := map[string]bool{}
	suiteTools := map[string]map[string]bool{}
	for _, c := range manifest.Cases {
		if suiteTools[c.Suite] == nil {
			suiteTools[c.Suite] = map[string]bool{}
		}
		for tool := range c.Archived {
			suiteTools[c.Suite][tool] = true
		}
	}

	for _, c := range manifest.Cases {
		if c.Cohort == "development" {
			data.DevelopmentCases++
		}
		if (c.Cohort == "development") != (settings.Cohort == "development") {
			continue
		}
		if settings.Repeat > 0 && !repeatSelection(c.ID) {
			continue
		}
		repoName, _, _ := parsePullURL(c.URL)
		if c.Suite == "personal" {
			unlabeledRepos[repoName] = true
		}
		tools := []string{"diffvouch"}
		for tool := range suiteTools[c.Suite] {
			tools = append(tools, tool)
		}
		for _, tool := range tools {
			var record Record
			readErr := readJSON(recordPath(dir, c.ID, tool), &record)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return readErr
			}
			state := record.Status
			if readErr != nil {
				state = "not_run"
				if tool != "diffvouch" {
					if _, ok := c.Archived[tool]; !ok {
						state = "archive_unavailable"
					}
				}
			}
			if data.Coverage[c.Suite] == nil {
				data.Coverage[c.Suite] = map[string]*Coverage{}
			}
			if data.Coverage[c.Suite][tool] == nil {
				data.Coverage[c.Suite][tool] = &Coverage{States: map[string]int{}}
			}
			coverage := data.Coverage[c.Suite][tool]
			coverage.Expected++
			coverage.States[state]++
			data.States[state]++
			if readErr != nil {
				continue
			}
			if record.InitialJudgment != nil && record.Judge != "" {
				data.Adjudications[tool]++
			}
			if record.Status != "complete" || record.Judgment == nil {
				continue
			}
			value := score(c, *record.Judgment)
			if record.InitialJudgment != nil && score(c, *record.InitialJudgment) != value {
				data.AdjudicationScoreChanges[tool]++
			}
			groups := []string{c.Suite}
			if c.Cohort == "published-100" {
				groups = append(groups, "swe-prbench-published-100")
			}
			for _, group := range groups {
				if counts[group] == nil {
					counts[group] = map[string]Counts{}
				}
				counts[group][tool] = counts[group][tool].add(value)
			}
			repo, _, _ := parsePullURL(c.URL)
			if repos[repo] == nil {
				repos[repo] = map[string]Counts{}
			}
			repos[repo][tool] = repos[repo][tool].add(value)
			if c.Suite == "martian" {
				if paired[tool] == nil {
					paired[tool] = map[string]Counts{}
				}
				paired[tool][c.ID] = value
			}
		}
	}
	convert := func(values map[string]map[string]Counts, target map[string]map[string]ToolReport) {
		for group, tools := range values {
			target[group] = map[string]ToolReport{}
			for tool, c := range tools {
				v := ToolReport{Counts: c, ReferencePrecision: ratio(c.TP, c.TP+c.FP), ReferenceRecall: ratio(c.TP, c.TP+c.FN), FindingValidity: ratio(c.Valid, c.Valid+c.Invalid), F2: ratio(5*c.TP, 5*c.TP+4*c.FN+c.FP)}
				if group == "personal" || unlabeledRepos[group] {
					v.ReferencePrecision = nil
					v.ReferenceRecall = nil
					v.F2 = nil
				}
				target[group][tool] = v
			}
		}
	}
	convert(counts, data.Suites)
	convert(repos, data.Repositories)
	for tool, other := range paired {
		if tool == "diffvouch" {
			continue
		}
		ids := []string{}
		for id := range other {
			if _, ok := paired["diffvouch"][id]; ok {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if len(ids) < 2 {
			continue
		}
		a, b := []Counts{}, []Counts{}
		for _, id := range ids {
			a = append(a, paired["diffvouch"][id])
			b = append(b, other[id])
		}
		delta, low, high := pairedInterval(a, b)
		data.Comparisons = append(data.Comparisons, Comparison{tool, len(ids), delta, low, high})
	}
	sort.Slice(data.Comparisons, func(i, j int) bool { return data.Comparisons[i].Competitor < data.Comparisons[j].Competitor })
	if err := writeJSON(filepath.Join(dir, "report.json"), data); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# DiffVouch evaluation report\n\nRun `%s`. Frozen cases: %d; development: %d; preparation exclusions: %d.\n\n", data.Run, data.ManifestCases, data.DevelopmentCases, len(data.PreparationExclusions))
	md.WriteString("| Suite | Tool | Expected PRs | Completed | Failed | Pending/paused |\n|---|---|---:|---:|---:|---:|\n")
	coverageGroups := []string{}
	for group := range data.Coverage {
		coverageGroups = append(coverageGroups, group)
	}
	sort.Strings(coverageGroups)
	for _, group := range coverageGroups {
		names := []string{}
		for name := range data.Coverage[group] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			v := data.Coverage[group][name]
			fmt.Fprintf(&md, "| %s | %s | %d | %d | %d | %d |\n", group, name, v.Expected, v.States["complete"], v.States["error"], v.Expected-v.States["complete"]-v.States["error"])
		}
	}
	md.WriteString("\n| Suite | Tool | Completed PRs | Reference matches | Unmatched claims | Missed references | Valid | Invalid | Unresolved |\n|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	groups := []string{}
	for group := range data.Suites {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		tools := []string{}
		for tool := range data.Suites[group] {
			tools = append(tools, tool)
		}
		sort.Strings(tools)
		for _, tool := range tools {
			c := data.Suites[group][tool].Counts
			fmt.Fprintf(&md, "| %s | %s | %d | %d | %d | %d | %d | %d | %d |\n", group, tool, c.Cases, c.TP, c.FP, c.FN, c.Valid, c.Invalid, c.Unresolved)
		}
	}
	md.WriteString("\n## Paired Martian comparisons\n\n| Historical competitor | Shared PRs | F2 difference | 95% paired PR interval |\n|---|---:|---:|---|\n")
	for _, c := range data.Comparisons {
		fmt.Fprintf(&md, "| %s | %d | %.3f | [%.3f, %.3f] |\n", c.Competitor, c.Cases, c.Delta, c.Low, c.High)
	}
	md.WriteString("\n## Interpretation\n\n")
	for _, line := range data.Limitations {
		fmt.Fprintf(&md, "- %s\n", line)
	}
	md.WriteString("\nDetailed counts, per-repository results, failure states and exclusions are in `report.json`. No regression gate is enforced.\n")
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(md.String()), 0o600)
}

func pairedInterval(a, b []Counts) (float64, float64, float64) {
	var totalA, totalB Counts
	for i := range a {
		totalA = totalA.add(a[i])
		totalB = totalB.add(b[i])
	}
	delta := f2(totalA) - f2(totalB)
	rng := rand.New(rand.NewSource(1))
	samples := make([]float64, 2000)
	for n := range samples {
		var x, y Counts
		for range a {
			i := rng.Intn(len(a))
			x = x.add(a[i])
			y = y.add(b[i])
		}
		samples[n] = f2(x) - f2(y)
	}
	sort.Float64s(samples)
	return delta, samples[49], samples[1949]
}

// Compare reports observations only. It never enforces a regression threshold.
func Compare(workdir, before, after string) error {
	var a, b Settings
	if err := readJSON(filepath.Join(before, "settings.json"), &a); err != nil {
		return err
	}
	if err := readJSON(filepath.Join(after, "settings.json"), &b); err != nil {
		return err
	}
	if a.Cohort != b.Cohort {
		return fmt.Errorf("comparison requires the same cohort")
	}
	if a.ManifestHash != b.ManifestHash {
		return fmt.Errorf("comparison requires the same frozen manifest")
	}
	raw, err := os.ReadFile(filepath.Join(workdir, "manifest.json"))
	if err != nil {
		return err
	}
	if digest(raw) != a.ManifestHash {
		return fmt.Errorf("manifest differs from run pins")
	}
	var m Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	type result struct {
		Cases          int      `json:"shared_cases"`
		Before         Counts   `json:"before"`
		After          Counts   `json:"after"`
		ReferenceDelta *float64 `json:"reference_f2_delta"`
		Low            *float64 `json:"paired_pr_95_low"`
		High           *float64 `json:"paired_pr_95_high"`
	}
	groups := map[string][][2]Counts{}
	for _, c := range m.Cases {
		if (c.Cohort == "development") != (a.Cohort == "development") {
			continue
		}
		var left, right Record
		if readJSON(recordPath(before, c.ID, "diffvouch"), &left) != nil || readJSON(recordPath(after, c.ID, "diffvouch"), &right) != nil {
			continue
		}
		if left.Status != "complete" || right.Status != "complete" || left.Judgment == nil || right.Judgment == nil {
			continue
		}
		groups[c.Suite] = append(groups[c.Suite], [2]Counts{score(c, *left.Judgment), score(c, *right.Judgment)})
	}
	results := map[string]result{}
	for suite, pairs := range groups {
		r := result{Cases: len(pairs)}
		x, y := []Counts{}, []Counts{}
		for _, p := range pairs {
			r.Before = r.Before.add(p[0])
			r.After = r.After.add(p[1])
			x = append(x, p[1])
			y = append(y, p[0])
		}
		if suite != "personal" && len(pairs) > 1 {
			d, l, h := pairedInterval(x, y)
			r.ReferenceDelta = &d
			r.Low = &l
			r.High = &h
		}
		results[suite] = r
	}
	return writeJSON(filepath.Join(after, "comparison.json"), map[string]any{"before": before, "after": after, "suites": results, "gate_enforced": false, "note": "Shared completed cases only; inspect each run's coverage. Model judges and repository dependence add uncertainty beyond these intervals."})
}
