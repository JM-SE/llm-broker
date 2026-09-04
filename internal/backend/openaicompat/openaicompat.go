// Package openaicompat implements the generic OpenAI-compatible Chat
// Completions adapter. It maps the normalized broker request to a narrow,
// documented subset of the Chat Completions protocol and normalizes the
// response. It is tested against the conformance suite plus adversarial
// httptest fixtures; compatibility is scoped to what is tested, never a
// universal claim.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
)

func init() {
	backend.RegisterDefault(config.AdapterOpenAICompatible, New)
}

// Transport budgets (seconds). These are process-level defaults of the
// built-in adapter; the overall inference budget comes from the worker
// context deadline.
const (
	connectTimeout       = 5 * time.Second
	overallDefaultBudget = 15 * time.Minute
	maxResponseBytes     = 4 << 20 // 4 MiB output ceiling
	maxErrorBodyBytes    = 4 << 10 // provider error bodies are bounded
	// maxBodyBytes guards against pathologically large request bodies.
	maxBodyBytes = 512 << 10
)

// Adapter is the OpenAI-compatible Chat Completions adapter.
type Adapter struct {
	baseURL           *url.URL
	model             string
	credential        string
	features          map[backend.Feature]bool
	client            *http.Client
	respHeaderTimeout time.Duration
	maxResponseBytes  int64
}

// New builds the adapter from operator configuration. The credential is
// read from the environment variable named by credential_env at
// construction time; it is never logged and never stored.
func New(cfg config.BackendConfig) (backend.Adapter, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base_url %q: %w", cfg.BaseURL, err)
	}
	credential := ""
	if cfg.CredentialEnv != "" {
		credential = os.Getenv(cfg.CredentialEnv)
		if credential == "" {
			return nil, fmt.Errorf("credential environment variable %s is not set", cfg.CredentialEnv)
		}
	}
	features := map[backend.Feature]bool{}
	for _, f := range cfg.Features {
		pf, err := backend.ParseFeature(f)
		if err != nil {
			return nil, err
		}
		features[pf] = true
	}
	headerTimeoutSeconds := cfg.ResponseHeaderTimeoutSeconds
	if headerTimeoutSeconds == 0 {
		headerTimeoutSeconds = config.DefaultResponseHeaderTimeoutSeconds
	}
	responseHeaderTimeout := time.Duration(headerTimeoutSeconds) * time.Second
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
	// Redirects are disabled: the broker never follows a provider redirect,
	// so credentials cannot cross origins.
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &Adapter{
		baseURL:           base,
		model:             cfg.Model,
		credential:        credential,
		features:          features,
		client:            client,
		respHeaderTimeout: responseHeaderTimeout,
		maxResponseBytes:  maxResponseBytes,
	}, nil
}

// setResponseHeaderTimeout overrides the header-acknowledgment budget and
// rebuilds the transport accordingly. Test-only seam.
func (a *Adapter) setResponseHeaderTimeout(d time.Duration) {
	a.respHeaderTimeout = d
	if tr, ok := a.client.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = d
	}
}

// Type returns "openai_compatible".
func (a *Adapter) Type() string { return config.AdapterOpenAICompatible }

// Infer performs one bounded Chat Completions request. The overall budget is
// the context deadline; if the caller supplies none, a generous default
// applies. Classification is conservative: once the request may have been
// accepted, failures are indeterminate, never auto-retried by the worker.
func (a *Adapter) Infer(ctx context.Context, req *backend.Request) (*backend.Response, error) {
	if a.credential == "" {
		return nil, backend.NewError(domain.ErrorInternal, "credential_missing",
			"the backend credential is not configured")
	}
	body, err := a.buildBody(req)
	if err != nil {
		return nil, err
	}

	if _, deadlineSet := ctx.Deadline(); !deadlineSet {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, overallDefaultBudget)
		defer cancel()
	}

	endpoint := a.endpoint()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, backend.NewError(domain.ErrorInternal, "request_build_failed",
			"the request could not be constructed")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.credential)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a bounded amount for connection reuse; the content is never
		// included in errors.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, classifyHTTPStatus(resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, a.maxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		// The connection broke mid-response: the inference may have run.
		return nil, backend.NewError(domain.ErrorIndeterminate, "response_interrupted",
			"the response was interrupted; the inference outcome is unknown")
	}
	if int64(len(raw)) > a.maxResponseBytes {
		return nil, backend.NewError(domain.ErrorOutputInvalid, "response_too_large",
			"the provider response exceeded the size limit")
	}

	out, err := parseChatCompletion(raw)
	if err != nil {
		// Well-formed HTTP but unparseable body: never retry automatically.
		return nil, backend.NewError(domain.ErrorOutputInvalid, "malformed_response",
			"the provider response could not be parsed")
	}
	return out, nil
}

// endpoint is the Chat Completions URL derived from the operator-configured
// base URL.
func (a *Adapter) endpoint() string {
	return strings.TrimRight(a.baseURL.String(), "/") + "/chat/completions"
}

// buildBody maps the normalized request to the Chat Completions subset.
// Fields that are not set are omitted; unsupported provider parameters are
// never invented. Structured output is emitted only when the adapter
// declares the feature and the workload request requires it.
func (a *Adapter) buildBody(req *backend.Request) ([]byte, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body := struct {
		Model          string          `json:"model"`
		Messages       []message       `json:"messages"`
		MaxTokens      *int64          `json:"max_tokens,omitempty"`
		Temperature    *float64        `json:"temperature,omitempty"`
		TopP           *float64        `json:"top_p,omitempty"`
		TopK           *int64          `json:"top_k,omitempty"`
		MinP           *float64        `json:"min_p,omitempty"`
		Seed           *int64          `json:"seed,omitempty"`
		Stop           []string        `json:"stop,omitempty"`
		ResponseFormat *responseFormat `json:"response_format,omitempty"`
	}{
		Model:       a.model,
		MaxTokens:   req.Generation.MaxTokens,
		Temperature: req.Generation.Temperature,
		TopP:        req.Generation.TopP,
		TopK:        req.Generation.TopK,
		MinP:        req.Generation.MinP,
		Seed:        req.Generation.Seed,
		Stop:        req.Generation.Stop,
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, message{Role: string(m.Role), Content: m.Content})
	}
	if req.Response.Kind != domain.ResponseText {
		if !a.features[backend.FeatureStructuredOutput] {
			return nil, backend.NewError(domain.ErrorInternal, "feature_not_declared",
				"structured output was requested but the backend does not declare the feature")
		}
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	return json.Marshal(body)
}

type responseFormat struct {
	Type string `json:"type"`
}

// parseChatCompletion normalizes a Chat Completions response. Hidden
// reasoning content is discarded before anything else sees it.
func parseChatCompletion(raw []byte) (*backend.Response, error) {
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role             string `json:"role"`
				Content          any    `json:"content"`
				ReasoningContent any    `json:"reasoning_content"` // discarded below
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			TotalTokens      *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		return nil, err
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("no choices in response")
	}
	// Reasoning content must never reach the caller: it is dropped here and
	// exists nowhere else in the product.
	content, ok := parsed.Choices[0].Message.Content.(string)
	if !ok {
		// Some servers serialize null content; treat as empty rather than
		// echoing raw values.
		content = ""
	}
	finish := domain.FinishUnknown
	switch parsed.Choices[0].FinishReason {
	case "stop":
		finish = domain.FinishStop
	case "length":
		finish = domain.FinishLength
	case "content_filter":
		finish = domain.FinishContentFilter
	}
	var usage domain.Usage
	if u := parsed.Usage.PromptTokens; u != nil {
		usage.PromptTokens = *u
	}
	if u := parsed.Usage.CompletionTokens; u != nil {
		usage.CompletionTokens = *u
	}
	if u := parsed.Usage.TotalTokens; u != nil {
		usage.TotalTokens = *u
	} else {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return &backend.Response{Content: content, FinishReason: finish, Usage: usage}, nil
}

// classifyHTTPStatus maps provider status codes to stable classes. The body
// is never included in the error.
func classifyHTTPStatus(status int) *backend.Error {
	switch {
	case status == http.StatusTooManyRequests || status >= 500:
		return backend.NewError(domain.ErrorTransientSafe, "backend_error",
			"the backend reported a transient error")
	case status >= 400:
		return backend.NewError(domain.ErrorBackendRejected, "backend_rejected",
			"the backend rejected the request")
	default:
		// Redirects are disabled, so any other status is a protocol surprise.
		return backend.NewError(domain.ErrorBackendRejected, "unexpected_status",
			"the backend returned an unexpected status")
	}
}

// classifyTransportError conservatively classifies connection-phase
// failures. Only errors that provably happened before the request could be
// sent (dial-phase) are transient_unsent; everything else may have been
// accepted and is indeterminate.
func classifyTransportError(err error) *backend.Error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return backend.NewError(domain.ErrorIndeterminate, "request_interrupted",
			"the request was interrupted; the inference outcome is unknown")
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		var opErr *net.OpError
		if errors.As(uerr.Err, &opErr) && opErr.Op == "dial" {
			return backend.NewError(domain.ErrorTransientUnsent, "connect_failed",
				"the backend could not be reached")
		}
		return backend.NewError(domain.ErrorIndeterminate, "request_interrupted",
			"the request was interrupted; the inference outcome is unknown")
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return backend.NewError(domain.ErrorTransientUnsent, "connect_failed",
			"the backend could not be reached")
	}
	return backend.NewError(domain.ErrorIndeterminate, "connection_failed",
		"the connection failed; the inference outcome is unknown")
}
