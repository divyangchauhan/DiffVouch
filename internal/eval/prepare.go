package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/privatefile"
)

const martianRevision = "e616e849755441da38f18bf3adba2c9583b03803"
const martianBase = "https://raw.githubusercontent.com/withmartian/code-review-benchmark/" + martianRevision + "/offline/"

type fetcher struct{ dir string }

func (f fetcher) get(ctx context.Context, address string) ([]byte, error) {
	path := filepath.Join(f.dir, "downloads", digest([]byte(address)))
	if raw, err := os.ReadFile(path); err == nil {
		return raw, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "diffvouch-evals")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var raw []byte
	if res.StatusCode != 200 && strings.HasPrefix(address, "https://api.github.com/") {
		// gh supplies already configured credentials without exposing them. The
		// request still targets only the public repository URL in the manifest.
		cmd := exec.CommandContext(ctx, "gh", "api", strings.TrimPrefix(address, "https://api.github.com/"))
		raw, err = cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("public GitHub request returned HTTP %d: %s", res.StatusCode, address)
		}
	} else {
		if res.StatusCode != 200 {
			return nil, fmt.Errorf("download returned HTTP %d: %s", res.StatusCode, address)
		}
		raw, err = io.ReadAll(io.LimitReader(res.Body, (100<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > 100<<20 {
			return nil, fmt.Errorf("dataset download exceeds 100 MiB")
		}
	}
	if err = privatefile.Write(path, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

type pull struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Base   struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func parsePullURL(address string) (string, int, error) {
	u, err := url.Parse(address)
	if err != nil || u.Host != "github.com" {
		return "", 0, fmt.Errorf("invalid public PR URL")
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) != 4 || p[2] != "pull" || !validRepo(p[0]+"/"+p[1]) {
		return "", 0, fmt.Errorf("invalid public PR URL")
	}
	n, err := strconv.Atoi(p[3])
	if err != nil || n < 1 {
		return "", 0, fmt.Errorf("invalid PR number")
	}
	return p[0] + "/" + p[1], n, nil
}
func (f fetcher) pull(ctx context.Context, address string) (pull, error) {
	repo, n, err := parsePullURL(address)
	if err != nil {
		return pull{}, err
	}
	raw, err := f.get(ctx, fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, n))
	if err != nil {
		return pull{}, err
	}
	var p pull
	err = json.Unmarshal(raw, &p)
	return p, err
}

func Prepare(ctx context.Context, dir, owner string, out io.Writer) error {
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
		return fmt.Errorf("manifest already exists; use a new directory to create a new frozen corpus")
	}
	f := fetcher{dir: dir}
	m := Manifest{Version: 1, Created: time.Now().UTC(), Sources: map[string]string{}, Cases: []Case{}, Excluded: []string{}}
	fmt.Fprintln(out, "Importing pinned Martian data and archive snapshot")
	raw, err := f.get(ctx, martianBase+"results/benchmark_data.json")
	if err != nil {
		return err
	}
	m.Sources["martian"] = martianRevision + " sha256:" + digest(raw) + " MIT"
	for _, name := range []string{"benchmark_data.json", "openai_gpt-5.2/candidates.json", "openai_gpt-5.2/dedup_groups.json", "openai_gpt-5.2/evaluations.json"} {
		snapshot, err := f.get(ctx, martianBase+"results/"+name)
		if err != nil {
			return err
		}
		if err = privatefile.Write(filepath.Join(dir, "sources", "martian", name), snapshot); err != nil {
			return err
		}
		m.Sources["martian/"+name] = martianBase + "results/" + name + " sha256:" + digest(snapshot)
	}

	var entries map[string]struct {
		Golden  []struct{ Comment, Category string } `json:"golden_comments"`
		Reviews []struct {
			Tool     string          `json:"tool"`
			URL      string          `json:"pr_url"`
			Comments json.RawMessage `json:"review_comments"`
		} `json:"reviews"`
	}
	if err = json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		v := entries[key]
		c := Case{ID: "martian/" + digest([]byte(key))[:16], Suite: "martian", Cohort: "evaluation", URL: key, Expected: []Expected{}, ReferencesComplete: true, Archived: map[string]json.RawMessage{}, ArchivedSnapshots: map[string]string{}}
		for i, g := range v.Golden {
			c.Expected = append(c.Expected, Expected{ID: fmt.Sprintf("g%d", i+1), Category: g.Category, Description: g.Comment})
		}
		// Replay a benchmark mirror, not the later corrected original PR. Keep
		// archived comments verbatim, and freeze the selected mirror's revisions.
		snapshot := ""
		for _, r := range v.Reviews {
			if len(r.Comments) > 0 && string(r.Comments) != "null" {
				c.Archived[r.Tool] = r.Comments
				c.ArchivedSnapshots[r.Tool] = r.URL
			}
			if r.Tool == "coderabbit" {
				snapshot = r.URL
			}
		}
		if snapshot == "" {
			m.Excluded = append(m.Excluded, c.ID+": no benchmark mirror snapshot")
			continue
		}
		p, err := f.pull(ctx, snapshot)
		if err != nil {
			m.Excluded = append(m.Excluded, c.ID+": "+err.Error())
			continue
		}
		c.Repo = p.Base.Repo.FullName
		c.Base = p.Base.SHA
		c.Head = p.Head.SHA
		if !validSHA(c.Base) || !validSHA(c.Head) {
			m.Excluded = append(m.Excluded, c.ID+": missing snapshot revisions")
			continue
		}
		m.Sources[c.ID+"/snapshot"] = snapshot
		m.Cases = append(m.Cases, c)
	}
	// Resolve the dataset commit before fetching any data so a mutable main
	// branch cannot combine files from different snapshots.
	fmt.Fprintln(out, "Importing all SWE-PRBench cases and published split")
	meta, err := f.get(ctx, "https://huggingface.co/api/datasets/foundry-ai/swe-prbench")
	if err != nil {
		return err
	}
	var hf struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(meta, &hf) != nil || !validSHA(hf.SHA) {
		return fmt.Errorf("cannot pin SWE-PRBench revision")
	}
	hfbase := "https://huggingface.co/datasets/foundry-ai/swe-prbench/resolve/" + hf.SHA + "/dataset/"
	split, err := f.get(ctx, hfbase+"evals/eval_100.json")
	if err != nil {
		return err
	}
	var splitRows []struct {
		ID string `json:"task_id"`
	}
	if err = json.Unmarshal(split, &splitRows); err != nil {
		return err
	}
	if len(splitRows) != 100 {
		return fmt.Errorf("published SWE split no longer has 100 cases")
	}
	published := map[string]bool{}
	for _, r := range splitRows {
		published[r.ID] = true
	}
	data, err := f.get(ctx, hfbase+"prs.jsonl")
	if err != nil {
		return err
	}
	m.Sources["swe-prbench"] = hf.SHA + " sha256:" + digest(data) + " CC-BY-4.0; attribution: FoundryHQ-AI SWE-PRBench"
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 64<<10), 20<<20)
	for scanner.Scan() {
		var row struct {
			ID    string `json:"task_id"`
			Repo  string `json:"repo"`
			URL   string `json:"pr_url"`
			Base  string `json:"base_commit"`
			Head  string `json:"head_commit"`
			Patch string `json:"diff_patch"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return err
		}
		c := Case{ID: "swe/" + row.ID, Suite: "swe-prbench", Cohort: "evaluation", URL: row.URL, Repo: row.Repo, Base: row.Base, Head: row.Head, Patch: row.Patch, Expected: []Expected{}}
		if published[row.ID] {
			c.Cohort = "published-100"
		}
		annotation, err := f.get(ctx, hfbase+"annotations/"+url.PathEscape(row.ID)+"_human.json")
		if err != nil {
			m.Excluded = append(m.Excluded, c.ID+": annotation unavailable: "+err.Error())
			continue
		}
		var labels struct {
			IDs      []string `json:"substantive_comment_ids"`
			Comments []struct {
				ID       string `json:"comment_id"`
				Body     string `json:"body"`
				File     string `json:"file"`
				Reviewer string `json:"reviewer"`
				Reply    bool   `json:"is_reply"`
			} `json:"comments"`
		}
		if err = json.Unmarshal(annotation, &labels); err != nil {
			return err
		}
		included := map[string]bool{}
		for _, id := range labels.IDs {
			included[id] = true
		}
		for _, l := range labels.Comments {
			if included[l.ID] && !l.Reply && !isBot(l.Reviewer) {
				c.Expected = append(c.Expected, Expected{ID: l.ID, Category: "human_reference", Description: l.File + ": " + l.Body})
			}
		}
		m.Cases = append(m.Cases, c)
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	fmt.Fprintf(out, "Importing public PRs owned by %s\n", owner)
	if owner == "" || strings.ContainsAny(owner, "/\\? ") {
		return fmt.Errorf("invalid public GitHub owner")
	}
	for page := 1; ; page++ {
		raw, err := f.get(ctx, fmt.Sprintf("https://api.github.com/users/%s/repos?type=owner&per_page=100&page=%d", url.PathEscape(owner), page))
		if err != nil {
			return err
		}
		var repos []struct {
			Name    string `json:"full_name"`
			Private bool   `json:"private"`
		}
		if err = json.Unmarshal(raw, &repos); err != nil {
			return err
		}
		if len(repos) == 0 {
			break
		}
		for _, repo := range repos {
			if repo.Private {
				continue
			}
			for p := 1; ; p++ {
				raw, err := f.get(ctx, fmt.Sprintf("https://api.github.com/repos/%s/pulls?state=all&per_page=100&page=%d", repo.Name, p))
				if err != nil {
					return err
				}
				var pulls []pull
				if err = json.Unmarshal(raw, &pulls); err != nil {
					return err
				}
				if len(pulls) == 0 {
					break
				}
				for _, pr := range pulls {
					c := Case{ID: fmt.Sprintf("personal/%s/%d", repo.Name, pr.Number), Suite: "personal", Cohort: "evaluation", URL: pr.URL, Repo: repo.Name, Base: pr.Base.SHA, Head: pr.Head.SHA, Expected: []Expected{}}
					if !validSHA(c.Base) || !validSHA(c.Head) {
						m.Excluded = append(m.Excluded, c.ID+": unavailable revision pins")
						continue
					}
					m.Cases = append(m.Cases, c)
				}
				if len(pulls) < 100 {
					break
				}
			}
		}
		if len(repos) < 100 {
			break
		}
	}
	// Hash ordering fixes the development assignment before any model output.
	sort.Slice(m.Cases, func(i, j int) bool { return digest([]byte(m.Cases[i].ID)) < digest([]byte(m.Cases[j].ID)) })
	dev := 0
	seen := map[string]bool{}
	for i := range m.Cases {
		c := &m.Cases[i]
		if seen[c.ID] {
			return fmt.Errorf("duplicate case %s", c.ID)
		}
		seen[c.ID] = true
		if c.Suite == "personal" && dev < 30 {
			c.Cohort = "development"
			dev++
		}
	}
	m.Sources["personal"] = "public GitHub PR inventory for " + owner + "; frozen at " + m.Created.Format(time.RFC3339)
	if err = writeJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		return err
	}
	fmt.Fprintf(out, "Frozen %d cases (%d development), %d preparation exclusions.\n", len(m.Cases), dev, len(m.Excluded))
	return nil
}

func isBot(name string) bool {
	lower := strings.ToLower(name)
	for _, part := range []string{"[bot]", "-bot", "gemini-code-assist", "coderabbit", "copilot", "greptile", "claude"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}
