package llmopenai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/acme/ai-assistant/pkg/llm"
)

const maxErrorBody = 32 << 10

type Config struct {
	BaseURL          string
	APIKey           string
	MaxFrameBytes    int
	MaxResponseBytes int
}

type Provider struct {
	client           *http.Client
	endpoint         string
	apiKey           string
	maxFrameBytes    int
	maxResponseBytes int
}

func New(client *http.Client, cfg Config) (*Provider, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("LLM_BASE_URL is required")
	}
	if client == nil {
		return nil, errors.New("http client is required")
	}
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = 1 << 20
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 2 << 20
	}
	return &Provider{
		client:           client,
		endpoint:         baseURL + "/chat/completions",
		apiKey:           cfg.APIKey,
		maxFrameBytes:    cfg.MaxFrameBytes,
		maxResponseBytes: cfg.MaxResponseBytes,
	}, nil
}

type completionRequest struct {
	Model         string        `json:"model"`
	Messages      []llm.Message `json:"messages"`
	Stream        bool          `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type completionChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (p *Provider) ChatStream(ctx context.Context, req llm.ChatRequest, onChunk llm.ChunkHandler) error {
	body := completionRequest{Model: req.Model, Messages: req.Messages, Stream: true}
	body.StreamOptions.IncludeUsage = true
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode completion request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create completion request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: %v", llm.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: upstream status %d", llm.ErrUnauthorized, resp.StatusCode)
		case http.StatusTooManyRequests:
			return fmt.Errorf("%w: upstream status %d", llm.ErrRateLimited, resp.StatusCode)
		default:
			return fmt.Errorf("%w: upstream status %d", llm.ErrUnavailable, resp.StatusCode)
		}
	}

	return p.consumeSSE(resp.Body, onChunk)
}

func (p *Provider) consumeSSE(reader io.Reader, onChunk llm.ChunkHandler) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), p.maxFrameBytes)
	total := 0
	seenDone := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			seenDone = true
			break
		}
		var upstream completionChunk
		if err := json.Unmarshal([]byte(payload), &upstream); err != nil {
			return fmt.Errorf("%w: decode SSE data: %v", llm.ErrProtocol, err)
		}
		chunk := llm.Chunk{}
		for _, choice := range upstream.Choices {
			chunk.Content += choice.Delta.Content
			if choice.FinishReason != nil {
				chunk.FinishReason = *choice.FinishReason
			}
		}
		if upstream.Usage != nil {
			chunk.Usage = &llm.Usage{
				PromptTokens:     upstream.Usage.PromptTokens,
				CompletionTokens: upstream.Usage.CompletionTokens,
				TotalTokens:      upstream.Usage.TotalTokens,
			}
		}
		total += len(chunk.Content)
		if total > p.maxResponseBytes {
			return llm.ErrResponseTooLarge
		}
		if chunk.Content != "" || chunk.FinishReason != "" || chunk.Usage != nil {
			if err := onChunk(chunk); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: read SSE: %v", llm.ErrProtocol, err)
	}
	if !seenDone {
		return fmt.Errorf("%w: stream ended without [DONE]", llm.ErrProtocol)
	}
	return nil
}
