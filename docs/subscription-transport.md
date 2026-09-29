# Native ChatGPT subscription transport

`--provider codex --transport subscription --model MODEL` runs DiffVouch's Go
review loop against the ChatGPT subscription service. Authentication uses a
separate DiffVouch session. It does not launch Codex or use Codex's credential
cache. Existing CLI and API transports retain their behavior.

## Protocol basis

The implementation follows the public Codex source at revision
`a5290028a2936b91ec9305f6de7780463620ca70`:

- [Device authorization](https://github.com/openai/codex/blob/a5290028a2936b91ec9305f6de7780463620ca70/codex-rs/login/src/device_code_auth.rs) supplies a user code, polls for approval, and exchanges the authorization code with a PKCE verifier.
- [Authentication manager](https://github.com/openai/codex/blob/a5290028a2936b91ec9305f6de7780463620ca70/codex-rs/login/src/auth/manager.rs) defines the public OAuth client identifier and JSON token-refresh protocol.
- [Responses transport](https://github.com/openai/codex/blob/a5290028a2936b91ec9305f6de7780463620ca70/codex-rs/codex-api/src/endpoint/responses.rs) sends streaming model requests. Subscription requests use `https://chatgpt.com/backend-api/codex/responses`, a bearer access token, and the ChatGPT account ID.
- [Streaming events](https://github.com/openai/codex/blob/a5290028a2936b91ec9305f6de7780463620ca70/codex-rs/codex-api/src/sse/responses.rs) provide completed output items and a terminal completion event.

This is a compatibility integration, not a documented stable third-party API.
Subscription entitlement, device-login availability, and model access remain
service-controlled. [OpenAI authentication documentation](https://developers.openai.com/codex/auth)
distinguishes subscription access from separately billed API-key access.

## Session and failure behavior

Login uses device authorization and expires after 15 minutes. DiffVouch stores
the token set through its existing secret store; global configuration contains
only the secret reference. Refresh uses the global configuration lock to avoid
concurrent token rotation and replaces stored credentials before continuing.
Logout removes the DiffVouch session locally.

A model request that returns HTTP 401 triggers at most one refresh and retry.
An HTTP 429 stops the review. There is no fallback to the API transport. A stream
that fails or disconnects before completion fails the review rather than
returning a partial review. Tool results and opaque reasoning items stay in the
conversation across rounds. The existing review deadline and tool budgets apply.
The live service can omit the `Content-Type` header on a valid event stream;
DiffVouch accepts this only through the same event parser and completion checks.

## Validation

Automated tests cover device login, secure storage references, refresh rotation,
concurrent refresh, logout, cancellation, account continuity, native file reads,
reasoning history, interrupted streams, and refusal to fall back to API billing.
They use mock HTTP responses and need no subscription allowance.

On 2026-09-19, native device login and a live review succeeded using
`gpt-5.6-sol` with low reasoning effort. An isolated Go fixture changed a capacity
check from `<` to `<=`. The resulting review was complete, identified the
expected correctness defect at the changed line, and reported no unfinished
checks. The fixture remained unchanged, and neither Codex nor Claude was
available on the review process's PATH. This smoke test verifies the native
subscription path; it is not a review-quality benchmark result.

The live check exposed a valid event stream without `Content-Type`. A regression
test reproduces that response shape and verifies acceptance. Provider race tests,
vet, and the rebuilt CLI passed after the fix. The full race suite and all six
platform builds had also passed before that parsing correction.

The user has replaced the earlier 50% reserve with permission to use available
subscription allowance until exhaustion. The transport stops on HTTP 429 and
never falls back to paid API transport; resumable eval runs and reset handling
still need implementation.
