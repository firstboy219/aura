package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aura.local/core/internal/config"
)

// OpenRouter is the P1 provider adapter. The key lives only here.
type OpenRouter struct {
	baseURL string
	key     string
	client  *http.Client
}

func NewOpenRouter(baseURL, key string, timeout time.Duration) *OpenRouter {
	return &OpenRouter{baseURL: strings.TrimRight(baseURL, "/"), key: key,
		client: &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true}}}
}

func (o *OpenRouter) ID() string { return "openrouter" }

type orRequest struct {
	Model          string         `json:"model"`
	Messages       []Message      `json:"messages"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	ResponseFormat any            `json:"response_format,omitempty"`
	Reasoning      map[string]any `json:"reasoning,omitempty"`
	Usage          map[string]any `json:"usage"`
	Provider       map[string]any `json:"provider,omitempty"`
}

type orResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int      `json:"prompt_tokens"`
		CompletionTokens    int      `json:"completion_tokens"`
		Cost                *float64 `json:"cost"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete performs one chat-completions attempt with native structured output.
func (o *OpenRouter) Complete(ctx context.Context, route config.ModelRoute, req Request) (*Attempt, error) {
	body := orRequest{Model: route.RouteID, Messages: req.Messages, MaxTokens: req.MaxOutputTokens,
		Usage: map[string]any{"include": true}}
	if len(req.ResponseSchema) > 0 {
		body.ResponseFormat = map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": "aura_response", "strict": true, "schema": json.RawMessage(req.ResponseSchema)}}
		body.Provider = map[string]any{"require_parameters": true}
	}
	if route.ReasoningEffort != "" {
		body.Reasoning = map[string]any{"effort": route.ReasoningEffort, "exclude": true}
	}
	b, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Authorization", "Bearer "+o.key)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("X-Title", "AURA")
	resp, err := o.client.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ProviderError{Status: 0, Transient: true, Msg: "transport: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &ProviderError{Status: resp.StatusCode, Transient: true, Msg: "read body: " + err.Error()}
	}
	var out orResponse
	jerr := json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || out.Error != nil {
		msg := strconv.Itoa(resp.StatusCode)
		if out.Error != nil {
			msg = out.Error.Message
		}
		return nil, &ProviderError{Status: resp.StatusCode, Transient: transient(resp.StatusCode), Msg: truncate(msg, 300)}
	}
	if jerr != nil || len(out.Choices) == 0 {
		return nil, &ProviderError{Status: resp.StatusCode, Transient: true, Msg: "malformed provider response"}
	}
	at := &Attempt{Content: out.Choices[0].Message.Content, FinishReason: out.Choices[0].FinishReason, RequestID: out.ID,
		InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens,
		ReasoningTokens: out.Usage.CompletionTokensDetails.ReasoningTokens, CostUSD: out.Usage.Cost,
		CacheHit: out.Usage.PromptTokensDetails.CachedTokens > 0}
	return at, nil
}

func transient(status int) bool {
	return status == 408 || status == 409 || status == 425 || status == 429 || status >= 500
}

// ModelInfo is the registry metadata for one route as published by OpenRouter.
type ModelInfo struct {
	RouteID          string
	ContextTokens    int
	PriceInPerMTok   float64
	PriceOutPerMTok  float64
	StructuredOutput bool
	Reasoning        bool
}

// ListModels fetches the public model catalog (no key, no spend). Registry
// metadata never grants authority (Blueprint §8.5.1).
func (o *OpenRouter) ListModels(ctx context.Context) (map[string]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models: HTTP %d", resp.StatusCode)
	}
	var cat struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&cat); err != nil {
		return nil, err
	}
	out := map[string]ModelInfo{}
	for _, m := range cat.Data {
		in, _ := strconv.ParseFloat(m.Pricing.Prompt, 64)
		outp, _ := strconv.ParseFloat(m.Pricing.Completion, 64)
		mi := ModelInfo{RouteID: m.ID, ContextTokens: m.ContextLength, PriceInPerMTok: in * 1e6, PriceOutPerMTok: outp * 1e6}
		for _, p := range m.SupportedParameters {
			switch p {
			case "structured_outputs":
				mi.StructuredOutput = true
			case "reasoning":
				mi.Reasoning = true
			}
		}
		out[m.ID] = mi
	}
	return out, nil
}
