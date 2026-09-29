package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var ErrAllowance = errors.New("ChatGPT included allowance is unavailable; evaluation paused")

type Usage struct {
	RateLimit struct {
		Allowed   *bool   `json:"allowed"`
		Reached   bool    `json:"limit_reached"`
		Primary   *Window `json:"primary_window"`
		Secondary *Window `json:"secondary_window"`
	} `json:"rate_limit"`
	ResetCredits struct {
		Available  int `json:"available_count"`
		Applicable int `json:"applicable_available_count"`
	} `json:"rate_limit_reset_credits"`
}

type Window struct {
	UsedPercent float64 `json:"used_percent"`
	Seconds     int     `json:"limit_window_seconds"`
	ResetAt     int64   `json:"reset_at"`
}

type ResetCredit struct {
	ID        string     `json:"id"`
	Type      string     `json:"reset_type"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func ReadUsage(ctx context.Context, client *http.Client) (Usage, error) {
	var result Usage
	err := accountRequest(ctx, client, "usage", nil, &result)
	return result, err
}

func ReadResetCredits(ctx context.Context, client *http.Client) ([]ResetCredit, error) {
	var result struct {
		Credits []ResetCredit `json:"credits"`
	}
	err := accountRequest(ctx, client, "rate-limit-reset-credits", nil, &result)
	return result.Credits, err
}

// ConsumeReset requires a specific credit and stable request ID. Callers must
// persist that ID before the request so retries cannot redeem another credit.
func ConsumeReset(ctx context.Context, client *http.Client, creditID, requestID string) (string, error) {
	if creditID == "" || requestID == "" {
		return "", errors.New("reset requires a credit ID and idempotency key")
	}
	var result struct {
		Code string `json:"code"`
	}
	err := accountRequest(ctx, client, "rate-limit-reset-credits/consume", map[string]string{"credit_id": creditID, "redeem_request_id": requestID}, &result)
	return result.Code, err
}

func accountRequest(ctx context.Context, client *http.Client, path string, body any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := Credentials(ctx, client, "")
	if err != nil {
		return err
	}
	method := http.MethodGet
	var raw []byte
	if body != nil {
		method = http.MethodPost
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, "https://chatgpt.com/backend-api/wham/"+path, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+session.AccessToken)
		req.Header.Set("ChatGPT-Account-Id", session.AccountID)
		req.Header.Set("User-Agent", "diffvouch")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if client == nil {
			client = http.DefaultClient
		}
		safe := *client
		safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		res, err := safe.Do(req)
		if err != nil {
			return errors.New("ChatGPT account request failed")
		}
		if res.StatusCode == 401 && attempt == 0 {
			res.Body.Close()
			session, err = Credentials(ctx, client, session.AccessToken)
			if err != nil {
				return err
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("ChatGPT account request returned HTTP %d", res.StatusCode)
		}
		if readErr != nil || len(data) > 1<<20 {
			return errors.New("invalid ChatGPT account response size")
		}
		if json.Unmarshal(data, result) != nil {
			return errors.New("invalid ChatGPT account response JSON")
		}
		return nil
	}
	return errors.New("ChatGPT account authentication failed")
}
