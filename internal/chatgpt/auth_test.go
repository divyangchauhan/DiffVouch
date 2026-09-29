package chatgpt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	return dir
}

func response(value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}
}

func token(account string, expiry time.Time) string {
	raw, _ := json.Marshal(map[string]any{"exp": expiry.Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
}

func TestDeviceLoginRefreshAndLogout(t *testing.T) {
	dir := testHome(t)
	t.Setenv("PATH", "")
	requests := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "auth.openai.com" {
			t.Fatalf("unexpected host: %s", r.URL.Host)
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("missing auth timeout")
		}
		switch requests {
		case 1:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Path != "/api/accounts/deviceauth/usercode" || body["client_id"] != clientID {
				t.Fatal("invalid device-code request")
			}
			return response(map[string]any{"device_auth_id": "device", "usercode": "TEST-CODE", "interval": "5"}), nil
		case 2:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Path != "/api/accounts/deviceauth/token" || body["device_auth_id"] != "device" || body["user_code"] != "TEST-CODE" {
				t.Fatal("invalid polling request")
			}
			return response(map[string]string{"authorization_code": "approved", "code_verifier": "verifier"}), nil
		case 3:
			_ = r.ParseForm()
			if r.URL.Path != "/oauth/token" || r.Form.Get("code_verifier") != "verifier" || r.Form.Get("redirect_uri") != issuer+"/deviceauth/callback" || r.Form.Get("grant_type") != "authorization_code" {
				t.Fatal("invalid PKCE exchange")
			}
			return response(map[string]any{"access_token": token("account", time.Now().Add(time.Hour)), "refresh_token": "refresh-1"}), nil
		case 4:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Header.Get("Content-Type") != "application/json" || body["grant_type"] != "refresh_token" || body["refresh_token"] != "refresh-1" {
				t.Fatal("invalid token refresh")
			}
			return response(map[string]any{"access_token": token("account", time.Now().Add(2*time.Hour)), "refresh_token": "refresh-2"}), nil
		default:
			t.Fatal("unexpected authentication request")
			return nil, nil
		}
	})}
	shown := false
	err := Login(context.Background(), client, "file", func(url, code string) error {
		shown = true
		if url != issuer+"/codex/device" || code != "TEST-CODE" {
			t.Fatal("incorrect device instructions")
		}
		return nil
	})
	if err != nil || !shown {
		t.Fatalf("login failed: %v", err)
	}
	session, err := Credentials(context.Background(), client, "")
	if err != nil || session.AccountID != "account" || requests != 3 {
		t.Fatalf("session not reused: %v", err)
	}
	if _, err := Credentials(context.Background(), client, session.AccessToken); err != nil {
		t.Fatal(err)
	}
	global, _ := config.LoadGlobal()
	stored, err := read(global.ChatGPT)
	if err != nil || stored.RefreshToken != "refresh-2" || requests != 4 {
		t.Fatal("rotation was not persisted")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "diffvouch", "config.json"))
	if strings.Contains(string(data), "refresh-2") || strings.Contains(string(data), "access_token") {
		t.Fatal("tokens leaked into config")
	}
	if err := Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := secret.Read(*global.ChatGPT); err == nil {
		t.Fatal("logout left credentials behind")
	}
	if _, err := Status(); err == nil {
		t.Fatal("status still reports logged in")
	}
}

func TestConcurrentRefreshRotatesOnce(t *testing.T) {
	testHome(t)
	if err := save(Session{AccessToken: "expired", RefreshToken: "original", AccountID: "account", ExpiresAt: time.Now().Add(-time.Hour)}, "file"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(map[string]any{"access_token": token("account", time.Now().Add(time.Hour)), "refresh_token": "rotated"}), nil
	})}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Credentials(context.Background(), client, "expired"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh raced: %d requests", calls.Load())
	}
}

func TestRefreshFailureDoesNotLeakOrReplaceCredentials(t *testing.T) {
	testHome(t)
	if err := save(Session{AccessToken: "expired", RefreshToken: "private-refresh", AccountID: "account", ExpiresAt: time.Now().Add(-time.Hour)}, "file"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"private-refresh"}`))}, nil
	})}
	_, err := Credentials(context.Background(), client, "")
	if err == nil || strings.Contains(err.Error(), "private-refresh") || !strings.Contains(err.Error(), "auth login") {
		t.Fatalf("unsafe or unhelpful error: %v", err)
	}
	global, _ := config.LoadGlobal()
	session, _ := read(global.ChatGPT)
	if session.RefreshToken != "private-refresh" {
		t.Fatal("failed refresh replaced credentials")
	}
}

func TestDeviceLoginCancellationAndMalformedResponses(t *testing.T) {
	for _, value := range []any{
		map[string]string{"device_auth_id": "device"},
		map[string]string{"device_auth_id": "device", "user_code": "code", "interval": "-1"},
	} {
		client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return response(value), nil })}
		if err := Login(context.Background(), client, "file", func(string, string) error { return nil }); err == nil {
			t.Fatal("accepted incomplete device response")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	requests := 0
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return response(map[string]string{"device_auth_id": "device", "user_code": "code", "interval": "5"}), nil
		}
		cancel()
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("pending"))}, nil
	})}
	if err := Login(ctx, client, "file", func(string, string) error { return nil }); err != context.Canceled {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}

func TestRefreshRejectsAccountChange(t *testing.T) {
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return response(map[string]any{"access_token": token("different-account", time.Now().Add(time.Hour)), "refresh_token": "new"}), nil
	})}
	previous := Session{AccountID: "original", RefreshToken: "old"}
	_, err := exchange(context.Background(), client, nil, previous)
	if err == nil || !strings.Contains(fmt.Sprint(err), "account changed") {
		t.Fatalf("account switch accepted: %v", err)
	}
}
