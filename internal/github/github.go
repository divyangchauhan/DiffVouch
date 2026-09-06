package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/dv"
	"github.com/divyangchauhan/DiffVouch/internal/gitdiff"
	"github.com/divyangchauhan/DiffVouch/internal/model"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
)

type Client struct {
	Config     config.GitHubApp
	privateKey string
	HTTP       *http.Client
}

type Pull struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"base"`
}

func DefaultURLs(host string) (api, web, version string) {
	if host == "github.com" {
		return "https://api.github.com", "https://github.com", "2026-03-10"
	}
	return "https://" + host + "/api/v3", "https://" + host, ""
}

func CreationURL(host, owner string) string {
	_, web, _ := DefaultURLs(host)
	return creationURL(web, owner)
}

func creationURL(web, owner string) string {
	if owner != "" {
		return web + "/organizations/" + url.PathEscape(owner) + "/settings/apps/new"
	}
	return web + "/settings/apps/new"
}

func OpenBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func Configure(appID, slug, keyPath, host, storage, apiBase, webBase, apiVersion string) (config.GitHubApp, error) {
	privateKeyRaw, err := os.ReadFile(keyPath)
	if err != nil {
		return config.GitHubApp{}, dv.Wrap(dv.ExitArguments, "read GitHub App private key", err)
	}
	return configurePrivateKey(appID, slug, privateKeyRaw, host, storage, apiBase, webBase, apiVersion)
}

func configurePrivateKey(appID, slug string, privateKeyRaw []byte, host, storage, apiBase, webBase, apiVersion string) (config.GitHubApp, error) {
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(appID) {
		return config.GitHubApp{}, dv.New(dv.ExitArguments, "GitHub App ID must be numeric")
	}
	if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(slug) {
		return config.GitHubApp{}, dv.New(dv.ExitArguments, "GitHub App slug must contain lowercase letters, digits, and hyphens")
	}
	defaultAPI, defaultWeb, defaultVersion := DefaultURLs(host)
	if apiBase == "" {
		apiBase = defaultAPI
	}
	if webBase == "" {
		webBase = defaultWeb
	}
	if apiVersion == "" {
		apiVersion = defaultVersion
	}
	provisional := config.GitHubApp{AppID: appID, Slug: slug, Host: host, APIBaseURL: strings.TrimRight(apiBase, "/"), WebBaseURL: strings.TrimRight(webBase, "/"), APIVersion: apiVersion}
	client := Client{Config: provisional, privateKey: string(privateKeyRaw), HTTP: &http.Client{Timeout: 60 * time.Second}}
	jwtToken, err := client.JWT()
	if err != nil {
		return config.GitHubApp{}, err
	}
	var app struct {
		ID   int    `json:"id"`
		Slug string `json:"slug"`
	}
	if err := client.request(http.MethodGet, "/app", jwtToken, nil, &app, "application/vnd.github+json"); err != nil {
		return config.GitHubApp{}, err
	}
	if strconv.Itoa(app.ID) != appID || (app.Slug != "" && app.Slug != slug) {
		return config.GitHubApp{}, dv.New(dv.ExitGitHub, "GitHub App credentials do not match the supplied ID and slug")
	}
	global, err := config.LoadGlobal()
	if err != nil {
		return config.GitHubApp{}, dv.Wrap(dv.ExitGitHub, "load global config", err)
	}
	old, hadOld := global.GitHubApps[host]
	ref, err := secret.Store(fmt.Sprintf("github-app:%s:%s:%d", host, appID, time.Now().UnixNano()), string(privateKeyRaw), storage)
	if err != nil {
		return config.GitHubApp{}, dv.Wrap(dv.ExitGitHub, "store GitHub App private key", err)
	}
	provisional.PrivateKey = ref
	global.GitHubApps[host] = provisional
	if err := config.SaveGlobal(global); err != nil {
		_ = secret.Delete(ref)
		return config.GitHubApp{}, dv.Wrap(dv.ExitGitHub, "save GitHub App config", err)
	}
	if hadOld && old.PrivateKey != ref {
		_ = secret.Delete(old.PrivateKey)
	}
	return provisional, nil
}

type ManifestCreateOptions struct {
	Name, Owner, Host, Storage, APIBaseURL, WebBaseURL, APIVersion string
	NoBrowser                                                      bool
	Timeout                                                        time.Duration
	OnReady                                                        func(string, error)
}

type ManifestCreation struct {
	App        config.GitHubApp
	InstallURL string
}

type manifestDefinition struct {
	Name                  string            `json:"name"`
	URL                   string            `json:"url"`
	Description           string            `json:"description"`
	RedirectURL           string            `json:"redirect_url"`
	Public                bool              `json:"public"`
	DefaultPermissions    map[string]string `json:"default_permissions"`
	DefaultEvents         []string          `json:"default_events"`
	RequestOAuthOnInstall bool              `json:"request_oauth_on_install"`
	HookAttributes        struct {
		URL    string `json:"url"`
		Active bool   `json:"active"`
	} `json:"hook_attributes"`
}

type manifestCallback struct {
	code string
}

var manifestPage = template.Must(template.New("manifest").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Create DiffVouch GitHub App</title></head>
<body><p>Redirecting to GitHub to create your private DiffVouch app…</p>
<form id="manifest" action="{{.Action}}" method="post">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<button type="submit">Continue to GitHub</button>
</form><script>document.getElementById('manifest').submit()</script></body></html>`))

func CreateFromManifest(ctx context.Context, options ManifestCreateOptions) (ManifestCreation, error) {
	if options.Host == "" {
		options.Host = "github.com"
	}
	defaultAPI, defaultWeb, defaultVersion := DefaultURLs(options.Host)
	if options.APIBaseURL == "" {
		options.APIBaseURL = defaultAPI
	}
	if options.WebBaseURL == "" {
		options.WebBaseURL = defaultWeb
	}
	if options.APIVersion == "" {
		options.APIVersion = defaultVersion
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Minute
	}
	if options.Name == "" {
		suffix, err := randomHex(4)
		if err != nil {
			return ManifestCreation{}, dv.Wrap(dv.ExitGitHub, "generate GitHub App name", err)
		}
		options.Name = "diffvouch-" + suffix
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ManifestCreation{}, dv.Wrap(dv.ExitGitHub, "start local GitHub App callback", err)
	}
	callbackURL := "http://" + listener.Addr().String() + "/callback"
	state, err := randomHex(32)
	if err != nil {
		_ = listener.Close()
		return ManifestCreation{}, dv.Wrap(dv.ExitGitHub, "generate manifest state", err)
	}
	manifest := newManifest(options.Name, callbackURL)
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		_ = listener.Close()
		return ManifestCreation{}, err
	}
	action := creationURL(strings.TrimRight(options.WebBaseURL, "/"), options.Owner) + "?state=" + url.QueryEscape(state)
	callback := make(chan manifestCallback, 1)
	var callbackUsed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = manifestPage.Execute(writer, map[string]string{"Action": action, "Manifest": string(manifestRaw)})
	})
	mux.HandleFunc("/callback", func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		returnedState, code := query.Get("state"), query.Get("code")
		if subtle.ConstantTimeCompare([]byte(returnedState), []byte(state)) != 1 || code == "" {
			http.Error(writer, "Invalid or expired DiffVouch manifest callback.", http.StatusBadRequest)
			return
		}
		if !callbackUsed.CompareAndSwap(false, true) {
			http.Error(writer, "This manifest callback was already used.", http.StatusConflict)
			return
		}
		select {
		case callback <- manifestCallback{code: code}:
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(writer, "<!doctype html><title>DiffVouch confirmation received</title><p>GitHub confirmation received. You can close this tab and return to DiffVouch.</p>")
		default:
			http.Error(writer, "This manifest callback was already used.", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	startURL := "http://" + listener.Addr().String() + "/start"
	var browserErr error
	if !options.NoBrowser {
		browserErr = OpenBrowser(startURL)
	}
	if options.OnReady != nil {
		options.OnReady(startURL, browserErr)
	}
	waitContext, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	var received manifestCallback
	select {
	case received = <-callback:
	case <-waitContext.Done():
		return ManifestCreation{}, dv.Wrap(dv.ExitGitHub, "GitHub App creation did not complete", waitContext.Err())
	}
	return createFromManifestCode(options, received.code)
}

func CreateFromManifestCode(options ManifestCreateOptions, code string) (ManifestCreation, error) {
	if options.Host == "" {
		options.Host = "github.com"
	}
	defaultAPI, defaultWeb, defaultVersion := DefaultURLs(options.Host)
	if options.APIBaseURL == "" {
		options.APIBaseURL = defaultAPI
	}
	if options.WebBaseURL == "" {
		options.WebBaseURL = defaultWeb
	}
	if options.APIVersion == "" {
		options.APIVersion = defaultVersion
	}
	return createFromManifestCode(options, code)
}

func createFromManifestCode(options ManifestCreateOptions, code string) (ManifestCreation, error) {
	if strings.TrimSpace(code) == "" {
		return ManifestCreation{}, dv.New(dv.ExitArguments, "manifest conversion code cannot be empty")
	}
	conversion, err := exchangeManifest(options.APIBaseURL, options.APIVersion, strings.TrimSpace(code))
	if err != nil {
		return ManifestCreation{}, err
	}
	app, err := configurePrivateKey(strconv.FormatInt(conversion.ID, 10), conversion.Slug, []byte(conversion.PEM), options.Host, options.Storage, options.APIBaseURL, options.WebBaseURL, options.APIVersion)
	if err != nil {
		return ManifestCreation{}, err
	}
	return ManifestCreation{App: app, InstallURL: strings.TrimRight(app.WebBaseURL, "/") + "/apps/" + url.PathEscape(app.Slug) + "/installations/new"}, nil
}

func newManifest(name, callbackURL string) manifestDefinition {
	manifest := manifestDefinition{
		Name: name, URL: "https://github.com/divyangchauhan/DiffVouch",
		Description: "Local-first pull request reviews from DiffVouch",
		RedirectURL: callbackURL, Public: false,
		DefaultPermissions: map[string]string{"pull_requests": "write"}, DefaultEvents: []string{},
		RequestOAuthOnInstall: false,
	}
	manifest.HookAttributes.URL = "https://github.com/divyangchauhan/DiffVouch"
	manifest.HookAttributes.Active = false
	return manifest
}

func randomHex(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

type manifestConversion struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	PEM  string `json:"pem"`
}

func exchangeManifest(apiBase, apiVersion, code string) (manifestConversion, error) {
	client := &Client{Config: config.GitHubApp{APIBaseURL: strings.TrimRight(apiBase, "/"), APIVersion: apiVersion}, HTTP: &http.Client{Timeout: 60 * time.Second}}
	var conversion manifestConversion
	if err := client.request(http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &conversion, "application/vnd.github+json"); err != nil {
		return conversion, err
	}
	if conversion.ID <= 0 || conversion.Slug == "" || conversion.PEM == "" {
		return conversion, dv.New(dv.ExitGitHub, "GitHub returned an incomplete App manifest conversion")
	}
	return conversion, nil
}

func LoadApp(host string) (config.GitHubApp, error) {
	global, err := config.LoadGlobal()
	if err != nil {
		return config.GitHubApp{}, dv.Wrap(dv.ExitGitHub, "load global config", err)
	}
	app, ok := global.GitHubApps[host]
	if !ok {
		return app, dv.New(dv.ExitGitHub, "no GitHub App configured for "+host+"; run 'diffvouch github app create'")
	}
	return app, nil
}

func RemoveApp(host string) (bool, error) {
	global, err := config.LoadGlobal()
	if err != nil {
		return false, err
	}
	app, ok := global.GitHubApps[host]
	if !ok {
		return false, nil
	}
	delete(global.GitHubApps, host)
	if err := config.SaveGlobal(global); err != nil {
		return false, err
	}
	_ = secret.Delete(app.PrivateKey)
	return true, nil
}

func NewClient(app config.GitHubApp) (*Client, error) {
	key, err := secret.Read(app.PrivateKey)
	if err != nil {
		return nil, dv.Wrap(dv.ExitGitHub, "read GitHub App private key", err)
	}
	return &Client{Config: app, privateKey: key, HTTP: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (c *Client) JWT() (string, error) {
	now := time.Now().Unix()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{"iat": now - 60, "exp": now + 540, "iss": c.Config.AppID})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	key, err := parsePrivateKey([]byte(c.privateKey))
	if err != nil {
		return "", dv.Wrap(dv.ExitGitHub, "parse GitHub App private key", err)
	}
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", dv.Wrap(dv.ExitGitHub, "sign GitHub App JWT", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parsePrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("PEM block not found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}

func (c *Client) App() (map[string]any, error) {
	token, err := c.JWT()
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = c.request(http.MethodGet, "/app", token, nil, &result, "application/vnd.github+json")
	return result, err
}

func (c *Client) InstallationToken(owner, repo string) (string, error) {
	jwtToken, err := c.JWT()
	if err != nil {
		return "", err
	}
	var installation struct {
		ID int64 `json:"id"`
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/installation"
	if err := c.request(http.MethodGet, path, jwtToken, nil, &installation, "application/vnd.github+json"); err != nil {
		return "", err
	}
	var response struct {
		Token string `json:"token"`
	}
	body := map[string]any{"repositories": []string{repo}, "permissions": map[string]string{"pull_requests": "write"}}
	if err := c.request(http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installation.ID), jwtToken, body, &response, "application/vnd.github+json"); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", dv.New(dv.ExitGitHub, "GitHub did not return an installation token")
	}
	return response.Token, nil
}

func (c *Client) request(method, path, token string, body any, target any, accept string) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Config.APIBaseURL, "/")+path, reader)
	if err != nil {
		return dv.Wrap(dv.ExitGitHub, "create GitHub request", err)
	}
	request.Header.Set("Accept", accept)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("User-Agent", "DiffVouch/0.1")
	request.Header.Set("Content-Type", "application/json")
	if c.Config.APIVersion != "" {
		request.Header.Set("X-GitHub-Api-Version", c.Config.APIVersion)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return dv.Wrap(dv.ExitGitHub, "GitHub API request failed", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := string(raw)
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		return dv.New(dv.ExitGitHub, fmt.Sprintf("GitHub API %s %s returned HTTP %d: %s", method, path, response.StatusCode, detail))
	}
	if bytesTarget, ok := target.(*[]byte); ok {
		*bytesTarget = raw
		return nil
	}
	if target != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, target); err != nil {
			return dv.Wrap(dv.ExitGitHub, "decode GitHub response", err)
		}
	}
	return nil
}

func ParseRemote(repo, explicit, explicitHost string) (host, owner, name string, err error) {
	if explicit != "" && !regexp.MustCompile(`^[^/\s]+/[^/\s]+$`).MatchString(explicit) {
		return "", "", "", dv.New(dv.ExitArguments, "--repo must be owner/name")
	}
	remote, remoteErr := gitdiff.Run(repo, 64*1024, "remote", "get-url", "origin")
	if remoteErr != nil {
		if explicit != "" {
			owner, name = splitRepo(explicit)
			host = explicitHost
			if host == "" {
				host = "github.com"
			}
			return
		}
		return "", "", "", remoteErr
	}
	remote = strings.TrimSpace(remote)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^git@([^:]+):([^/]+)/(.+)$`),
		regexp.MustCompile(`^ssh://git@([^/]+)/([^/]+)/(.+)$`),
		regexp.MustCompile(`^https?://([^/]+)/([^/]+)/(.+)$`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(remote)
		if len(match) == 4 {
			host, owner, name = match[1], match[2], strings.TrimSuffix(match[3], ".git")
			if explicit != "" {
				owner, name = splitRepo(explicit)
			}
			if explicitHost != "" {
				host = explicitHost
			}
			return
		}
	}
	if explicit != "" {
		owner, name = splitRepo(explicit)
		host = explicitHost
		if host == "" {
			host = "github.com"
		}
		return
	}
	return "", "", "", dv.New(dv.ExitGitHub, "cannot resolve GitHub repository from origin; pass --repo owner/name")
}

func splitRepo(value string) (string, string) {
	parts := strings.SplitN(value, "/", 2)
	return parts[0], parts[1]
}

func ResolvePull(repo, explicitRepo, explicitHost string, number int) (string, Pull, error) {
	host, owner, name, err := ParseRemote(repo, explicitRepo, explicitHost)
	if err != nil {
		return "", Pull{}, err
	}
	app, err := LoadApp(host)
	if err != nil {
		return "", Pull{}, err
	}
	client, err := NewClient(app)
	if err != nil {
		return "", Pull{}, err
	}
	token, err := client.InstallationToken(owner, name)
	if err != nil {
		return "", Pull{}, err
	}
	pull, err := discoverPull(client, token, repo, owner, name, number)
	return owner + "/" + name, pull, err
}

func discoverPull(client *Client, token, repo, owner, name string, number int) (Pull, error) {
	if number > 0 {
		var pull Pull
		err := client.request(http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(name), number), token, nil, &pull, "application/vnd.github+json")
		return pull, err
	}
	branch, err := gitdiff.Run(repo, 4096, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return Pull{}, dv.New(dv.ExitGitHub, "detached HEAD requires --pr")
	}
	head, err := gitdiff.Run(repo, 4096, "rev-parse", "HEAD")
	if err != nil {
		return Pull{}, err
	}
	var matches []Pull
	for page := 1; page <= 100; page++ {
		var pulls []Pull
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100&page=%d", url.PathEscape(owner), url.PathEscape(name), page)
		if err := client.request(http.MethodGet, path, token, nil, &pulls, "application/vnd.github+json"); err != nil {
			return Pull{}, err
		}
		for _, pull := range pulls {
			if pull.Head.Ref == strings.TrimSpace(branch) && pull.Head.SHA == strings.TrimSpace(head) {
				matches = append(matches, pull)
			}
		}
		if len(pulls) < 100 {
			break
		}
	}
	if len(matches) != 1 {
		return Pull{}, dv.New(dv.ExitGitHub, "could not resolve exactly one open PR for the current branch; pass --pr")
	}
	return matches[0], nil
}

type DiffLocation struct {
	Path, Side string
	Line       int
}

func ChangedLines(diff string) map[DiffLocation]struct{} {
	result := map[DiffLocation]struct{}{}
	oldPath, newPath := "", ""
	oldLine, newLine := 0, 0
	inHunk := false
	hunk := regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			oldPath, newPath, inHunk = "", "", false
		case !inHunk && strings.HasPrefix(line, "--- "):
			oldPath = decodeDiffPath(strings.TrimPrefix(line, "--- "))
		case !inHunk && strings.HasPrefix(line, "+++ "):
			newPath = decodeDiffPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ "):
			match := hunk.FindStringSubmatch(line)
			if len(match) == 3 {
				oldLine, _ = strconv.Atoi(match[1])
				newLine, _ = strconv.Atoi(match[2])
				inHunk = true
			}
		case inHunk && strings.HasPrefix(line, "+") && newPath != "":
			result[DiffLocation{newPath, "RIGHT", newLine}] = struct{}{}
			newLine++
		case inHunk && strings.HasPrefix(line, "-"):
			path := newPath
			if path == "" {
				path = oldPath
			}
			if path != "" {
				result[DiffLocation{path, "LEFT", oldLine}] = struct{}{}
			}
			oldLine++
		case inHunk && strings.HasPrefix(line, " "):
			oldLine++
			newLine++
		}
	}
	return result
}

func decodeDiffPath(value string) string {
	if value == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(value, "\"") {
		if decoded, err := strconv.Unquote(value); err == nil {
			value = decoded
		}
	}
	if strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/") {
		value = value[2:]
	}
	return value
}

func Publish(result *model.ReviewResult, repo, explicitRepo, explicitHost string, number int) (string, string, error) {
	if result.Partial || result.Status != "complete" {
		return "", "", dv.New(dv.ExitGitHub, "only complete reviews may be published")
	}
	host, owner, name, err := ParseRemote(repo, explicitRepo, explicitHost)
	if err != nil {
		return "", "", err
	}
	app, err := LoadApp(host)
	if err != nil {
		return "", "", err
	}
	client, err := NewClient(app)
	if err != nil {
		return "", "", err
	}
	token, err := client.InstallationToken(owner, name)
	if err != nil {
		return "", "", err
	}
	pull, err := discoverPull(client, token, repo, owner, name, number)
	if err != nil {
		return "", "", err
	}
	if err := validatePullForReview(pull, result); err != nil {
		return "", "", err
	}
	var diffRaw []byte
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(name), pull.Number)
	if err := client.request(http.MethodGet, path, token, nil, &diffRaw, "application/vnd.github.v3.diff"); err != nil {
		return "", "", err
	}
	eligible := ChangedLines(string(diffRaw))
	var comments []map[string]any
	for _, finding := range result.Findings {
		if finding.Confidence == "low" || finding.Path == nil || finding.Line == nil || finding.Side == nil {
			continue
		}
		side := "RIGHT"
		if *finding.Side == "old" {
			side = "LEFT"
		}
		if _, ok := eligible[DiffLocation{*finding.Path, side, *finding.Line}]; !ok {
			continue
		}
		body := fmt.Sprintf("**%s**\n\n%s\n\n**Recommendation:** %s", finding.Title, finding.Explanation, finding.Recommendation)
		if finding.Confidence == "medium" {
			body = "**Medium confidence:** " + body
		}
		comments = append(comments, map[string]any{"path": *finding.Path, "line": *finding.Line, "side": side, "body": body})
	}
	body := map[string]any{"commit_id": result.Scope.HeadSHA, "event": "COMMENT", "body": ReviewBody(*result), "comments": comments}
	currentPull, err := discoverPull(client, token, repo, owner, name, pull.Number)
	if err != nil {
		return "", "", err
	}
	if err := validatePullForReview(currentPull, result); err != nil {
		return "", "", err
	}
	var response struct {
		HTMLURL string `json:"html_url"`
	}
	if err := client.request(http.MethodPost, path+"/reviews", token, body, &response, "application/vnd.github+json"); err != nil {
		return "", "", err
	}
	if response.HTMLURL == "" {
		return "", "", dv.New(dv.ExitGitHub, "GitHub created the review but returned no URL")
	}
	return response.HTMLURL, app.Slug + "[bot]", nil
}

func validatePullForReview(pull Pull, result *model.ReviewResult) error {
	if pull.State != "open" {
		return dv.New(dv.ExitGitHub, "pull request is not open")
	}
	if pull.Head.SHA != result.Scope.HeadSHA {
		return dv.New(dv.ExitGitHub, "PR head changed after review; run a fresh review")
	}
	if pull.Base.SHA != result.Scope.BaseSHA {
		return dv.New(dv.ExitGitHub, "PR base changed after review; run a fresh review")
	}
	return nil
}

func ReviewBody(result model.ReviewResult) string {
	var output strings.Builder
	fmt.Fprintf(&output, "## DiffVouch review\n\n**Rating: %.1f/5 — %s**\n\n%s\n\n", result.Rating.Overall, result.Rating.Label, result.Summary)
	for _, group := range []struct {
		heading  string
		blocking bool
	}{{"Blocking issues", true}, {"Non-blocking issues", false}} {
		output.WriteString("### " + group.heading + "\n\n")
		count := 0
		for _, finding := range result.Findings {
			if finding.Blocking != group.blocking {
				continue
			}
			count++
			locationText := ""
			if finding.Path != nil {
				locationText = " `" + gitdiff.DisplayPath(*finding.Path)
				if finding.Line != nil {
					locationText += fmt.Sprintf(":%d", *finding.Line)
				}
				locationText += "`"
			}
			fmt.Fprintf(&output, "- **%s · %s**%s\n  %s\n  **Recommendation:** %s\n", finding.Severity, finding.Title, locationText, finding.Explanation, finding.Recommendation)
		}
		if count == 0 {
			output.WriteString("None.\n")
		}
		output.WriteString("\n")
	}
	if len(result.NeedsVerification) > 0 {
		output.WriteString("### Needs verification\n\n")
		for _, item := range result.NeedsVerification {
			fmt.Fprintf(&output, "- %s\n", item)
		}
		output.WriteString("\n")
	}
	output.WriteString("### Rating breakdown\n\n| Dimension | Score |\n|---|---:|\n")
	fmt.Fprintf(&output, "| Correctness | %.1f/5 |\n| Security | %.1f/5 |\n| Maintainability | %.1f/5 |\n| Testing | %.1f/5 |\n| Scope | %.1f/5 |\n\n",
		result.Rating.Dimensions.Correctness, result.Rating.Dimensions.Security,
		result.Rating.Dimensions.Maintainability, result.Rating.Dimensions.Testing,
		result.Rating.Dimensions.Scope)
	fmt.Fprintf(&output, "Provider: `%s/%s` · Model: `%s` · Reviewed commit: `%s`", result.Provider.Name, result.Provider.Transport, result.Provider.Model, result.Scope.HeadSHA)
	return output.String()
}
