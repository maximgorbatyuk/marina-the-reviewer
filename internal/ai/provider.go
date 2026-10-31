package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/redact"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Request struct {
	SystemPrompt        string
	UserPrompt          string
	Schema              json.RawMessage
	MaximumOutputTokens int
	OnPrepared          func(string, []byte) error
	RedactionSecrets    map[string]string
}

type Usage struct {
	InputTokens  *int `json:"inputTokens"`
	OutputTokens *int `json:"outputTokens"`
	TotalTokens  *int `json:"totalTokens"`
}

type Response struct {
	Output                   json.RawMessage `json:"output"`
	RawOutput                string          `json:"-"`
	RawResponse              string          `json:"-"`
	ResponsePayload          string          `json:"-"`
	ResponsePayloadTruncated bool            `json:"-"`
	FinishReason             string          `json:"finishReason"`
	Usage                    Usage           `json:"usage"`
	Elapsed                  time.Duration   `json:"elapsed"`
	Channel                  string          `json:"channel"`
}

type Error struct {
	Code       string        `json:"code"`
	Message    string        `json:"message"`
	Status     int           `json:"status,omitempty"`
	RetryAfter time.Duration `json:"-"`
	Partial    *Response     `json:"-"`
}

func (err *Error) Error() string { return err.Code + ": " + err.Message }

func IsRetryable(err error) bool {
	var providerErr *Error
	return errors.As(err, &providerErr) && (providerErr.Code == "network_error" || providerErr.Code == "timeout" || providerErr.Code == "rate_limit" || providerErr.Code == "server_error")
}

func Complete(ctx context.Context, provider config.ProviderSnapshot, token string, input Request) (result Response, callErr error) {
	started := time.Now()
	defer func() { result.Elapsed = time.Since(started) }()
	profile := provider.Profile
	if input.MaximumOutputTokens < 1 || (profile.ContextLength > 0 && input.MaximumOutputTokens >= profile.ContextLength) {
		return Response{}, &Error{Code: "configuration", Message: "invalid output token budget"}
	}
	var responseSchema any
	if json.Unmarshal(input.Schema, &responseSchema) != nil || externalReference(responseSchema) {
		return Response{}, &Error{Code: "configuration", Message: "response schema must be self-contained JSON"}
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("review-schema.json", responseSchema); err != nil {
		return Response{}, &Error{Code: "configuration", Message: "invalid response schema"}
	}
	compiled, err := compiler.Compile("review-schema.json")
	if err != nil {
		return Response{}, &Error{Code: "configuration", Message: "response schema could not be compiled"}
	}
	system := input.SystemPrompt
	responseFormat := map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "architecture_review", "strict": true, "schema": responseSchema}}
	if profile.ResponseFormat == "json_object" {
		responseFormat = map[string]any{"type": "json_object"}
		system += "\n\nReturn JSON that matches this schema exactly:\n" + string(input.Schema)
	}
	parameter := profile.OutputTokenParameter
	if parameter == "" {
		parameter = "max_tokens"
	}
	body := map[string]any{"model": profile.Model, "temperature": 0, "stream": profile.Stream,
		parameter: input.MaximumOutputTokens, "response_format": responseFormat,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": input.UserPrompt}}}
	if usage.Enabled(ctx) && requestStreamUsage(profile) {
		body["stream_options"] = map[string]bool{"include_usage": true}
	}
	for key, value := range profile.ExtraParameters {
		if _, reserved := body[key]; reserved || strings.EqualFold(key, "authorization") || strings.EqualFold(key, "api_key") {
			return Response{}, &Error{Code: "configuration", Message: "extra parameter overrides a protected request field"}
		}
		body[key] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Response{}, &Error{Code: "configuration", Message: "request parameters are invalid"}
	}
	endpoint, err := chatEndpoint(profile.BaseURL)
	if err != nil {
		return Response{}, &Error{Code: "configuration", Message: "invalid provider URL"}
	}
	if input.OnPrepared != nil {
		if err := input.OnPrepared(endpoint, encoded); err != nil {
			return Response{}, &Error{Code: "configuration", Message: "provider request archive could not be saved"}
		}
	}
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Response{}, &Error{Code: "configuration", Message: "provider request could not be created"}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	secrets := make(map[string]string, len(input.RedactionSecrets)+1)
	for _, value := range input.RedactionSecrets {
		secrets[value] = value
	}
	secrets[token] = token
	meta := usageMetadata(provider, token)
	for _, value := range []*string{&meta.ProviderID, &meta.Provider, &meta.Profile, &meta.Model} {
		*value = redact.Text(*value, secrets)
	}
	meta.RequestURL, meta.RequestPayload = redact.Text(endpoint, secrets), []byte(redact.Text(string(encoded), secrets))
	finishUsage := usage.Begin(ctx, meta)
	var recorded usage.Response
	defer func() {
		if recorded.Complete != nil {
			recorded.Payload = []byte(redact.Text(result.ResponsePayload, secrets))
		}
		finishUsage(tokenCounts(result.Usage), usageStatus(callErr), recorded)
	}()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		code := "network_error"
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		message := "provider request did not complete"
		if code == "timeout" {
			message = fmt.Sprintf("provider request did not complete before its deadline (timeout %s; elapsed %d ms; no response headers)", timeout, time.Since(started).Milliseconds())
		}
		return Response{}, &Error{Code: code, Message: message}
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(response.Body)
	complete := readErr == nil
	recorded.HTTPStatus, recorded.Complete = response.StatusCode, &complete
	result.ResponsePayload, result.ResponsePayloadTruncated = string(data), !complete
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.RawResponse = string(data)
		var value envelope
		if json.Unmarshal(data, &value) == nil {
			result.Usage = usageOf(value)
		}
		code := "request_rejected"
		switch {
		case response.StatusCode == 401 || response.StatusCode == 403:
			code = "authentication"
		case response.StatusCode == 429:
			code = "rate_limit"
		case response.StatusCode >= 500:
			code = "server_error"
		case response.StatusCode >= 300 && response.StatusCode < 400:
			code = "configuration"
		case response.StatusCode == 400 || response.StatusCode == 413:
			lower := strings.ToLower(string(data))
			if strings.Contains(lower, "context_length") || strings.Contains(lower, "context window") || strings.Contains(lower, "context_length_exceeded") || strings.Contains(lower, "maximum context") {
				code = "context_overflow"
			}
		}
		return result, &Error{Code: code, Message: fmt.Sprintf("provider returned HTTP %d", response.StatusCode), Status: response.StatusCode, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	if readErr != nil {
		result.RawResponse = string(data)
		result.ResponsePayload = string(data)
		result.ResponsePayloadTruncated = true
		var partialReasoning string
		if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || bytes.HasPrefix(bytes.TrimSpace(data), []byte("data:")) {
			result.RawOutput, partialReasoning, result.FinishReason, result.Usage, _ = parseSSE(data)
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			terminal := "absent"
			if result.FinishReason != "" {
				terminal = "present"
			}
			message := fmt.Sprintf("provider response did not finish before its deadline (timeout %s; elapsed %d ms; received %d bytes; content %d bytes; reasoning %d bytes; terminal finish reason %s)",
				timeout, time.Since(started).Milliseconds(), len(data), len(result.RawOutput), len(partialReasoning), terminal)
			return result, &Error{Code: "timeout", Message: message}
		}
		return result, &Error{Code: "network_error", Message: "provider response could not be read"}
	}
	var content, reasoning, finish string
	var usage Usage
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || bytes.HasPrefix(bytes.TrimSpace(data), []byte("data:")) {
		content, reasoning, finish, usage, err = parseSSE(data)
	} else {
		content, reasoning, finish, usage, err = parseJSON(data)
	}
	result.Usage = usage
	if err != nil {
		result.RawOutput = content
		result.RawResponse = string(data)
		result.ResponsePayload = string(data)
		return result, &Error{Code: "invalid_output", Message: "provider completion envelope is invalid"}
	}
	result = Response{Output: json.RawMessage(content), RawOutput: content, ResponsePayload: string(data), FinishReason: finish, Usage: usage, Elapsed: time.Since(started), Channel: "content"}
	if finish == "length" || finish == "max_tokens" {
		return result, &Error{Code: "output_truncated", Message: "provider output token limit was reached", Partial: &result}
	}
	if finish == "content_filter" || finish == "refusal" {
		return result, &Error{Code: "refused", Message: "provider declined the request"}
	}
	if finish != "stop" {
		return result, &Error{Code: "invalid_output", Message: "provider did not confirm a complete final answer", Partial: &result}
	}
	validate := func(value string) bool {
		var document any
		return json.Unmarshal([]byte(value), &document) == nil && compiled.Validate(document) == nil
	}
	if !validate(content) {
		if strings.TrimSpace(content) == "" && validate(reasoning) {
			result.Output = json.RawMessage(reasoning)
			result.RawOutput = reasoning
			result.Channel = "reasoning_json"
		} else {
			return result, &Error{Code: "invalid_output", Message: "model output does not match the response schema", Partial: &result}
		}
	}
	return result, nil
}

func chatEndpoint(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid endpoint")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		if !strings.HasSuffix(u.Path, "/v1") {
			u.Path += "/v1"
		}
		u.Path += "/chat/completions"
	}
	return u.String(), nil
}

type tokenUsage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
	TotalTokens      *int `json:"total_tokens"`
}

type envelope struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"`
			Refusal   string `json:"refusal"`
		} `json:"message"`
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *tokenUsage `json:"usage"`
	Groq  *struct {
		Usage *tokenUsage `json:"usage"`
	} `json:"x_groq"`
	Error json.RawMessage `json:"error"`
}

func usageOf(value envelope) Usage {
	if value.Usage == nil && value.Groq != nil {
		value.Usage = value.Groq.Usage
	}
	if value.Usage == nil {
		return Usage{}
	}
	return Usage{value.Usage.PromptTokens, value.Usage.CompletionTokens, value.Usage.TotalTokens}
}

func parseJSON(data []byte) (string, string, string, Usage, error) {
	var value envelope
	if err := json.Unmarshal(data, &value); err != nil {
		return "", "", "", Usage{}, errors.New("invalid completion")
	}
	if len(value.Choices) != 1 || (len(value.Error) != 0 && string(value.Error) != "null") {
		return "", "", "", usageOf(value), errors.New("invalid completion")
	}
	choice := value.Choices[0]
	if choice.Message.Refusal != "" {
		choice.FinishReason = "refusal"
	}
	return choice.Message.Content, choice.Message.Reasoning, choice.FinishReason, usageOf(value), nil
}

func parseSSE(data []byte) (string, string, string, Usage, error) {
	var content, reasoning strings.Builder
	var finish string
	var usage Usage
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, min(65536, len(data)+1)), len(data)+1)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var value envelope
		if json.Unmarshal([]byte(payload), &value) != nil {
			return content.String(), reasoning.String(), finish, usage, errors.New("invalid stream event")
		}
		if value.Usage != nil || value.Groq != nil && value.Groq.Usage != nil {
			usage = usageOf(value)
		}
		if len(value.Error) > 0 && string(value.Error) != "null" {
			return content.String(), reasoning.String(), finish, usage, errors.New("invalid stream event")
		}
		if len(value.Choices) > 0 {
			content.WriteString(value.Choices[0].Delta.Content)
			reasoning.WriteString(value.Choices[0].Delta.Reasoning)
			if value.Choices[0].FinishReason != "" {
				finish = value.Choices[0].FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return content.String(), reasoning.String(), finish, usage, err
	}
	if finish == "" {
		return content.String(), reasoning.String(), finish, usage, errors.New("stream has no terminal finish reason")
	}
	return content.String(), reasoning.String(), finish, usage, nil
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(time.Now()) {
		return time.Until(at)
	}
	return 0
}

func externalReference(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" || key == "$dynamicRef" {
				if ref, ok := child.(string); !ok || !strings.HasPrefix(ref, "#") {
					return true
				}
			}
			if externalReference(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if externalReference(child) {
				return true
			}
		}
	}
	return false
}
