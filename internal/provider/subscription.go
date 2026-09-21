package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/divyangchauhan/DiffVouch/internal/chatgpt"
	"github.com/divyangchauhan/DiffVouch/internal/dv"
)

const subscriptionEndpoint = "https://chatgpt.com/backend-api/codex/responses"

func (a *apiAdapter) postSubscription(ctx context.Context, body any) (openAIResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return openAIResponse{}, dv.Wrap(dv.ExitProvider, "encode subscription request", err)
	}
	session, err := chatgpt.Credentials(ctx, a.client, "")
	if err != nil {
		return openAIResponse{}, dv.Wrap(dv.ExitProvider, "ChatGPT subscription authentication", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if a.beforeCall != nil {
			if err := a.beforeCall(ctx); err != nil {
				return openAIResponse{}, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, subscriptionEndpoint, bytes.NewReader(raw))
		if err != nil {
			return openAIResponse{}, err
		}
		req.Header.Set("Authorization", "Bearer "+session.AccessToken)
		req.Header.Set("ChatGPT-Account-ID", session.AccountID)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("User-Agent", "diffvouch")
		req.Header.Set("originator", "diffvouch")
		client := a.client
		if client == nil {
			client = http.DefaultClient
		}
		safeClient := *client
		safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := safeClient.Do(req)
		if err != nil {
			return openAIResponse{}, dv.New(dv.ExitProvider, "ChatGPT subscription request failed")
		}
		if response.StatusCode == http.StatusUnauthorized && attempt == 0 {
			response.Body.Close()
			session, err = chatgpt.Credentials(ctx, a.client, session.AccessToken)
			if err != nil {
				return openAIResponse{}, dv.Wrap(dv.ExitProvider, "refresh ChatGPT subscription", err)
			}
			continue
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusTooManyRequests {
			return openAIResponse{}, dv.Wrap(dv.ExitProvider, "ChatGPT subscription rate or usage limit reached (no API fallback)", chatgpt.ErrAllowance)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return openAIResponse{}, dv.New(dv.ExitProvider, fmt.Sprintf("ChatGPT subscription returned HTTP %d; check subscription access and the selected model", response.StatusCode))
		}
		// Live subscription responses can omit Content-Type. The parser still
		// requires valid SSE events and explicit completion before accepting output.
		contentType := strings.ToLower(response.Header.Get("Content-Type"))
		if contentType != "" && !strings.HasPrefix(contentType, "text/event-stream") {
			return openAIResponse{}, dv.New(dv.ExitProvider, "ChatGPT subscription returned a non-streaming response")
		}
		return readSubscriptionStream(response.Body)
	}
	return openAIResponse{}, dv.New(dv.ExitProvider, "ChatGPT subscription authentication failed")
}

// Retain completed output items, including opaque reasoning content, for the
// next tool round. Never accept a partial response after a dropped connection.
func readSubscriptionStream(reader io.Reader) (openAIResponse, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, (64<<20)+1))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	var data strings.Builder
	items := map[int]json.RawMessage{}
	itemBytes := 0
	consume := func() (*openAIResponse, error) {
		if data.Len() == 0 {
			return nil, nil
		}
		raw := data.String()
		data.Reset()
		if strings.TrimSpace(raw) == "[DONE]" {
			return nil, nil
		}
		var event struct {
			Type     string          `json:"type"`
			Index    int             `json:"output_index"`
			Item     json.RawMessage `json:"item"`
			Response openAIResponse  `json:"response"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, dv.New(dv.ExitProvider, "ChatGPT subscription returned invalid event JSON")
		}
		switch event.Type {
		case "response.output_item.done":
			if len(event.Item) == 0 || event.Index < 0 || items[event.Index] != nil {
				return nil, dv.New(dv.ExitProvider, "ChatGPT subscription returned an invalid output item")
			}
			itemBytes += len(event.Item)
			if itemBytes > 4<<20 {
				return nil, dv.New(dv.ExitProvider, "ChatGPT subscription output exceeded 4 MiB")
			}
			items[event.Index] = event.Item
		case "response.completed":
			result := event.Response
			if result.Status != "" && result.Status != "completed" {
				return nil, dv.New(dv.ExitProvider, "ChatGPT subscription response did not complete")
			}
			result.Status = "completed"
			if len(result.Output) == 0 {
				indices := make([]int, 0, len(items))
				for index := range items {
					indices = append(indices, index)
				}
				sort.Ints(indices)
				for _, index := range indices {
					result.Output = append(result.Output, items[index])
				}
			}
			return &result, nil
		case "response.failed", "response.incomplete", "error":
			return nil, dv.New(dv.ExitProvider, "ChatGPT subscription response failed or was incomplete")
		}
		return nil, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			result, err := consume()
			if err != nil {
				return openAIResponse{}, err
			}
			if result != nil {
				return *result, nil
			}
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			if data.Len()+len(value)+1 > 4<<20 {
				return openAIResponse{}, dv.New(dv.ExitProvider, "ChatGPT subscription event exceeded 4 MiB")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if scanner.Err() != nil {
		return openAIResponse{}, dv.New(dv.ExitProvider, "read ChatGPT subscription stream failed")
	}
	// Some servers omit the trailing blank line on their final event.
	result, err := consume()
	if err != nil {
		return openAIResponse{}, err
	}
	if result != nil {
		return *result, nil
	}
	return openAIResponse{}, dv.New(dv.ExitProvider, "ChatGPT subscription stream ended before response.completed")
}
