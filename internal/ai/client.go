package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ── Provider defaults ──

var ProviderBaseURLs = map[string]string{
	"openai":    "https://api.openai.com/v1",
	"deepseek":  "https://api.deepseek.com/v1",
	"ollama":    "http://localhost:11434/v1",
	"qwen":      "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"qwen-intl": "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
	"kimi":      "https://api.moonshot.cn/v1",
	"minimax":   "https://api.minimaxi.com/v1",
	"zhipu":     "https://open.bigmodel.cn/api/paas/v4",
}

var ProviderDefaultModels = map[string]string{
	"openai":   "gpt-4o",
	"deepseek": "deepseek-chat",
	"ollama":   "qwen2.5:14b",
	"qwen":     "qwen-plus",
	"kimi":     "kimi-k2.5",
	"minimax":  "MiniMax-M2.5",
	"zhipu":    "glm-5",
}

// ── Model classification ──

var pureReasoningKeywords = []string{
	"o1", "o3", "o4",
	"deepseek-reasoner",
	"-r1",
	"kimi-k2-thinking",
}

var hybridThinkingKeywords = []string{
	"qwen3.6-plus", "qwen3.6-flash", "qwen3.6",
	"qwen3.5-plus", "qwen3.5-flash", "qwen3.5",
	"qwen-max", "qwen-plus", "qwen3",
	"kimi-k2.5",
	"minimax",
	"glm-5", "glm-4.7",
}

// IsReasoningModel returns true for pure reasoning models (o1/o3/R1 etc).
func IsReasoningModel(model string) bool {
	m := strings.ToLower(model)
	for _, kw := range pureReasoningKeywords {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

// IsHybridThinkingModel returns true for models with an enable_thinking switch.
func IsHybridThinkingModel(model string) bool {
	m := strings.ToLower(model)
	for _, kw := range hybridThinkingKeywords {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

// ── Client ──

// Client wraps an OpenAI-compatible API endpoint.
type Client struct {
	BaseURL           string
	APIKey            string
	Model             string
	Provider          string
	HTTPClient        *http.Client
	StreamHTTPClient  *http.Client // no Timeout; uses context instead
	EnableThinking    *bool        // nil=auto, true=force on, false=force off
}

// NewClient creates an API client.
func NewClient(provider, apiKey, baseURL, model string, timeout float64, enableThinking *bool) *Client {
	provider = strings.ToLower(strings.TrimSpace(provider))

	resolvedURL := baseURL
	if resolvedURL == "" {
		resolvedURL = ProviderBaseURLs[provider]
	}
	resolvedModel := model
	if resolvedModel == "" {
		resolvedModel = ProviderDefaultModels[provider]
	}
	resolvedKey := apiKey
	if resolvedKey == "" {
		if provider == "ollama" {
			resolvedKey = "ollama"
		} else {
			resolvedKey = "sk-placeholder"
		}
	}

	return &Client{
		BaseURL:          resolvedURL,
		APIKey:           resolvedKey,
		Model:            resolvedModel,
		Provider:         provider,
		HTTPClient:       &http.Client{Timeout: time.Duration(timeout * float64(time.Second))},
		StreamHTTPClient: &http.Client{}, // no Timeout for SSE; relies on context
		EnableThinking:   enableThinking,
	}
}

// ChatMessage represents a single message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the request body for /chat/completions.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	// For reasoning models
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
	// Qwen thinking
	EnableThinking *bool `json:"enable_thinking,omitempty"`
}

// ChatResponse represents the API response.
type ChatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// ChatCompletion sends a chat completion request and returns the response.
func (c *Client) ChatCompletion(messages []ChatMessage, temperature float64, maxTokens int, enableThinking *bool) (*ChatResponse, error) {
	m := strings.ToLower(c.Model)
	pureR := IsReasoningModel(c.Model)
	hybrid := IsHybridThinkingModel(c.Model)

	// Determine thinking mode: config override > param > auto-detect
	useThinking := false
	if pureR {
		useThinking = true
	} else if hybrid {
		// Priority: c.EnableThinking (config) > enableThinking (param) > auto (true for hybrid)
		if c.EnableThinking != nil {
			useThinking = *c.EnableThinking
		} else if enableThinking != nil {
			useThinking = *enableThinking
		} else {
			useThinking = true
		}
	}

	// Adapt messages for reasoning models (o1/o3 don't support system role)
	msgs := messages
	if pureR && !strings.Contains(m, "deepseek") && !strings.Contains(m, "-r1") {
		msgs = adaptMessagesForReasoning(messages)
	}

	// Build request body as a map for flexible fields
	body := map[string]any{
		"model":    c.Model,
		"messages": msgs,
	}

	if pureR {
		if strings.Contains(m, "deepseek") || strings.Contains(m, "-r1") {
			body["temperature"] = 1
			body["max_tokens"] = maxTokens
		} else {
			body["max_completion_tokens"] = maxTokens
		}
	} else if hybrid && useThinking {
		body["temperature"] = 1
		body["max_tokens"] = maxTokens
		switch c.Provider {
		case "kimi", "zhipu":
			body["thinking"] = map[string]string{"type": "enabled"}
		case "minimax":
			body["reasoning_split"] = true
		default:
			body["enable_thinking"] = true
		}
	} else {
		body["temperature"] = temperature
		body["max_tokens"] = maxTokens
		if hybrid {
			switch c.Provider {
			case "kimi", "zhipu":
				body["thinking"] = map[string]string{"type": "disabled"}
			case "minimax":
				body["reasoning_split"] = false
			default:
				body["enable_thinking"] = false
			}
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, truncStr(string(respBody), 300))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &chatResp, nil
}

// ExtractResponseText gets (content, reasoning_content) from a response.
func ExtractResponseText(resp *ChatResponse) (content, reasoning string) {
	if len(resp.Choices) == 0 {
		return "", ""
	}
	msg := resp.Choices[0].Message
	content = strings.TrimSpace(msg.Content)
	reasoning = strings.TrimSpace(msg.ReasoningContent)
	return
}

func adaptMessagesForReasoning(messages []ChatMessage) []ChatMessage {
	var systemParts []string
	var userMsgs []ChatMessage
	for _, m := range messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.Content)
		} else {
			userMsgs = append(userMsgs, m)
		}
	}
	if len(systemParts) == 0 {
		return userMsgs
	}
	if len(userMsgs) > 0 && userMsgs[0].Role == "user" {
		prefix := strings.Join(systemParts, "\n\n")
		userMsgs[0].Content = prefix + "\n\n" + userMsgs[0].Content
	}
	return userMsgs
}

// StreamChunk represents a single chunk of streamed output.
type StreamChunk struct {
	Content          string
	ReasoningContent string
	Done             bool
	Truncated        bool  // true when finish_reason=length (output cut off by max_tokens)
	Err              error
}

// ChatCompletionStream sends a chat completion request with streaming enabled.
// It returns a channel that receives chunks as they arrive. The channel is
// closed when the stream ends (either successfully or on error).
func (c *Client) ChatCompletionStream(ctx context.Context, messages []ChatMessage, temperature float64, maxTokens int) (<-chan StreamChunk, error) {
	// Build request body
	body := map[string]any{
		"model":    c.Model,
		"messages": messages,
		"stream":   true,
	}

	pureR := IsReasoningModel(c.Model)
	hybrid := IsHybridThinkingModel(c.Model)

	// Determine thinking mode: config override > auto-detect
	useThinking := false
	if pureR {
		useThinking = true
	} else if hybrid {
		if c.EnableThinking != nil {
			useThinking = *c.EnableThinking
		} else {
			useThinking = true
		}
	}

	if pureR {
		body["max_tokens"] = maxTokens
	} else if hybrid && useThinking {
		body["temperature"] = 1
		body["max_tokens"] = maxTokens
		switch c.Provider {
		case "kimi", "zhipu":
			body["thinking"] = map[string]string{"type": "enabled"}
		case "minimax":
			body["reasoning_split"] = true
		default:
			body["enable_thinking"] = true
		}
	} else {
		body["temperature"] = temperature
		body["max_tokens"] = maxTokens
		if hybrid {
			switch c.Provider {
			case "kimi", "zhipu":
				body["thinking"] = map[string]string{"type": "disabled"}
			case "minimax":
				body["reasoning_split"] = false
			default:
				body["enable_thinking"] = false
			}
		}
	}

	// stream_options for providers that support it (qwen/dashscope)
	if hybrid && useThinking && c.Provider == "qwen" {
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.StreamHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, truncStr(string(respBody), 300))
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		// Increase scanner buffer for long lines
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			// SSE lines start with "data: "
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				ch <- StreamChunk{Done: true}
				return
			}

			var sse struct {
				Choices []struct {
					Delta struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &sse); err != nil {
				continue // skip malformed chunks
			}
			if len(sse.Choices) > 0 {
				delta := sse.Choices[0].Delta
				if delta.Content != "" || delta.ReasoningContent != "" {
					select {
					case ch <- StreamChunk{Content: delta.Content, ReasoningContent: delta.ReasoningContent}:
					case <-ctx.Done():
						ch <- StreamChunk{Done: true}
						return
					}
				}
				// finish_reason=length 说明被 max_tokens 截断
				if sse.Choices[0].FinishReason == "length" {
					ch <- StreamChunk{Done: true, Truncated: true}
					return
				}
			}
		}

		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			ch <- StreamChunk{Err: err}
		} else {
			ch <- StreamChunk{Done: true}
		}
	}()

	return ch, nil
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
