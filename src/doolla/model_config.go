package doolla

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// StreamingModel returns the complete assistant message, including tool calls.
// onText receives incremental text and can return an error to stop the request.
type StreamingModel interface {
	Model
	Stream(context.Context, []Message, []ToolSpec, func(string) error) (Message, error)
}

// ModelConfig configures generation and transport. MaxOutputTokens and
// OllamaContextSize use the provider defaults when zero. AzureToken, when
// supplied, is evaluated per request so long-lived processes can renew tokens.
type ModelConfig struct {
	Provider, BaseURL, Model, APIKey string
	HTTPClient                       *http.Client
	Temperature                      *float64
	MaxOutputTokens                  int
	// OpenAIUseLegacyMaxTokens sends max_tokens to Chat Completions endpoints
	// that do not support max_completion_tokens (including Ollama).
	OpenAIUseLegacyMaxTokens bool
	OpenAIOrganization       string
	GeminiGenerationConfig   map[string]any
	AzureAPIVersion          string
	AzureToken               func(context.Context) (string, error)
	OllamaContextSize        int
	OllamaOptions            map[string]any
}

type remoteModel struct {
	options                                   ModelConfig
	provider, baseURL, model, key, apiVersion string
	client                                    *http.Client
}

func firstValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// NewConfiguredModel selects a provider and uses its environment defaults for
// unset values. Azure's model argument is the deployment name.
func NewConfiguredModel(provider, baseURL, model, key string) (Model, error) {
	return NewModel(ModelConfig{Provider: provider, BaseURL: baseURL, Model: model, APIKey: key})
}

// NewModel configures a synchronous and streaming provider adapter.
func NewModel(config ModelConfig) (Model, error) {
	provider, baseURL, model, key := config.Provider, config.BaseURL, config.Model, config.APIKey
	if t := config.Temperature; t != nil {
		if math.IsNaN(*t) || *t < 0 || *t > 2 {
			return nil, errors.New("temperature must be between 0 and 2")
		}
		value := *t
		config.Temperature = &value
	}
	if config.MaxOutputTokens < 0 || config.OllamaContextSize < 0 {
		return nil, errors.New("token limits cannot be negative")
	}
	config.OllamaOptions = maps.Clone(config.OllamaOptions)
	config.GeminiGenerationConfig = maps.Clone(config.GeminiGenerationConfig)
	m := remoteModel{provider: strings.ToLower(strings.TrimSpace(provider)), client: config.HTTPClient, options: config}
	switch m.provider {
	case "mock":
		return &MockModel{}, nil
	case "", "openai", "openai-chat", "openai-responses", "openai_responses", "responses":
		if strings.Contains(m.provider, "responses") {
			m.provider = "responses"
		} else {
			m.provider = "openai"
		}
		m.baseURL = firstValue(baseURL, os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1")
		m.key = firstValue(key, os.Getenv("OPENAI_API_KEY"))
		m.model = firstValue(model, os.Getenv("OPENAI_MODEL"), "gpt-5")
	case "azure", "azureopenai", "azure-openai", "azure_openai":
		m.provider = "azure"
		m.baseURL = firstValue(baseURL, os.Getenv("AZURE_OPENAI_ENDPOINT"))
		m.model = firstValue(model, os.Getenv("AZURE_OPENAI_MODEL"))
		m.key = firstValue(key, os.Getenv("AZURE_OPENAI_API_KEY"))
		m.apiVersion = firstValue(config.AzureAPIVersion, os.Getenv("AZURE_OPENAI_API_VERSION"), "2024-10-21")
		if m.model == "" || m.baseURL == "" {
			return nil, errors.New("Azure OpenAI requires a deployment name and endpoint")
		}
		if config.AzureToken == nil && m.key == "" && os.Getenv("AZURE_OPENAI_AD_TOKEN") == "" {
			return nil, errors.New("Azure OpenAI requires an API key or AZURE_OPENAI_AD_TOKEN")
		}
	case "anthropic":
		m.baseURL = firstValue(baseURL, os.Getenv("ANTHROPIC_BASE_URL"), "https://api.anthropic.com")
		m.model = firstValue(model, os.Getenv("ANTHROPIC_MODEL"), "claude-sonnet-4-5")
		m.key = firstValue(key, os.Getenv("ANTHROPIC_API_KEY"))
	case "gemini", "google":
		m.provider = "gemini"
		m.baseURL = firstValue(baseURL, os.Getenv("GEMINI_BASE_URL"), "https://generativelanguage.googleapis.com/v1beta")
		m.model = strings.TrimPrefix(firstValue(model, os.Getenv("GEMINI_MODEL"), "gemini-2.5-pro"), "models/")
		m.key = firstValue(key, os.Getenv("GOOGLE_API_KEY"), os.Getenv("GEMINI_API_KEY"))
	case "ollama":
		if m.client == nil {
			m.client = &http.Client{Timeout: 240 * time.Second}
		}
		m.baseURL = firstValue(baseURL, os.Getenv("OLLAMA_HOST"), "http://localhost:11434")
		m.model = firstValue(model, os.Getenv("OLLAMA_MODEL"))
		m.key = firstValue(key, os.Getenv("OLLAMA_API_KEY"))
		if m.model == "" {
			return nil, errors.New("Ollama requires a model name")
		}
	default:
		return nil, fmt.Errorf("unknown model provider %q", provider)
	}
	if m.options.Temperature == nil && (m.provider == "anthropic" || m.provider == "gemini") {
		defaultTemperature := 0.7
		m.options.Temperature = &defaultTemperature
	}
	u, err := url.Parse(m.baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid %s model endpoint", m.provider)
	}
	m.baseURL = strings.TrimRight(m.baseURL, "/")
	return &m, nil
}

func (m *remoteModel) Reply(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
	return m.generate(ctx, messages, tools, false, nil)
}

func (m *remoteModel) Stream(ctx context.Context, messages []Message, tools []ToolSpec, onText func(string) error) (Message, error) {
	return m.generate(ctx, messages, tools, true, onText)
}

func (m *remoteModel) generate(ctx context.Context, messages []Message, tools []ToolSpec, stream bool, onText func(string) error) (Message, error) {
	switch m.provider {
	case "openai", "azure":
		return m.chatCompletion(ctx, messages, tools, stream, onText)
	case "responses":
		return m.responses(ctx, messages, tools, stream, onText)
	case "anthropic":
		return m.anthropic(ctx, messages, tools, stream, onText)
	case "gemini":
		return m.gemini(ctx, messages, tools, stream, onText)
	case "ollama":
		return m.ollama(ctx, messages, tools, stream, onText)
	default:
		return Message{}, fmt.Errorf("unknown provider %q", m.provider)
	}
}

// MockModel returns a configurable reply without network access. Configure
// Response or Replies before use; Replies repeats the last item when exhausted.
type MockModel struct {
	Response string
	Replies  []Message
	calls    atomic.Uint64
}

func (m *MockModel) reply(ctx context.Context, stream bool) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	count := m.calls.Add(1)
	if len(m.Replies) > 0 {
		reply := m.Replies[min(count-1, uint64(len(m.Replies)-1))]
		reply.Role = "assistant"
		return reply, nil
	}
	mode := "Request"
	if stream {
		mode = "Streamed"
	}
	return Message{Role: "assistant", FinishReason: "stop", Usage: &TokenUsage{Input: 50, Output: 20}, Content: fmt.Sprintf("%s (%s #%d)", firstValue(m.Response, "Hello! This is a mock response."), mode, count)}, nil
}

func (m *MockModel) ResetCallCount()   { m.calls.Store(0) }
func (m *MockModel) CallCount() uint64 { return m.calls.Load() }
func (m *MockModel) Reply(ctx context.Context, _ []Message, _ []ToolSpec) (Message, error) {
	return m.reply(ctx, false)
}
func (m *MockModel) Stream(ctx context.Context, _ []Message, _ []ToolSpec, onText func(string) error) (Message, error) {
	reply, err := m.reply(ctx, true)
	if err != nil {
		return Message{}, err
	}
	for word := range strings.SplitAfterSeq(reply.Content, " ") {
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		if word != "" && onText != nil {
			if err := onText(word); err != nil {
				return Message{}, err
			}
		}
	}
	return reply, nil
}

// ValidateModelTools reports provider-specific schema diagnostics without a
// network call. Custom models may supply their own validation method.
func ValidateModelTools(model Model, tools []ToolSpec) []string {
	if v, ok := model.(interface{ ValidateTools([]ToolSpec) []string }); ok {
		return v.ValidateTools(tools)
	}
	return nil
}

func (m OpenAIModel) ValidateTools(tools []ToolSpec) []string { return m.remote().ValidateTools(tools) }
func (m *remoteModel) ValidateTools(tools []ToolSpec) []string {
	if m.provider == "responses" {
		return nil
	}
	var issues []string
	for _, tool := range tools {
		if tool.Name == "" {
			issues = append(issues, "tool name is required")
		}
		if (m.provider == "openai" || m.provider == "azure") && len([]rune(tool.Name)) > 64 {
			issues = append(issues, fmt.Sprintf("tool name exceeds 64 characters: %q", tool.Name))
		}
		if (m.provider == "gemini" || m.provider == "ollama") && tool.Description == "" {
			issues = append(issues, fmt.Sprintf("tool %q requires a description", tool.Name))
		}
	}
	return issues
}
