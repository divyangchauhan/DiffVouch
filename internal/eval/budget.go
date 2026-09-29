package eval

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/chatgpt"
	"github.com/divyangchauhan/DiffVouch/internal/model"
)

type budget struct {
	dir         string
	resetBefore time.Time
	client      *http.Client
}

func (b budget) before(ctx context.Context) error {
	u, err := chatgpt.ReadUsage(ctx, b.client)
	if err != nil {
		return fmt.Errorf("%w: %v", chatgpt.ErrAllowance, err)
	}
	if err = writeJSON(filepath.Join(b.dir, "usage.json"), map[string]any{"observed_at": time.Now().UTC(), "usage": u}); err != nil {
		return fmt.Errorf("%w: record usage: %v", chatgpt.ErrAllowance, err)
	}
	if includedAllowed(u) {
		return nil
	}
	if u.RateLimit.Allowed == nil || b.resetBefore.IsZero() || u.ResetCredits.Applicable < 1 {
		return chatgpt.ErrAllowance
	}
	credits, err := chatgpt.ReadResetCredits(ctx, b.client)
	if err != nil {
		return fmt.Errorf("%w: %v", chatgpt.ErrAllowance, err)
	}
	sort.Slice(credits, func(i, j int) bool {
		if credits[i].ExpiresAt == nil {
			return false
		}
		if credits[j].ExpiresAt == nil {
			return true
		}
		return credits[i].ExpiresAt.Before(*credits[j].ExpiresAt)
	})
	for _, credit := range credits {
		if credit.Type != "codex_rate_limits" || credit.Status != "available" || credit.ExpiresAt == nil || !credit.ExpiresAt.After(time.Now()) || credit.ExpiresAt.After(b.resetBefore) {
			continue
		}
		path := filepath.Join(b.dir, "resets", digest([]byte(credit.ID))+".json")
		state := struct {
			RequestID string `json:"request_id"`
			Result    string `json:"result"`
		}{}
		if err := readJSON(path, &state); errors.Is(err, os.ErrNotExist) {
			state.RequestID = model.NewReviewID()
		} else if err != nil {
			return fmt.Errorf("%w: invalid saved reset attempt: %v", chatgpt.ErrAllowance, err)
		}
		if state.RequestID == "" {
			return fmt.Errorf("%w: invalid saved reset attempt", chatgpt.ErrAllowance)
		}
		if err := writeJSON(path, state); err != nil {
			return fmt.Errorf("%w: %v", chatgpt.ErrAllowance, err)
		}
		state.Result, err = chatgpt.ConsumeReset(ctx, b.client, credit.ID, state.RequestID)
		if err != nil {
			return fmt.Errorf("%w: reset failed: %v", chatgpt.ErrAllowance, err)
		}
		if err := writeJSON(path, state); err != nil {
			return fmt.Errorf("%w: %v", chatgpt.ErrAllowance, err)
		}
		if state.Result != "reset" && state.Result != "already_redeemed" {
			return fmt.Errorf("%w: reset returned %s", chatgpt.ErrAllowance, state.Result)
		}
		fresh, err := chatgpt.ReadUsage(ctx, b.client)
		if err == nil && includedAllowed(fresh) {
			return nil
		}
		return chatgpt.ErrAllowance
	}
	return chatgpt.ErrAllowance
}

func includedAllowed(u chatgpt.Usage) bool {
	if u.RateLimit.Allowed == nil || !*u.RateLimit.Allowed || u.RateLimit.Reached {
		return false
	}
	for _, window := range []*chatgpt.Window{u.RateLimit.Primary, u.RateLimit.Secondary} {
		if window != nil && window.UsedPercent >= 100 {
			return false
		}
	}
	return true
}
