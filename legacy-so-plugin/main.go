package main

import "github.com/maximhq/bifrost/core/schemas"

const pluginName = "guardrail"

// Init is called once at load time with the `config` object from config.json.
func Init(config any) error { return configure(config) }

// GetName returns the plugin identifier (required by the loader).
func GetName() string { return pluginName }

// Cleanup is called on shutdown (required by the loader).
func Cleanup() error { return nil }

// HTTPTransportPreHook runs before the request is forwarded upstream.
// Returning (nil, nil) continues with any in-place mutations to req applied.
// Returning (*HTTPResponse, nil) short-circuits with that response.
func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return handlePreHook(req)
}
