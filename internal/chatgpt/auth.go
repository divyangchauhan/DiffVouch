// Package chatgpt implements the subscription authentication protocol used by
// the open-source Codex client. DiffVouch owns its session and never launches
// Codex or reads or changes Codex's credentials.
package chatgpt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
)

const (
	issuer    = "https://auth.openai.com"
	clientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	loginHint = "run 'diffvouch auth login openai --transport subscription'"
)

type Session struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	AccountID    string    `json:"account_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Login uses device authorization so the same flow works on local and remote
// machines. The caller displays only the verification URL and temporary code.
func Login(ctx context.Context, client *http.Client, storage string, show func(string, string) error) error {
	if storage != "auto" && storage != "keyring" && storage != "file" {
		return errors.New("secret storage must be auto, keyring, or file")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var device struct {
		ID            string          `json:"device_auth_id"`
		UserCode      string          `json:"user_code"`
		UserCodeAlias string          `json:"usercode"`
		Interval      json.RawMessage `json:"interval"`
	}
	status, err := post(ctx, client, "/api/accounts/deviceauth/usercode", map[string]string{"client_id": clientID}, &device)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("ChatGPT device login returned HTTP %d; check that device-code login is enabled in your ChatGPT settings", status)
	}
	if device.UserCode == "" {
		device.UserCode = device.UserCodeAlias
	}
	if device.ID == "" || device.UserCode == "" {
		return errors.New("ChatGPT returned an incomplete device login")
	}
	interval := 5 * time.Second
	if len(device.Interval) > 0 {
		seconds, err := strconv.Atoi(strings.Trim(string(device.Interval), "\""))
		if err != nil || seconds < 0 || seconds > 900 {
			return errors.New("ChatGPT returned an invalid login polling interval")
		}
		if seconds > 0 {
			interval = time.Duration(seconds) * time.Second
		}
	}
	if err := show(issuer+"/codex/device", device.UserCode); err != nil {
		return err
	}
	for {
		var approval struct {
			Code     string `json:"authorization_code"`
			Verifier string `json:"code_verifier"`
		}
		status, err := post(ctx, client, "/api/accounts/deviceauth/token", map[string]string{"device_auth_id": device.ID, "user_code": device.UserCode}, &approval)
		if err != nil {
			return err
		}
		if status == http.StatusOK {
			if approval.Code == "" || approval.Verifier == "" {
				return errors.New("ChatGPT returned incomplete device authorization")
			}
			session, err := exchange(ctx, client, url.Values{
				"grant_type": {"authorization_code"}, "client_id": {clientID},
				"code": {approval.Code}, "code_verifier": {approval.Verifier},
				"redirect_uri": {issuer + "/deviceauth/callback"},
			}, Session{})
			if err != nil {
				return err
			}
			return save(session, storage)
		}
		if status != http.StatusForbidden && status != http.StatusNotFound {
			return fmt.Errorf("ChatGPT device authorization returned HTTP %d", status)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func read(ref *secret.Ref) (Session, error) {
	if ref == nil {
		return Session{}, errors.New("ChatGPT subscription is not signed in; " + loginHint)
	}
	raw, err := secret.Read(*ref)
	if err != nil {
		return Session{}, fmt.Errorf("read ChatGPT session: %w", err)
	}
	var session Session
	if json.Unmarshal([]byte(raw), &session) != nil || session.AccessToken == "" || session.RefreshToken == "" || session.AccountID == "" || session.ExpiresAt.IsZero() {
		return Session{}, errors.New("invalid ChatGPT session; " + loginHint)
	}
	return session, nil
}

func Status() (string, error) {
	global, err := config.LoadGlobal()
	if err != nil {
		return "", err
	}
	session, err := read(global.ChatGPT)
	if err != nil {
		return "", err
	}
	if time.Now().After(session.ExpiresAt) {
		return "signed in; access token will refresh on use", nil
	}
	return "signed in", nil
}

// Credentials refreshes expiring credentials. rejectedToken requests one
// refresh after a 401, unless another process has already replaced that token.
func Credentials(ctx context.Context, client *http.Client, rejectedToken string) (Session, error) {
	global, err := config.LoadGlobal()
	if err != nil {
		return Session{}, err
	}
	session, err := read(global.ChatGPT)
	if err != nil {
		return Session{}, err
	}
	needsRefresh := func(s Session) bool {
		return time.Until(s.ExpiresAt) < time.Minute || (rejectedToken != "" && s.AccessToken == rejectedToken)
	}
	if !needsRefresh(session) {
		return session, nil
	}
	// The global-config lock also serializes token rotation with login/logout.
	_, err = config.UpdateGlobal(func(current *config.Global) error {
		var err error
		session, err = read(current.ChatGPT)
		if err != nil {
			return err
		}
		if !needsRefresh(session) {
			return nil
		}
		session, err = exchange(ctx, client, url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {session.RefreshToken}}, session)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(session)
		if err != nil {
			return err
		}
		_, err = secret.Store(current.ChatGPT.Name, string(raw), current.ChatGPT.Backend)
		return err
	})
	return session, err
}

func save(session Session, storage string) error {
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	ref, err := secret.Store(fmt.Sprintf("chatgpt:%d", time.Now().UnixNano()), string(raw), storage)
	if err != nil {
		return err
	}
	var previous *secret.Ref
	_, err = config.UpdateGlobal(func(global *config.Global) error {
		previous = global.ChatGPT
		global.ChatGPT = &ref
		return nil
	})
	if err != nil {
		_ = secret.Delete(ref)
		return err
	}
	if previous != nil {
		_ = secret.Delete(*previous)
	}
	return nil
}

func Logout() error {
	var previous *secret.Ref
	_, err := config.UpdateGlobal(func(global *config.Global) error {
		previous = global.ChatGPT
		global.ChatGPT = nil
		return nil
	})
	if err != nil {
		return err
	}
	if previous != nil {
		return secret.Delete(*previous)
	}
	return nil
}

func exchange(ctx context.Context, client *http.Client, form url.Values, previous Session) (Session, error) {
	var tokens struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ID        string `json:"id_token"`
		ExpiresIn int64  `json:"expires_in"`
	}
	var body any = form
	if form.Get("grant_type") == "refresh_token" {
		body = map[string]string{"grant_type": "refresh_token", "client_id": clientID, "refresh_token": form.Get("refresh_token")}
	}
	status, err := post(ctx, client, "/oauth/token", body, &tokens)
	if err != nil {
		return Session{}, err
	}
	if status != http.StatusOK {
		return Session{}, fmt.Errorf("ChatGPT token exchange returned HTTP %d; %s", status, loginHint)
	}
	if tokens.Access == "" {
		return Session{}, errors.New("ChatGPT token exchange returned no access token")
	}
	result := previous
	result.AccessToken = tokens.Access
	result.ExpiresAt = time.Time{}
	if tokens.Refresh != "" {
		result.RefreshToken = tokens.Refresh
	}
	// These unverified claims supply routing metadata only. The remote server
	// authenticates the token; they never grant local permissions.
	for _, token := range []string{tokens.Access, tokens.ID} {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims struct {
			Exp  int64 `json:"exp"`
			Auth struct {
				AccountID string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
		}
		if json.Unmarshal(raw, &claims) != nil {
			continue
		}
		if token == tokens.Access && claims.Exp > 0 {
			result.ExpiresAt = time.Unix(claims.Exp, 0)
		}
		if claims.Auth.AccountID != "" {
			if previous.AccountID != "" && previous.AccountID != claims.Auth.AccountID {
				return Session{}, errors.New("ChatGPT account changed during token refresh; " + loginHint)
			}
			result.AccountID = claims.Auth.AccountID
		}
	}
	if tokens.ExpiresIn > 0 && tokens.ExpiresIn <= 365*24*60*60 {
		result.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}
	if result.RefreshToken == "" || result.AccountID == "" || !result.ExpiresAt.After(time.Now()) {
		return Session{}, errors.New("ChatGPT token exchange returned incomplete or expired credentials")
	}
	return result, nil
}

func post(ctx context.Context, client *http.Client, path string, body any, target any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var raw []byte
	contentType := "application/json"
	if form, ok := body.(url.Values); ok {
		raw, contentType = []byte(form.Encode()), "application/x-www-form-urlencoded"
	} else {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "diffvouch")
	if client == nil {
		client = http.DefaultClient
	}
	// Authentication requests must not follow redirects with credential bodies.
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safeClient.Do(req)
	if err != nil {
		return 0, errors.New("ChatGPT authentication request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, nil
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return 0, errors.New("read ChatGPT authentication response")
	}
	if len(raw) > 1<<20 {
		return 0, errors.New("ChatGPT authentication response exceeded 1 MiB")
	}
	if json.Unmarshal(raw, target) != nil {
		return 0, errors.New("ChatGPT returned invalid authentication JSON")
	}
	return response.StatusCode, nil
}
