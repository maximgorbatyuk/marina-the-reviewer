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
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const maximumResponseBytes = 16 << 20

type Request struct {
	SystemPrompt        string
	UserPrompt          string
	Schema              json.RawMessage
	MaximumOutputTokens int
}

type Usage struct {
	InputTokens  *int `json:"inputTokens"`
	OutputTokens *int `json:"outputTokens"`
	TotalTokens  *int `json:"totalTokens"`
}

type Response struct {
	Output       json.RawMessage `json:"output"`
	FinishReason string          `json:"finishReason"`
	Usage        Usage           `json:"usage"`
	Elapsed      time.Duration   `json:"elapsed"`
	Channel      string          `json:"channel"`
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

func Complete(ctx context.Context, provider config.ProviderSnapshot, token string, input Request) (Response, error) {
	started := time.Now()
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
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		code := "network_error"
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		return Response{}, &Error{Code: code, Message: "provider request did not complete"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
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
			data, _ := io.ReadAll(io.LimitReader(response.Body, 65536))
			lower := strings.ToLower(string(data))
			if strings.Contains(lower, "context_length") || strings.Contains(lower, "context window") || strings.Contains(lower, "context_length_exceeded") || strings.Contains(lower, "maximum context") {
				code = "context_overflow"
			}
		}
		return Response{}, &Error{Code: code, Message: fmt.Sprintf("provider returned HTTP %d", response.StatusCode), Status: response.StatusCode, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, &Error{Code: "network_error", Message: "provider response could not be read"}
	}
	if len(data) > maximumResponseBytes {
		return Response{}, &Error{Code: "invalid_output", Message: "provider response exceeds the size limit"}
	}
	var content, reasoning, finish string
	var usage Usage
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || bytes.HasPrefix(bytes.TrimSpace(data), []byte("data:")) {
		content, reasoning, finish, usage, err = parseSSE(data)
	} else {
		content, reasoning, finish, usage, err = parseJSON(data)
	}
	if err != nil {
		return Response{}, &Error{Code: "invalid_output", Message: "provider completion envelope is invalid"}
	}
	result := Response{Output: json.RawMessage(content), FinishReason: finish, Usage: usage, Elapsed: time.Since(started), Channel: "content"}
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
	Usage *struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
		TotalTokens      *int `json:"total_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func usageOf(value envelope) Usage {
	if value.Usage == nil {
		return Usage{}
	}
	return Usage{value.Usage.PromptTokens, value.Usage.CompletionTokens, value.Usage.TotalTokens}
}

func parseJSON(data []byte) (string, string, string, Usage, error) {
	var value envelope
	if err := json.Unmarshal(data, &value); err != nil || len(value.Choices) != 1 || (len(value.Error) != 0 && string(value.Error) != "null") {
		return "", "", "", Usage{}, errors.New("invalid completion")
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
	scanner.Buffer(make([]byte, 65536), maximumResponseBytes)
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
		if json.Unmarshal([]byte(payload), &value) != nil || (len(value.Error) > 0 && string(value.Error) != "null") {
			return "", "", "", usage, errors.New("invalid stream event")
		}
		if value.Usage != nil {
			usage = usageOf(value)
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
		return "", "", "", usage, err
	}
	if finish == "" {
		return "", "", "", usage, errors.New("stream has no terminal finish reason")
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
