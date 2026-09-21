// Package eval runs frozen, resumable review evaluations. It does not enforce
// review-quality gates or change the reviewer's prompt to fit a benchmark.
package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/privatefile"
)

type Expected struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Description string `json:"description"`
}
type Case struct {
	ID                 string                     `json:"id"`
	Suite              string                     `json:"suite"`
	Cohort             string                     `json:"cohort"`
	URL                string                     `json:"url"`
	Repo               string                     `json:"repo"`
	Base               string                     `json:"base"`
	Head               string                     `json:"head"`
	Patch              string                     `json:"patch,omitempty"`
	Expected           []Expected                 `json:"expected"`
	ReferencesComplete bool                       `json:"references_complete"`
	Archived           map[string]json.RawMessage `json:"archived,omitempty"`
	ArchivedSnapshots  map[string]string          `json:"archived_snapshots,omitempty"`
}
type Manifest struct {
	Version  int               `json:"version"`
	Created  time.Time         `json:"created"`
	Sources  map[string]string `json:"sources"`
	Cases    []Case            `json:"cases"`
	Excluded []string          `json:"excluded"`
}
type Finding struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Verdict     string   `json:"verdict"`
	Evidence    string   `json:"evidence"`
	Matches     []string `json:"matches"`
	DuplicateOf string   `json:"duplicate_of"`
}
type Judgment struct {
	Findings []Finding `json:"findings"`
	Notes    string    `json:"notes"`
}
type Record struct {
	CaseID          string          `json:"case_id"`
	Tool            string          `json:"tool"`
	Status          string          `json:"status"`
	Error           string          `json:"error,omitempty"`
	Review          json.RawMessage `json:"review,omitempty"`
	Judgment        *Judgment       `json:"judgment,omitempty"`
	InitialJudgment *Judgment       `json:"initial_judgment,omitempty"`
	Judge           string          `json:"judge,omitempty"`
	Seconds         float64         `json:"seconds"`
	Completed       time.Time       `json:"completed"`
}
type Settings struct {
	Cohort             string `json:"cohort"`
	ReviewerPromptHash string `json:"reviewer_prompt_hash"`
	Model              string `json:"model"`
	Effort             string `json:"effort"`
	Judge              string `json:"judge"`
	Astra              string `json:"astra"`
	BinaryHash         string `json:"binary_hash"`
	ManifestHash       string `json:"manifest_hash"`
	JudgePromptHash    string `json:"judge_prompt_hash"`
	Repeat             int    `json:"repeat"`
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.Write(path, append(raw, '\n'))
}
func readJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
func recordPath(dir, caseID, tool string) string {
	return filepath.Join(dir, "records", digest([]byte(caseID+"\x00"+tool))+".json")
}
