package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
)

// Config is the JSON object placed under the plugin entry in config.json.
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

type runtimeConfig struct {
	systemPrompt string
	override     bool
	blockRes     []*regexp.Regexp
	blockMessage string
}

// cfg is swapped atomically so the hook is lock-free on the hot path.
var cfg atomic.Pointer[runtimeConfig]

const defaultBlockMessage = "Request blocked by guardrail policy."

// configure decodes the loader-provided config (a map[string]any from JSON) into
// a compiled runtimeConfig. Re-marshalling tolerates both map and raw-JSON inputs.
func configure(raw any) error {
	rc := &runtimeConfig{blockMessage: defaultBlockMessage}
	if raw != nil {
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var c Config
		if err := json.Unmarshal(b, &c); err != nil {
			return err
		}
		rc.systemPrompt = strings.TrimSpace(c.SystemPrompt)
		rc.override = strings.EqualFold(strings.TrimSpace(c.SystemMode), "override")
		if m := strings.TrimSpace(c.BlockMessage); m != "" {
			rc.blockMessage = m
		}
		for _, p := range c.BlockPatterns {
			re, err := regexp.Compile(p)
			if err != nil {
				return err
			}
			rc.blockRes = append(rc.blockRes, re)
		}
	}
	cfg.Store(rc)
	return nil
}

func handlePreHook(req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	rc := cfg.Load()
	if rc == nil || req == nil || len(req.Body) == 0 {
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

	if len(rc.blockRes) > 0 {
		if text := extractUserText(body, kind); text != "" {
			for _, re := range rc.blockRes {
				if re.MatchString(text) {
					return blockedResponse(rc.blockMessage), nil
				}
			}
		}
	}

	if rc.systemPrompt != "" {
		switch kind {
		case kindAnthropic:
			injectAnthropic(body, rc)
		case kindOpenAI:
			injectOpenAI(body, rc)
		}
		if newBody, err := json.Marshal(body); err == nil {
			req.Body = newBody
		}
	}

	return nil, nil
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

// injectAnthropic merges our system prompt into the top-level `system` field,
// which may be absent, a string, or an array of content blocks.
func injectAnthropic(body map[string]any, rc *runtimeConfig) {
	if rc.override {
		body["system"] = rc.systemPrompt
		return
	}
	switch existing := body["system"].(type) {
	case nil:
		body["system"] = rc.systemPrompt
	case string:
		if strings.TrimSpace(existing) == "" {
			body["system"] = rc.systemPrompt
		} else {
			body["system"] = rc.systemPrompt + "\n\n" + existing
		}
	case []any:
		block := map[string]any{"type": "text", "text": rc.systemPrompt}
		body["system"] = append([]any{block}, existing...)
	default:
		body["system"] = rc.systemPrompt
	}
}

// injectOpenAI merges our system prompt into the leading system message.
func injectOpenAI(body map[string]any, rc *runtimeConfig) {
	msgs, _ := body["messages"].([]any)
	sysMsg := map[string]any{"role": "system", "content": rc.systemPrompt}

	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok && first["role"] == "system" {
			if rc.override {
				first["content"] = rc.systemPrompt
			} else if c, ok := first["content"].(string); ok && strings.TrimSpace(c) != "" {
				first["content"] = rc.systemPrompt + "\n\n" + c
			} else {
				first["content"] = rc.systemPrompt
			}
			return
		}
	}
	body["messages"] = append([]any{sysMsg}, msgs...)
}

// extractUserText concatenates the text of all user messages for pattern matching.
func extractUserText(body map[string]any, kind reqKind) string {
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
