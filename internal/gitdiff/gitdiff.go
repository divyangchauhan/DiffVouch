package gitdiff

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/divyangchauhan/DiffVouch/internal/dv"
)

type Collected struct {
	Repository    string
	Patch         string
	Mode          string
	BaseRef       string
	BaseSHA       string
	MergeBase     *string
	HeadSHA       string
	ReviewedFiles []string
	ExcludedFiles []string
	BinaryFiles   []string
}

type Options struct {
	Root          string
	Base          string
	CommittedOnly bool
	StagedOnly    bool
	Excludes      []string
	MaxDiffBytes  int
}

func RepositoryRoot(path string) (string, error) {
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return "", dv.Wrap(dv.ExitGit, "resolve working directory", err)
		}
	}
	output, err := Run(path, 64*1024, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func Run(repo string, limit int, args ...string) (string, error) {
	return run(repo, limit, false, args...)
}

func run(repo string, limit int, allowDiff bool, args ...string) (string, error) {
	commandArgs := append([]string{"-c", "core.quotepath=false", "-c", "color.ui=false"}, args...)
	command := exec.Command("git", commandArgs...)
	command.Dir = repo
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", dv.Wrap(dv.ExitGit, "capture Git output", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", dv.New(dv.ExitGit, "Git is not installed")
		}
		return "", dv.Wrap(dv.ExitGit, "start Git", err)
	}
	if limit <= 0 {
		_ = command.Process.Kill()
		_ = command.Wait()
		return "", dv.New(dv.ExitCoverage, "Git patch exceeded the configured safety limit")
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	if len(data) > limit {
		_ = command.Process.Kill()
		_ = command.Wait()
		return "", dv.New(dv.ExitCoverage, fmt.Sprintf("Git output exceeded the configured %d-byte safety limit", limit))
	}
	waitErr := command.Wait()
	if readErr != nil {
		return "", dv.Wrap(dv.ExitGit, "read Git output", readErr)
	}
	if waitErr != nil {
		var exit *exec.ExitError
		if !(allowDiff && errors.As(waitErr, &exit) && exit.ExitCode() == 1) {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = waitErr.Error()
			}
			return "", dv.New(dv.ExitGit, detail)
		}
	}
	return string(data), nil
}

func Collect(options Options) (Collected, error) {
	if options.MaxDiffBytes < 10_000 {
		return Collected{}, dv.New(dv.ExitArguments, "max diff bytes must be at least 10000")
	}
	if options.StagedOnly && options.Base != "" {
		return Collected{}, dv.New(dv.ExitArguments, "--staged-only cannot be combined with --base")
	}
	if options.StagedOnly && options.CommittedOnly {
		return Collected{}, dv.New(dv.ExitArguments, "--staged-only cannot be combined with --committed-only")
	}
	repo, err := RepositoryRoot(options.Root)
	if err != nil {
		return Collected{}, err
	}
	head, err := Run(repo, 4096, "rev-parse", "--verify", "HEAD^{commit}")
	unborn := err != nil
	if unborn {
		if options.Base != "" || options.CommittedOnly {
			return Collected{}, dv.New(dv.ExitGit, "base and committed-only reviews require a HEAD commit")
		}
		head, err = emptyTree(repo)
		if err != nil {
			return Collected{}, err
		}
	}
	head = strings.TrimSpace(head)
	result := Collected{Repository: repo, HeadSHA: head}
	common := []string{"diff", "--find-renames", "--no-ext-diff", "--no-textconv", "--no-color"}
	var patch string
	switch {
	case options.StagedOnly:
		result.Mode, result.BaseRef, result.BaseSHA = "staged", "HEAD", head
		stagedBase := "HEAD"
		if unborn {
			stagedBase, result.BaseRef = head, "empty-tree"
		}
		patch, err = Run(repo, options.MaxDiffBytes, append(common, "--cached", stagedBase, "--")...)
	case options.Base != "":
		result.BaseRef = options.Base
		baseSHA, baseErr := Run(repo, 4096, "rev-parse", "--verify", options.Base+"^{commit}")
		if baseErr != nil {
			return Collected{}, dv.New(dv.ExitGit, fmt.Sprintf("base %q is not available locally", options.Base))
		}
		result.BaseSHA = strings.TrimSpace(baseSHA)
		merge, mergeErr := Run(repo, 4096, "merge-base", "HEAD", options.Base)
		if mergeErr != nil || strings.TrimSpace(merge) == "" {
			return Collected{}, dv.New(dv.ExitGit, fmt.Sprintf("HEAD and %q have no merge base", options.Base))
		}
		merge = strings.TrimSpace(merge)
		result.MergeBase = &merge
		args := append(common, merge)
		if options.CommittedOnly {
			result.Mode = "committed"
			args = append(args, "HEAD")
		} else {
			result.Mode = "base"
		}
		args = append(args, "--")
		patch, err = Run(repo, options.MaxDiffBytes, args...)
	case options.CommittedOnly:
		return Collected{}, dv.New(dv.ExitArguments, "--committed-only requires --base")
	default:
		result.Mode, result.BaseRef, result.BaseSHA = "working-tree", "HEAD", head
		workingBase := "HEAD"
		if unborn {
			workingBase, result.BaseRef = head, "empty-tree"
		}
		patch, err = Run(repo, options.MaxDiffBytes, append(common, workingBase, "--")...)
	}
	if err != nil {
		return Collected{}, err
	}
	if !options.StagedOnly && !options.CommittedOnly {
		remaining := options.MaxDiffBytes - len([]byte(patch))
		untracked, binary, untrackedErr := collectUntracked(repo, remaining)
		if untrackedErr != nil {
			return Collected{}, untrackedErr
		}
		patch += untracked
		result.BinaryFiles = append(result.BinaryFiles, binary...)
	}
	sections := SplitFilePatches(patch)
	if patch != "" && len(sections) == 0 {
		return Collected{}, dv.New(dv.ExitCoverage, "Git returned a non-empty patch that could not be parsed safely")
	}
	var kept strings.Builder
	for _, section := range sections {
		path := PatchPath(section)
		if path == "" {
			return Collected{}, dv.New(dv.ExitCoverage, "could not resolve a changed path from the Git patch")
		}
		if !utf8.ValidString(section) || isBinarySection(section) {
			result.BinaryFiles = append(result.BinaryFiles, path)
			continue
		}
		if matchesAny(path, options.Excludes) {
			result.ExcludedFiles = append(result.ExcludedFiles, path)
			continue
		}
		kept.WriteString(section)
		result.ReviewedFiles = append(result.ReviewedFiles, path)
	}
	result.Patch = kept.String()
	if len([]byte(result.Patch)) > options.MaxDiffBytes {
		return Collected{}, dv.New(dv.ExitCoverage, fmt.Sprintf("review patch exceeds the configured %d-byte safety limit", options.MaxDiffBytes))
	}
	result.ReviewedFiles = uniqueSorted(result.ReviewedFiles)
	result.ExcludedFiles = uniqueSorted(result.ExcludedFiles)
	result.BinaryFiles = uniqueSorted(result.BinaryFiles)
	return result, nil
}

func emptyTree(repo string) (string, error) {
	command := exec.Command("git", "mktree")
	command.Dir = repo
	command.Stdin = strings.NewReader("")
	raw, err := command.CombinedOutput()
	if err != nil {
		return "", dv.Wrap(dv.ExitGit, "create empty Git tree", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func collectUntracked(repo string, limit int) (string, []string, error) {
	pathsRaw, err := Run(repo, limit, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", nil, err
	}
	var patch strings.Builder
	var binary []string
	for _, relative := range strings.Split(pathsRaw, "\x00") {
		if relative == "" {
			continue
		}
		absolute := filepath.Join(repo, filepath.FromSlash(relative))
		info, statErr := os.Lstat(absolute)
		if statErr != nil {
			return "", nil, dv.Wrap(dv.ExitGit, "inspect untracked file", statErr)
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			binary = append(binary, relative)
			continue
		}
		if info.Mode().IsRegular() {
			file, openErr := os.Open(absolute)
			if openErr != nil {
				return "", nil, dv.Wrap(dv.ExitGit, "inspect untracked file", openErr)
			}
			probe := make([]byte, 8192)
			count, _ := file.Read(probe)
			_ = file.Close()
			if bytes.IndexByte(probe[:count], 0) >= 0 {
				binary = append(binary, relative)
				continue
			}
		}
		remaining := limit - patch.Len()
		section, diffErr := run(repo, remaining, true, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--", "/dev/null", relative)
		if diffErr != nil {
			return "", nil, diffErr
		}
		patch.WriteString(section)
	}
	return patch.String(), binary, nil
}

func SplitFilePatches(patch string) []string {
	indices := regexp.MustCompile(`(?m)^diff --git `).FindAllStringIndex(patch, -1)
	if len(indices) == 0 {
		return nil
	}
	sections := make([]string, 0, len(indices))
	for index, location := range indices {
		end := len(patch)
		if index+1 < len(indices) {
			end = indices[index+1][0]
		}
		sections = append(sections, patch[location[0]:end])
	}
	return sections
}

var diffHeader = regexp.MustCompile(`^diff --git ("(?:\\.|[^"])*"|\S+) ("(?:\\.|[^"])*"|\S+)`)
var hunkHeader = regexp.MustCompile(`(?m)^@@ `)

func PatchPath(section string) string {
	var fallback string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "--- ") {
			fallback = decodePath(strings.TrimPrefix(line, "--- "))
		}
		if strings.HasPrefix(line, "+++ ") {
			if path := decodePath(strings.TrimPrefix(line, "+++ ")); path != "" {
				return path
			}
		}
	}
	if fallback != "" {
		return fallback
	}
	match := diffHeader.FindStringSubmatch(strings.SplitN(section, "\n", 2)[0])
	if len(match) == 3 {
		if path := decodePath(match[2]); path != "" {
			return path
		}
		return decodePath(match[1])
	}
	return ""
}

func decodePath(value string) string {
	if value == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(value, "\"") {
		if decoded, err := strconv.Unquote(value); err == nil {
			value = decoded
		}
	}
	if strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/") {
		return value[2:]
	}
	return value
}

func isBinarySection(section string) bool {
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			return false
		}
		if line == "GIT binary patch" || strings.HasPrefix(line, "Binary files ") {
			return true
		}
	}
	return false
}

func matchesAny(path string, patterns []string) bool {
	for _, pattern := range patterns {
		quoted := regexp.QuoteMeta(filepath.ToSlash(pattern))
		quoted = strings.ReplaceAll(quoted, `\*\*`, `.*`)
		quoted = strings.ReplaceAll(quoted, `\*`, `[^/]*`)
		quoted = strings.ReplaceAll(quoted, `\?`, `[^/]`)
		if matched, _ := regexp.MatchString("^"+quoted+"$", filepath.ToSlash(path)); matched {
			return true
		}
	}
	return false
}

func DisplayPath(path string) string {
	unsafe := !utf8.ValidString(path)
	for _, character := range path {
		if character == '`' || unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			unsafe = true
			break
		}
	}
	if !unsafe {
		return path
	}
	return strings.ReplaceAll(strconv.QuoteToASCII(path), "`", `\u0060`)
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func ChunkPatch(patch string, limit int) ([]string, error) {
	if len([]byte(patch)) <= limit {
		return []string{patch}, nil
	}
	var chunks []string
	var current strings.Builder
	for _, section := range SplitFilePatches(patch) {
		if current.Len() > 0 && current.Len()+len(section) > limit {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if len(section) <= limit {
			current.WriteString(section)
			continue
		}
		hunkLocation := hunkHeader.FindStringIndex(section)
		if hunkLocation == nil {
			return nil, dv.New(dv.ExitCoverage, "a single changed file exceeds the chunk limit")
		}
		hunkAt := hunkLocation[0]
		header := section[:hunkAt]
		hunkStarts := hunkHeader.FindAllStringIndex(section[hunkAt:], -1)
		for index, location := range hunkStarts {
			end := len(section) - hunkAt
			if index+1 < len(hunkStarts) {
				end = hunkStarts[index+1][0]
			}
			piece := header + section[hunkAt+location[0]:hunkAt+end]
			if len(piece) > limit {
				return nil, dv.New(dv.ExitCoverage, "a single diff hunk exceeds the chunk limit")
			}
			if current.Len() > 0 && current.Len()+len(piece) > limit {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			current.WriteString(piece)
		}
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks, nil
}
