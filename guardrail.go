// Package guardrail is a built-in Bifrost HTTP transport plugin that injects a
// governance system prompt into inference requests and blocks requests whose user
// input matches configured regex patterns.
package guardrail

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// PluginName is the registry name used in config.json (no path = built-in).
const PluginName = "guardrail"

const defaultBlockMessage = "Request blocked by guardrail policy."

// Config is the JSON object under the plugin entry in config.json.
type Config struct {
	// SystemPrompt is injected into every inference request.
	SystemPrompt string `json:"system_prompt"`
	// SystemMode: "prepend" (default) keeps any caller system prompt below ours;
	// "override" replaces it entirely.
	SystemMode string `json:"system_mode"`
	// BlockPatterns are regexes; if any matches the user input the request is blocked.
	BlockPatterns []string `json:"block_patterns"`
	// BlockMessage is returned (HTTP 403) when a block pattern matches.
	BlockMessage string `json:"block_message"`
}

// Plugin implements schemas.HTTPTransportPlugin.
type Plugin struct {
	systemPrompt string
	override     bool
	blockRes     []*regexp.Regexp
	blockMessage string
}

// Init builds the plugin from its persisted config.
func Init(c *Config) (schemas.BasePlugin, error) {
	p := &Plugin{blockMessage: defaultBlockMessage}
	if c != nil {
		p.systemPrompt = strings.TrimSpace(c.SystemPrompt)
		p.override = strings.EqualFold(strings.TrimSpace(c.SystemMode), "override")
		if m := strings.TrimSpace(c.BlockMessage); m != "" {
			p.blockMessage = m
		}
		for _, pat := range c.BlockPatterns {
			re, err := regexp.Compile(pat)
			if err != nil {
				return nil, err
			}
			p.blockRes = append(p.blockRes, re)
		}
	}
	return p, nil
}

func (p *Plugin) GetName() string { return PluginName }

func (p *Plugin) Cleanup() error { return nil }

// HTTPTransportPreHook injects the system prompt and enforces the input guardrail.
// Returning (nil, nil) continues with in-place mutations to req applied;
// returning (*HTTPResponse, nil) short-circuits with that response.
func (p *Plugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if req == nil || len(req.Body) == 0 {
		return nil, nil
	}

	kind := requestKind(req.Path)
	if kind == kindOther {
		return nil, nil
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		// Not JSON we understand — let it pass untouched.
		return nil, nil
	}

	if len(p.blockRes) > 0 {
		if text := extractUserText(body); text != "" {
			for _, re := range p.blockRes {
				if re.MatchString(text) {
					return blockedResponse(p.blockMessage), nil
				}
			}
		}
	}

	if p.systemPrompt != "" {
		switch kind {
		case kindAnthropic:
			p.injectAnthropic(body)
		case kindOpenAI:
			p.injectOpenAI(body)
		}
		if newBody, err := json.Marshal(body); err == nil {
			req.Body = newBody
		}
	}

	return nil, nil
}

// HTTPTransportPostHook is a no-op (guardrail acts on the request only).
func (p *Plugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes chunks through unchanged.
func (p *Plugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

type reqKind int

const (
	kindOther reqKind = iota
	kindAnthropic
	kindOpenAI
)

func requestKind(path string) reqKind {
	switch {
	case strings.Contains(path, "/v1/messages"):
		return kindAnthropic
	case strings.Contains(path, "/chat/completions"), strings.Contains(path, "/v1/responses"):
		return kindOpenAI
	default:
		return kindOther
	}
}

// injectAnthropic merges the system prompt into the top-level `system` field,
// which may be absent, a string, or an array of content blocks.
func (p *Plugin) injectAnthropic(body map[string]any) {
	if p.override {
		body["system"] = p.systemPrompt
		return
	}
	switch existing := body["system"].(type) {
	case nil:
		body["system"] = p.systemPrompt
	case string:
		if strings.TrimSpace(existing) == "" {
			body["system"] = p.systemPrompt
		} else {
			body["system"] = p.systemPrompt + "\n\n" + existing
		}
	case []any:
		block := map[string]any{"type": "text", "text": p.systemPrompt}
		body["system"] = append([]any{block}, existing...)
	default:
		body["system"] = p.systemPrompt
	}
}

// injectOpenAI merges the system prompt into the leading system message.
func (p *Plugin) injectOpenAI(body map[string]any) {
	msgs, _ := body["messages"].([]any)
	sysMsg := map[string]any{"role": "system", "content": p.systemPrompt}

	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
			if p.override {
				first["content"] = p.systemPrompt
			} else if c, ok := first["content"].(string); ok && strings.TrimSpace(c) != "" {
				first["content"] = p.systemPrompt + "\n\n" + c
			} else {
				first["content"] = p.systemPrompt
			}
			return
		}
	}
	body["messages"] = append([]any{sysMsg}, msgs...)
}

// extractUserText concatenates the text of all user messages for pattern matching.
func extractUserText(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	var sb strings.Builder
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok || msg["role"] != "user" {
			continue
		}
		switch content := msg["content"].(type) {
		case string:
			sb.WriteString(content)
			sb.WriteByte('\n')
		case []any:
			for _, blk := range content {
				b, ok := blk.(map[string]any)
				if !ok {
					continue
				}
				if t, ok := b["text"].(string); ok {
					sb.WriteString(t)
					sb.WriteByte('\n')
				}
			}
		}
	}
	return sb.String()
}

func blockedResponse(message string) *schemas.HTTPResponse {
	payload := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "permission_error",
			"message": message,
		},
	}
	body, _ := json.Marshal(payload)
	return &schemas.HTTPResponse{
		StatusCode: 403,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       body,
	}
}
