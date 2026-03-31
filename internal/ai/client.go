package ai

import (
	"bytes"
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
}

var ProviderDefaultModels = map[string]string{
	"openai":   "gpt-4o",
	"deepseek": "deepseek-chat",
	"ollama":   "qwen2.5:14b",
	"qwen":     "qwen-plus",
	"kimi":     "kimi-k2.5",
	"minimax":  "MiniMax-M2.5",
}

// ── Model classification ──

var pureReasoningKeywords = []string{
	"o1", "o3", "o4",
	"deepseek-reasoner",
	"-r1",
	"kimi-k2-thinking",
}

var hybridThinkingKeywords = []string{
	"qwen3.5-plus", "qwen3.5-flash", "qwen3.5",
	"qwen-max", "qwen-plus", "qwen3",
	"kimi-k2.5",
	"minimax",
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
	BaseURL    string
	APIKey     string
	Model      string
	Provider   string
	HTTPClient *http.Client
}

// NewClient creates an API client.
func NewClient(provider, apiKey, baseURL, model string, timeout float64) *Client {
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
		BaseURL:  resolvedURL,
		APIKey:   resolvedKey,
		Model:    resolvedModel,
		Provider: provider,
		HTTPClient: &http.Client{
			Timeout: time.Duration(timeout * float64(time.Second)),
		},
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
	useThinking := false
	if hybrid && enableThinking != nil && *enableThinking {
		useThinking = true
	} else if pureR {
		useThinking = true
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
		case "kimi":
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
			case "kimi":
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

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
