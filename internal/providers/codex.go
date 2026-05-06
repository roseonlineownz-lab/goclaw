package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CodexProvider implements Provider for the OpenAI Responses API,
// used with ChatGPT subscription via OAuth (Codex flow).
// Wire format: POST /codex/responses on chatgpt.com backend.
type CodexProvider struct {
	name            string
	apiBase         string // e.g. "https://api.openai.com/v1" or "https://chatgpt.com/backend-api"
	defaultModel    string
	client          *http.Client
	retryConfig     RetryConfig
	tokenSource     TokenSource
	routingDefaults *CodexRoutingDefaults
}

// NewCodexProvider creates a provider for the OpenAI Responses API with OAuth token.
func NewCodexProvider(name string, tokenSource TokenSource, apiBase, defaultModel string) *CodexProvider {
	if apiBase == "" {
		apiBase = "https://chatgpt.com/backend-api"
	}
	apiBase = strings.TrimRight(apiBase, "/")

	if defaultModel == "" {
		defaultModel = "gpt-5.4"
	}

	return &CodexProvider{
		name:         name,
		apiBase:      apiBase,
		defaultModel: defaultModel,
		client:       &http.Client{Timeout: 300 * time.Second},
		retryConfig:  DefaultRetryConfig(),
		tokenSource:  tokenSource,
	}
}

func (p *CodexProvider) Name() string           { return p.name }
func (p *CodexProvider) DefaultModel() string   { return p.defaultModel }
func (p *CodexProvider) SupportsThinking() bool { return true }

// CodexRoutingDefaults describes provider-level defaults for ChatGPT OAuth routing.
type CodexRoutingDefaults struct {
	Strategy           string
	ExtraProviderNames []string
}

// RoutingDefaults returns the configured ChatGPT OAuth routing defaults, or nil
// when no defaults are set.
func (p *CodexProvider) RoutingDefaults() *CodexRoutingDefaults {
	if p == nil || p.routingDefaults == nil {
		return nil
	}
	return &CodexRoutingDefaults{
		Strategy:           p.routingDefaults.Strategy,
		ExtraProviderNames: append([]string(nil), p.routingDefaults.ExtraProviderNames...),
	}
}

func (p *CodexProvider) WithRoutingDefaults(strategy string, extraProviderNames []string) *CodexProvider {
	if p == nil {
		return nil
	}
	strategy = strings.TrimSpace(strategy)
	switch strategy {
	case "", "manual":
		strategy = chatGPTOAuthStrategyPriorityOrder
	case chatGPTOAuthStrategyRoundRobin, chatGPTOAuthStrategyPriorityOrder:
	default:
		strategy = chatGPTOAuthStrategyPriorityOrder
	}
	names := make([]string, 0, len(extraProviderNames))
	seen := make(map[string]bool, len(extraProviderNames))
	for _, name := range extraProviderNames {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 && strategy == chatGPTOAuthStrategyPriorityOrder {
		p.routingDefaults = nil
		return p
	}
	p.routingDefaults = &CodexRoutingDefaults{
		Strategy:           strategy,
		ExtraProviderNames: names,
	}
	return p
}

func (p *CodexProvider) RouteEligibility(ctx context.Context) RouteEligibility {
	if p == nil || p.tokenSource == nil {
		return RouteEligibility{Class: RouteEligibilityUnknown, Reason: "missing_token_source"}
	}
	if aware, ok := p.tokenSource.(RouteEligibilityAware); ok {
		eligibility := aware.RouteEligibility(ctx)
		if eligibility.Class == "" {
			eligibility.Class = RouteEligibilityUnknown
		}
		return eligibility
	}
	return RouteEligibility{Class: RouteEligibilityHealthy}
}

func (p *CodexProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Codex Responses API requires stream=true; delegate to ChatStream with no chunk handler.
	return p.ChatStream(ctx, req, nil)
}

func (p *CodexProvider) ChatStream(ctx context.Context, req ChatRequest, onChunk func(StreamChunk)) (*ChatResponse, error) {
	body := p.buildRequestBody(req, true)

	respBody, err := RetryDo(ctx, p.retryConfig, func() (io.ReadCloser, error) {
		return p.doRequest(ctx, body)
	})
	if err != nil {
		return nil, err
	}
	defer respBody.Close()

	result := &ChatResponse{FinishReason: "stop"}
	toolCalls := make(map[string]*codexToolCallAcc) // keyed by item_id
	messageState := newCodexMessageStreamState()

	scanner := bufio.NewScanner(respBody)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimPrefix(data, " ")
		if data == "[DONE]" {
			break
		}

		var event codexSSEEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		p.processSSEEvent(&event, result, toolCalls, messageState, onChunk)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: stream read error: %w", p.name, err)
	}

	// Build tool calls from accumulators
	for _, acc := range toolCalls {
		if acc.name == "" {
			continue
		}
		args := make(map[string]interface{})
		_ = json.Unmarshal([]byte(acc.rawArgs), &args)
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:        acc.callID,
			Name:      acc.name,
			Arguments: args,
		})
	}

	if len(result.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
	}

	if onChunk != nil {
		onChunk(StreamChunk{Done: true})
	}

	return result, nil
}

// processSSEEvent handles a single SSE event during streaming.
func (p *CodexProvider) processSSEEvent(event *codexSSEEvent, result *ChatResponse, toolCalls map[string]*codexToolCallAcc, messageState *codexMessageStreamState, onChunk func(StreamChunk)) {
	switch event.Type {
	case "response.output_item.added":
		if messageState != nil && event.Item != nil {
			messageState.registerMessageItem(codexEventItemKey(event.ItemID, event.Item), event.OutputIndex, event.Item)
			messageState.updateResultPhase(result)
		}

	case "response.output_text.delta":
		if messageState != nil {
			messageState.recordTextDelta(event.ItemID, event.OutputIndex, event.ContentIndex, event.Delta, result, onChunk)
		} else if event.Delta != "" {
			appendCodexContent(result, event.Delta, onChunk)
		}

	case "response.output_text.done":
		if messageState != nil {
			messageState.recordFinalText(event.ItemID, event.OutputIndex, event.ContentIndex, event.Text, result, onChunk)
		} else if event.Text != "" {
			appendCodexContent(result, event.Text, onChunk)
		}

	case "response.content_part.done":
		if event.Part != nil && event.Part.Type == "output_text" {
			if messageState != nil {
				messageState.recordFinalText(event.ItemID, event.OutputIndex, event.ContentIndex, event.Part.Text, result, onChunk)
			} else if event.Part.Text != "" {
				appendCodexContent(result, event.Part.Text, onChunk)
			}
		}

	case "response.function_call_arguments.delta":
		if event.ItemID != "" {
			acc := toolCalls[event.ItemID]
			if acc == nil {
				acc = &codexToolCallAcc{}
				toolCalls[event.ItemID] = acc
			}
			acc.rawArgs += event.Delta
		}

	case "response.output_item.done":
		if event.Item != nil {
			switch event.Item.Type {
			case "message":
				if messageState != nil {
					messageState.registerMessageItem(codexEventItemKey(event.ItemID, event.Item), event.OutputIndex, event.Item)
					messageState.flushMessage(codexEventItemKey(event.ItemID, event.Item), result, onChunk)
					messageState.updateResultPhase(result)
				}
				if event.Item.Phase != "" {
					result.Phase = event.Item.Phase
				}
			case "function_call":
				acc := toolCalls[event.Item.ID]
				if acc == nil {
					acc = &codexToolCallAcc{}
				}
				acc.callID = event.Item.CallID
				acc.name = event.Item.Name
				if event.Item.Arguments != "" {
					acc.rawArgs = event.Item.Arguments
				}
				toolCalls[event.Item.ID] = acc
			case "reasoning":
				for _, s := range event.Item.Summary {
					if s.Text != "" {
						result.Thinking += s.Text
						if onChunk != nil {
							onChunk(StreamChunk{Thinking: s.Text})
						}
					}
				}
			}
		}

	case "response.completed", "response.incomplete", "response.failed":
		if event.Response != nil {
			if event.Response.Usage != nil {
				u := event.Response.Usage
				result.Usage = &Usage{
					PromptTokens:     u.InputTokens,
					CompletionTokens: u.OutputTokens,
					TotalTokens:      u.TotalTokens,
				}
				if u.OutputTokensDetails != nil {
					result.Usage.ThinkingTokens = u.OutputTokensDetails.ReasoningTokens
				}
			}
			if event.Response.Status == "incomplete" {
				result.FinishReason = "length"
			}
			if messageState != nil {
				messageState.ingestCompletedResponse(event.Response)
				messageState.flushCompletedResponse(result, onChunk)
				messageState.updateResultPhase(result)
			}
		}
	}
}
