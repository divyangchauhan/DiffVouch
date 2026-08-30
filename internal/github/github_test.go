package github

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/config"
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
