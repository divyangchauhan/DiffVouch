package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/divyangchauhan/DiffVouch/internal/privatefile"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
	"gopkg.in/yaml.v3"
)

type Models struct {
	Codex  string `yaml:"codex"`
	Claude string `yaml:"claude"`
}

type Provider struct {
	DefaultTransport string `yaml:"default_transport"`
	Models           Models `yaml:"models"`
}

type Review struct {
	Rubric       map[string]int `yaml:"rubric"`
	Instructions []string       `yaml:"instructions"`
	Exclude      []string       `yaml:"exclude"`
	MaxDiffBytes int            `yaml:"max_diff_bytes"`
	ChunkBytes   int            `yaml:"chunk_bytes"`
}

type QualityGate struct {
	FailBelow      *float64 `yaml:"fail_below"`
	FailOnSeverity string   `yaml:"fail_on_severity"`
}

type Repository struct {
	Version     int         `yaml:"version"`
	Provider    Provider    `yaml:"provider"`
	Review      Review      `yaml:"review"`
	QualityGate QualityGate `yaml:"quality_gate"`
}

func Defaults() Repository {
	return Repository{
		Version:  1,
		Provider: Provider{DefaultTransport: "cli"},
		Review: Review{
			Rubric: map[string]int{
				"correctness": 35, "security": 20, "maintainability": 20,
				"testing": 15, "scope": 10,
			},
			MaxDiffBytes: 500_000,
			ChunkBytes:   180_000,
		},
	}
}

func LoadRepository(repo, explicit, trustedRef string) (Repository, error) {
	value := Defaults()
	var raw []byte
	var source string
	var err error
	if explicit != "" {
		absolute, absoluteErr := filepath.Abs(explicit)
		if absoluteErr != nil {
			return value, absoluteErr
		}
		relative, relativeErr := filepath.Rel(repo, absolute)
		insideRepository := relativeErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
		if insideRepository {
			relative = filepath.ToSlash(relative)
			source = trustedRef + ":" + relative
			command := exec.Command("git", "show", source)
			command.Dir = repo
			raw, err = command.Output()
			if err != nil {
				return value, fmt.Errorf("read trusted explicit config %s: %w", source, err)
			}
		} else {
			source = absolute
			raw, err = os.ReadFile(absolute)
			if err != nil {
				return value, fmt.Errorf("read external explicit config %s: %w", absolute, err)
			}
		}
	} else {
		source = trustedRef + ":.diffvouch.yml"
		check := exec.Command("git", "cat-file", "-e", source)
		check.Dir = repo
		if err := check.Run(); err != nil {
			return value, nil
		}
		command := exec.Command("git", "show", source)
		command.Dir = repo
		raw, err = command.Output()
		if err != nil {
			return value, fmt.Errorf("read trusted config %s: %w", source, err)
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("invalid DiffVouch config %s: %w", source, err)
	}
	if err := validate(value); err != nil {
		return value, fmt.Errorf("invalid DiffVouch config %s: %w", source, err)
	}
	return value, nil
}

func validate(value Repository) error {
	if value.Version != 1 {
		return fmt.Errorf("unsupported version %d", value.Version)
	}
	if value.Provider.DefaultTransport != "" && value.Provider.DefaultTransport != "cli" {
		return errors.New("repository config cannot select API transport")
	}
	if value.Review.MaxDiffBytes < 10_000 || value.Review.ChunkBytes < 10_000 {
		return errors.New("diff and chunk limits must be at least 10000 bytes")
	}
	total := 0
	for _, name := range []string{"correctness", "security", "maintainability", "testing", "scope"} {
		weight, ok := value.Review.Rubric[name]
		if !ok || weight < 0 {
			return fmt.Errorf("rubric requires a non-negative %s weight", name)
		}
		total += weight
	}
	if total != 100 {
		return fmt.Errorf("rubric weights total %d, expected 100", total)
	}
	return nil
}

type GitHubApp struct {
	AppID      string     `json:"app_id"`
	Slug       string     `json:"slug"`
	Host       string     `json:"host"`
	APIBaseURL string     `json:"api_base_url"`
	WebBaseURL string     `json:"web_base_url"`
	APIVersion string     `json:"api_version,omitempty"`
	PrivateKey secret.Ref `json:"private_key"`
}

type Global struct {
	Version    int                   `json:"version"`
	APIKeys    map[string]secret.Ref `json:"api_keys"`
	GitHubApps map[string]GitHubApp  `json:"github_apps"`
}

func globalPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "diffvouch", "config.json"), nil
}

func LoadGlobal() (Global, error) {
	path, err := globalPath()
	if err != nil {
		return Global{}, err
	}
	return loadGlobal(path)
}

func loadGlobal(path string) (Global, error) {
	value := Global{Version: 1, APIKeys: map[string]secret.Ref{}, GitHubApps: map[string]GitHubApp{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("parse global config: %w", err)
	}
	if value.Version != 1 {
		return value, fmt.Errorf("unsupported global config version %d", value.Version)
	}
	if value.APIKeys == nil {
		value.APIKeys = map[string]secret.Ref{}
	}
	if value.GitHubApps == nil {
		value.GitHubApps = map[string]GitHubApp{}
	}
	return value, nil
}

func SaveGlobal(value Global) error {
	path, err := globalPath()
	if err != nil {
		return err
	}
	return privatefile.WithLock(path, func() error { return saveGlobal(path, value) })
}

func UpdateGlobal(update func(*Global) error) (Global, error) {
	path, err := globalPath()
	if err != nil {
		return Global{}, err
	}
	var value Global
	err = privatefile.WithLock(path, func() error {
		var loadErr error
		value, loadErr = loadGlobal(path)
		if loadErr != nil {
			return loadErr
		}
		if updateErr := update(&value); updateErr != nil {
			return updateErr
		}
		return saveGlobal(path, value)
	})
	return value, err
}

func saveGlobal(path string, value Global) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.Write(path, append(raw, '\n'))
}
