// Package openaicompatible implements a small, stdlib-only client for the
// OpenAI Chat Completions HTTP API.
//
// The client is deliberately one-shot: it sends a single non-streaming
// completion request and decodes a single non-streaming response. It does not
// implement SSE, retries, provider discovery, or a model catalog.
//
// Security properties:
//
//   - The endpoint must be an absolute HTTPS URL with an explicit host and
//     path. Plain HTTP, userinfo, fragments, and relative URLs are rejected.
//   - The API key is read from exactly one named environment variable and is
//     sent only in the Authorization header. It is never placed in the URL,
//     request body, query string, or an error message.
//   - Redirects are never followed, and the transport does not consult
//     HTTP_PROXY, HTTPS_PROXY, or NO_PROXY.
//   - TLS verification is always enabled. An optional CA file adds explicit
//     trust roots on top of the system pool; it never disables verification.
//   - Response bodies are bounded to 4 MiB and request bodies are bounded by
//     the caller's conversation limits.
//
// Wire profile:
//
// The client speaks a narrow, one-shot subset of the Chat Completions wire
// format. It is not a universal compatibility layer, and it makes no claim to
// work with every vendor that advertises "OpenAI-compatible":
//
//   - Tool messages carry an optional "name" field; it is omitted when empty.
//   - An assistant message that carries tool calls serializes "content" as the
//     empty string, not null. Some strict vendors reject that shape.
//   - Only non-streaming, single-choice, text-content responses are accepted.
//
// Error is intentionally generic: it classifies a failure as transport, status,
// or protocol and never includes the endpoint, request body, response body, or
// API key. That hides detail an operator might want when debugging an
// authentication failure; it is a deliberate trade-off, not an oversight.
package openaicompatible
