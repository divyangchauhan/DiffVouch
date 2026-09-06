package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/model"
)

func privateKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestJWTUsesRS256AndShortLifetime(t *testing.T) {
	client := Client{Config: config.GitHubApp{AppID: "123"}, privateKey: privateKey(t)}
	token, err := client.JWT()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid JWT: %s", token)
	}
	headerRaw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header map[string]string
	_ = json.Unmarshal(headerRaw, &header)
	payloadRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var payload map[string]any
	_ = json.Unmarshal(payloadRaw, &payload)
	if header["alg"] != "RS256" || payload["iss"] != "123" {
		t.Fatalf("wrong claims: %#v %#v", header, payload)
	}
	if payload["exp"].(float64)-payload["iat"].(float64) > 600 || int64(payload["iat"].(float64)) > time.Now().Unix() {
		t.Fatal("JWT lifetime is unsafe")
	}
}

func TestChangedLinesIncludesDeletedSide(t *testing.T) {
	diff := "diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\n--- a/gone.txt\n+++ /dev/null\n@@ -2 +0,0 @@\n-removed\n"
	if _, ok := ChangedLines(diff)[DiffLocation{"gone.txt", "LEFT", 2}]; !ok {
		t.Fatal("deleted line is not eligible")
	}
}

func TestValidatePullForReviewRejectsChangedHead(t *testing.T) {
	result := &model.ReviewResult{Scope: model.Scope{BaseSHA: "base", HeadSHA: "reviewed"}}
	pull := Pull{State: "open"}
	pull.Base.SHA = "base"
	pull.Head.SHA = "new-head"
	if err := validatePullForReview(pull, result); err == nil || !strings.Contains(err.Error(), "head changed") {
		t.Fatalf("changed head was accepted: %v", err)
	}
}

func TestReviewBodyEscapesFilenameControls(t *testing.T) {
	path := "forged\n\x1b[31m`file.go"
	result := model.ReviewResult{Findings: []model.Finding{{Severity: model.Medium, Title: "Finding", Path: &path}}}
	body := ReviewBody(result)
	if strings.Contains(body, "forged\n") || strings.Contains(body, "\x1b") || strings.Contains(body, "`file.go") || !strings.Contains(body, `forged\n\x1b`) {
		t.Fatalf("unsafe path reached review Markdown: %q", body)
	}
}

func TestRequestFallsBackToDefaultHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := Client{Config: config.GitHubApp{APIBaseURL: server.URL}}
	var response map[string]bool
	if err := client.request(http.MethodGet, "/test", "token", nil, &response, "application/json"); err != nil {
		t.Fatal(err)
	}
	if !response["ok"] {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestManifestHasMinimumPermissionsAndNoWebhooks(t *testing.T) {
	manifest := newManifest("diffvouch-test", "http://127.0.0.1:1234/callback")
	if manifest.Public || manifest.RequestOAuthOnInstall || manifest.HookAttributes.Active {
		t.Fatalf("manifest enables an unnecessary capability: %#v", manifest)
	}
	if len(manifest.DefaultEvents) != 0 || len(manifest.DefaultPermissions) != 1 || manifest.DefaultPermissions["pull_requests"] != "write" {
		t.Fatalf("manifest permissions are not minimal: %#v", manifest)
	}
}

func TestCreateFromManifestCompletesCallbackAndStoresApp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	key := privateKey(t)
	var apiServer *httptest.Server
	apiServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/app-manifests/temporary-code/conversions":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "" {
				t.Errorf("unsafe conversion request: method=%s authorization=%q", request.Method, request.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": 321, "slug": "diffvouch-test", "pem": key})
		case "/app":
			if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
				t.Error("app validation did not use a JWT")
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": 321, "slug": "diffvouch-test"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer apiServer.Close()

	created, err := CreateFromManifest(context.Background(), ManifestCreateOptions{
		Name: "diffvouch-test", Host: "github.example.test", Storage: "file",
		APIBaseURL: apiServer.URL, WebBaseURL: apiServer.URL, Timeout: 3 * time.Second,
		NoBrowser: true,
		OnReady: func(startURL string, browserErr error) {
			go func() {
				response, getErr := http.Get(startURL)
				if getErr != nil {
					t.Error(getErr)
					return
				}
				raw, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil {
					t.Error(readErr)
					return
				}
				page := string(raw)
				actionMatch := regexp.MustCompile(`action="([^"]+)"`).FindStringSubmatch(page)
				manifestMatch := regexp.MustCompile(`name="manifest" value="([^"]+)"`).FindStringSubmatch(page)
				if len(actionMatch) != 2 || len(manifestMatch) != 2 {
					t.Errorf("manifest form is incomplete: %s", page)
					return
				}
				actionURL, parseErr := url.Parse(html.UnescapeString(actionMatch[1]))
				if parseErr != nil {
					t.Error(parseErr)
					return
				}
				var manifest manifestDefinition
				if decodeErr := json.Unmarshal([]byte(html.UnescapeString(manifestMatch[1])), &manifest); decodeErr != nil {
					t.Error(decodeErr)
					return
				}
				badResponse, badErr := http.Get(manifest.RedirectURL + "?code=temporary-code&state=wrong")
				if badErr != nil {
					t.Error(badErr)
					return
				}
				_ = badResponse.Body.Close()
				if badResponse.StatusCode != http.StatusBadRequest {
					t.Errorf("invalid state returned %d", badResponse.StatusCode)
					return
				}
				callbackURL := manifest.RedirectURL + "?code=temporary-code&state=" + url.QueryEscape(actionURL.Query().Get("state"))
				callbackResponse, callbackErr := http.Get(callbackURL)
				if callbackErr != nil {
					t.Error(callbackErr)
					return
				}
				_ = callbackResponse.Body.Close()
				if callbackResponse.StatusCode != http.StatusOK {
					t.Errorf("callback returned %d", callbackResponse.StatusCode)
				}
			}()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.App.AppID != "321" || created.App.Slug != "diffvouch-test" || created.App.PrivateKey.Backend != "file" {
		t.Fatalf("unexpected created app: %#v", created)
	}
	if created.InstallURL != apiServer.URL+"/apps/diffvouch-test/installations/new" {
		t.Fatalf("unexpected installation URL: %s", created.InstallURL)
	}
}

func TestDiscoverPullPaginates(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "--initial-branch=feature"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.invalid"},
		{"commit", "--allow-empty", "-q", "-m", "initial"},
	} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if err := command.Run(); err != nil {
			t.Fatal(err)
		}
	}
	headCommand := exec.Command("git", "rev-parse", "HEAD")
	headCommand.Dir = repo
	headRaw, err := headCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(headRaw))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("page") == "1" {
			pulls := make([]Pull, 100)
			for index := range pulls {
				pulls[index].Number = index + 1
				pulls[index].Head.Ref = "someone-else"
			}
			_ = json.NewEncoder(writer).Encode(pulls)
			return
		}
		pull := Pull{Number: 101}
		pull.Head.Ref = "feature"
		pull.Head.SHA = head
		_ = json.NewEncoder(writer).Encode([]Pull{pull})
	}))
	defer server.Close()
	client := &Client{Config: config.GitHubApp{APIBaseURL: server.URL}, HTTP: server.Client()}
	pull, err := discoverPull(client, "token", repo, "owner", "project", 0)
	if err != nil {
		t.Fatal(err)
	}
	if pull.Number != 101 {
		t.Fatalf("wrong pull request: %#v", pull)
	}
}

func TestParseRemoteAndExplicitRepository(t *testing.T) {
	repo := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repo
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("git", "remote", "add", "origin", "git@github.com:owner/DiffVouch.git")
	command.Dir = repo
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	host, owner, name, err := ParseRemote(repo, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if host != "github.com" || owner != "owner" || name != "DiffVouch" {
		t.Fatalf("wrong remote: %s %s %s", host, owner, name)
	}
	repoWithoutOrigin := filepath.Join(t.TempDir(), "repo")
	_ = repoWithoutOrigin
	host, owner, name, err = ParseRemote(repo, "other/project", "github.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if host != "github.example.com" || owner != "other" || name != "project" {
		t.Fatalf("wrong override: %s %s %s", host, owner, name)
	}
}
