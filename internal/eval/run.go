package eval

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/chatgpt"
	"github.com/divyangchauhan/DiffVouch/internal/privatefile"
	"github.com/divyangchauhan/DiffVouch/internal/review"
)

type RunOptions struct {
	Directory, Model, Effort, Judge, Astra, Suite, Phase, Tools, Cohort string
	Limit, Repeat                                                       int
	ResetBefore                                                         time.Time
	RetryErrors                                                         bool
	Output                                                              io.Writer
}

func Run(ctx context.Context, o RunOptions) error {
	var manifest Manifest
	manifestPath := filepath.Join(o.Directory, "manifest.json")
	if err := readJSON(manifestPath, &manifest); err != nil {
		return err
	}
	manifestBytes, _ := os.ReadFile(manifestPath)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return err
	}
	settings := Settings{Cohort: o.Cohort, ReviewerPromptHash: review.PromptHash(), Model: o.Model, Effort: o.Effort, Judge: o.Judge, Astra: o.Astra, ManifestHash: digest(manifestBytes), BinaryHash: digest(binary), JudgePromptHash: digest([]byte(judgePrompt)), Repeat: o.Repeat}
	settingsBytes, _ := json.Marshal(settings)
	dir := filepath.Join(o.Directory, "runs", digest(settingsBytes)[:16])
	if o.Output == nil {
		o.Output = io.Discard
	}
	runErr := privatefile.WithLock(filepath.Join(o.Directory, "runner"), func() (runErr error) {
		if err := writeJSON(filepath.Join(dir, "settings.json"), settings); err != nil {
			return err
		}
		if err := privatefile.Write(filepath.Join(o.Directory, "latest"), []byte(dir)); err != nil {
			return err
		}
		defer func() {
			if reportErr := Report(o.Directory, dir); runErr == nil {
				runErr = reportErr
			}
		}()
		guard := budget{dir: o.Directory, resetBefore: o.ResetBefore}
		count := 0
		for _, c := range manifest.Cases {
			if (c.Cohort == "development") != (o.Cohort == "development") || (o.Suite != "" && c.Suite != o.Suite) {
				continue
			}
			if o.Repeat > 0 && repeatSelection(c.ID) == false {
				continue
			}
			if o.Limit > 0 && count >= o.Limit {
				break
			}
			count++
			archivedTools := []string{}
			if o.Phase != "review" {
				for tool := range c.Archived {
					if o.Tools == "all" || strings.Contains(","+o.Tools+",", ","+tool+",") {
						archivedTools = append(archivedTools, tool)
					}
				}
			}
			sort.Strings(archivedTools)
			tools := append([]string{"diffvouch"}, archivedTools...)
			needsWork := false
			for _, tool := range tools {
				var old Record
				if err := readJSON(recordPath(dir, c.ID, tool), &old); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if o.Phase == "grade" && tool == "diffvouch" && len(old.Review) == 0 {
					continue
				}
				if old.Status != "complete" && !(o.Phase == "review" && len(old.Review) > 0) && (old.Status != "error" || o.RetryErrors) {
					needsWork = true
				}
			}
			if !needsWork {
				continue
			}
			if err := guard.before(ctx); err != nil {
				return err
			}
			fmt.Fprintf(o.Output, "Preparing %s\n", c.ID)
			root, prepErr := prepareRepository(ctx, o.Directory, c)
			for _, tool := range tools {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				path := recordPath(dir, c.ID, tool)
				r := Record{CaseID: c.ID, Tool: tool}
				if err := readJSON(path, &r); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if o.Phase == "grade" && tool == "diffvouch" && len(r.Review) == 0 {
					continue
				}
				if r.Status == "complete" || o.Phase == "review" && len(r.Review) > 0 || r.Status == "error" && !o.RetryErrors {
					continue
				}
				started := time.Now()
				r.Status = "running"
				r.Error = ""
				if err := writeJSON(path, r); err != nil {
					return err
				}
				fmt.Fprintf(o.Output, "Reviewing/grading %s [%s]\n", c.ID, tool)
				err := prepErr
				if err == nil && tool != "diffvouch" {
					archived, archiveErr := (fetcher{dir: o.Directory}).pull(ctx, c.ArchivedSnapshots[tool])
					if archiveErr != nil {
						err = archiveErr
					} else if archived.Base.SHA != c.Base || archived.Head.SHA != c.Head {
						err = fmt.Errorf("archived review snapshot differs from evaluated base/head")
					}
				}
				if err == nil && len(r.Review) == 0 {
					if tool == "diffvouch" {
						var value any
						result, _, reviewErr := review.Perform(review.Options{ProviderName: "codex", Transport: "subscription", Model: o.Model, Effort: o.Effort, Base: c.Base, CommittedOnly: true, Root: root, Diagnostics: io.Discard, BeforeCall: guard.before})
						err = reviewErr
						if err == nil && result == nil {
							err = fmt.Errorf("no reviewable text changes")
						}
						value = result
						if err == nil {
							r.Review, err = json.Marshal(value)
						}
					} else {
						r.Review = c.Archived[tool]
					}
					if err == nil {
						if writeErr := writeJSON(path, r); writeErr != nil {
							return writeErr
						}
					}
				}
				if err == nil {
					err = checkRepository(ctx, root, c.Head)
				}
				if err == nil && o.Phase != "review" {
					if r.Judgment == nil {
						r.Judgment, err = judge(ctx, root, c, anonymousReview(tool, r.Review), o.Judge, guard.before)
						r.Judge = o.Judge
					}
					if err == nil && o.Astra != "" && r.Judge != o.Astra && needsAdjudication(*r.Judgment) {
						r.InitialJudgment = r.Judgment
						if writeErr := writeJSON(path, r); writeErr != nil {
							return writeErr
						}
						var adjudicated *Judgment
						adjudicated, err = judge(ctx, root, c, anonymousReview(tool, r.Review), o.Astra, guard.before)
						if err == nil {
							r.Judgment = adjudicated
							r.Judge = o.Astra
						}
					}
				}
				if err == nil {
					err = checkRepository(ctx, root, c.Head)
				}
				r.Seconds += time.Since(started).Seconds()
				r.Completed = time.Now().UTC()
				if err != nil {
					r.Status = "error"
					r.Error = err.Error()
				} else {
					r.Status = "complete"
					if o.Phase == "review" {
						r.Status = "reviewed"
					}
				}
				if errors.Is(err, chatgpt.ErrAllowance) || errors.Is(err, context.Canceled) {
					r.Status = "paused"
				}
				if writeErr := writeJSON(path, r); writeErr != nil {
					return writeErr
				}
				if r.Status == "paused" {
					return err
				}
				if err != nil {
					fmt.Fprintf(o.Output, "Recorded failure: %s\n", err)
				}
			}
			if err := Report(o.Directory, dir); err != nil {
				return err
			}
			// Checkouts are disposable and can be very large. Retain a paused or
			// modified checkout for diagnosis; reconstruct clean cases from pins.
			if root != "" && checkRepository(ctx, root, c.Head) == nil {
				if err := os.RemoveAll(root); err != nil {
					return err
				}
			}
		}
		return Report(o.Directory, dir)
	})
	// A request can exhaust the window after its preflight check. Checkpoint
	// first, then redeem only when the account confirms an eligible exhaustion.
	if errors.Is(runErr, chatgpt.ErrAllowance) && ctx.Err() == nil && !o.ResetBefore.IsZero() {
		u, err := chatgpt.ReadUsage(ctx, nil)
		if err == nil && !includedAllowed(u) && u.RateLimit.Allowed != nil && u.ResetCredits.Applicable > 0 {
			if err := (budget{dir: o.Directory, resetBefore: o.ResetBefore}).before(ctx); err == nil {
				fmt.Fprintln(o.Output, "Applied eligible earned reset; resuming checkpoints.")
				return Run(ctx, o)
			}
		}
	}
	return runErr
}

func needsAdjudication(j Judgment) bool {
	for _, f := range j.Findings {
		if f.DuplicateOf == "" && (f.Verdict == "unresolved" || f.Verdict == "valid" && len(f.Matches) == 0) {
			return true
		}
	}
	return false
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1")
	raw, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %.1500s", args[0], err, raw)
	}
	return strings.TrimSpace(string(raw)), nil
}

func prepareRepository(ctx context.Context, dir string, c Case) (string, error) {
	if !validRepo(c.Repo) || !validSHA(c.Base) || !validSHA(c.Head) {
		return "", fmt.Errorf("case has invalid repository or revision pins")
	}
	root := filepath.Join(dir, "checkouts", digest([]byte(c.ID))[:24])
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(err) {
		if _, err = git(ctx, root, "init", "-q"); err != nil {
			return "", err
		}
		if _, err = git(ctx, root, "remote", "add", "origin", "https://github.com/"+c.Repo+".git"); err != nil {
			return "", err
		}
	}
	for _, sha := range []string{c.Base, c.Head} {
		if _, err := git(ctx, root, "cat-file", "-e", sha+"^{commit}"); err != nil {
			if _, err = git(ctx, root, "fetch", "--no-tags", "--depth=50", "origin", sha); err != nil {
				return "", err
			}
		}
	}
	if _, err := git(ctx, root, "checkout", "--detach", c.Head); err != nil {
		return "", err
	}
	if err := checkRepository(ctx, root, c.Head); err != nil {
		return "", err
	}
	if _, err := git(ctx, root, "merge-base", c.Base, c.Head); err != nil {
		if _, err = git(ctx, root, "fetch", "--no-tags", "--deepen=500", "origin", c.Base, c.Head); err != nil {
			return "", err
		}
		if _, err = git(ctx, root, "merge-base", c.Base, c.Head); err != nil {
			return "", fmt.Errorf("cannot reconstruct merge base: %w", err)
		}
	}
	if c.Patch != "" {
		actual, err := git(ctx, root, "diff", "--no-ext-diff", c.Base+"..."+c.Head)
		if err != nil {
			return "", err
		}
		if normalizePatch(actual) != normalizePatch(c.Patch) {
			return "", fmt.Errorf("checkout diff differs from dataset patch")
		}
	}
	return root, nil
}
func checkRepository(ctx context.Context, root, head string) error {
	actual, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if actual != head {
		return fmt.Errorf("review changed checkout revision")
	}
	status, err := git(ctx, root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("review or judge modified tracked files")
	}
	return nil
}
func validSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validRepo(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
				return false
			}
		}
	}
	return true
}

// Omit provider metadata, ratings and timestamps from DiffVouch's submitted
// findings. Archived inline comment text is preserved verbatim.
func anonymousReview(tool string, raw json.RawMessage) json.RawMessage {
	if tool != "diffvouch" {
		return raw
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	if findings, ok := value["findings"]; ok {
		return findings
	}
	return raw
}
func repeatSelection(id string) bool {
	// A byte rather than an ASCII hex character gives approximately 20 percent.
	raw, _ := hex.DecodeString(digest([]byte(id))[:2])
	return int(raw[0])%5 == 0
}
func normalizePatch(p string) string {
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSpace(p), "\n") {
		if strings.HasPrefix(line, "index ") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
