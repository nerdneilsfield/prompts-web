package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxModelResponse caps how much will be read from upstream. A runaway or
// hostile response must not be able to exhaust this process.
const maxModelResponse = 4 << 20

// Error kinds reported to the client. This is a closed vocabulary so the
// frontend can branch on it.
const (
	ErrKindAuth      = "auth"       // 401/403 upstream: key or access problem
	ErrKindRateLimit = "rate_limit" // 429 upstream
	ErrKindUpstream  = "upstream"   // any other non-2xx, or transport failure
	ErrKindInvalid   = "invalid"    // response was not usable JSON text
	ErrKindTimeout   = "timeout"    // deadline hit
	ErrKindCanceled  = "canceled"   // client disconnected
)

// classify maps a transport-level failure onto a kind. A canceled parent context
// means the client hung up, which is not the same user-visible event as the
// per-request deadline expiring.
func classify(parent context.Context, err error) string {
	if errors.Is(parent.Err(), context.Canceled) {
		return ErrKindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(parent.Err(), context.DeadlineExceeded) {
		return ErrKindTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrKindCanceled
	}
	return ErrKindUpstream
}

func newModelClient() *http.Client {
	return &http.Client{
		// No client timeout: the per-request context owns the deadline.
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

type modelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string         `json:"model"`
	Messages    []modelMessage `json:"messages"`
	Temperature float64        `json:"temperature"`
	TopP        float64        `json:"top_p"`
	MaxTokens   int            `json:"max_tokens"`
	Stream      bool           `json:"stream"`
}

// chatResponse decodes only the fields this site needs.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// runParams is the sampling configuration recorded with a run. It mirrors the
// non-message fields of chatRequest, so the record cannot drift from what was
// sent.
type runParams struct {
	Temperature float64 `json:"temperature"`
	TopP        float64 `json:"top_p"`
	MaxTokens   int     `json:"max_tokens"`
	Stream      bool    `json:"stream"`
}

// buildRequest renders the upstream body and the parameter record for it. The
// message text is not part of the record: the input and the scheme snapshot have
// their own columns. Neither value can fail to encode.
func (c *Client) buildRequest(input, scheme string) ([]byte, json.RawMessage) {
	req := chatRequest{
		Model: c.cfg.Model,
		Messages: []modelMessage{
			{Role: "system", Content: PreserveInstruction},
			{Role: "user", Content: scheme + "\n\n--- 原文 ---\n" + input},
		},
		Temperature: c.cfg.Temperature,
		TopP:        c.cfg.TopP,
		MaxTokens:   c.cfg.MaxTokens,
		Stream:      false,
	}
	body, err := json.Marshal(req)
	if err != nil {
		// Unreachable: every field is a string, number or bool.
		panic("encode model request: " + err.Error())
	}
	params, err := json.Marshal(runParams{Temperature: req.Temperature, TopP: req.TopP, MaxTokens: req.MaxTokens, Stream: req.Stream})
	if err != nil {
		panic("encode run params: " + err.Error())
	}
	return body, params
}

// optimize performs the single upstream call. It returns the assistant text and
// an empty kind on success, or an empty text and one of the ErrKind* values on
// failure. It does not retry: a failed run stays failed and the client keeps its
// input. The transport error itself is deliberately dropped: it can quote the
// upstream response, and nothing here may log or return that.
func (c *Client) optimize(ctx context.Context, body []byte) (string, string) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", ErrKindUpstream
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", classify(ctx, err)
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", ErrKindAuth
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", ErrKindRateLimit
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return "", ErrKindUpstream
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelResponse+1))
	if err != nil {
		return "", classify(ctx, err)
	}
	if len(raw) > maxModelResponse {
		return "", ErrKindInvalid
	}

	var decoded chatResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", ErrKindInvalid
	}
	if len(decoded.Choices) == 0 {
		return "", ErrKindInvalid
	}
	text := decoded.Choices[0].Message.Content
	if strings.TrimSpace(text) == "" {
		return "", ErrKindInvalid
	}
	return text, ""
}
