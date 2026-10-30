package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Version  int                `json:"version"`
	Routing  Routing            `json:"routing"`
	Profiles map[string]Profile `json:"profiles"`
}

type Routing struct {
	Primary    string   `json:"primary"`
	Fallback   string   `json:"fallback,omitempty"`
	FallbackOn []string `json:"fallbackOn,omitempty"`
}

type Profile struct {
	Backend                   string                     `json:"backend"`
	BaseURL                   string                     `json:"baseUrl"`
	Model                     string                     `json:"model"`
	APIKey                    string                     `json:"apiKey,omitempty"`
	APIKeyEnv                 string                     `json:"apiKeyEnv,omitempty"`
	APIKeyFile                string                     `json:"apiKeyFile,omitempty"`
	ResponseFormat            string                     `json:"responseFormat"`
	OutputTokenParameter      string                     `json:"outputTokenParameter"`
	ContextLength             int                        `json:"contextLength"`
	MaximumOutputTokens       int                        `json:"maximumOutputTokens,omitempty"`
	MaximumConcurrentRequests int                        `json:"maximumConcurrentRequests"`
	TimeoutSeconds            int                        `json:"timeoutSeconds"`
	MaximumAttempts           int                        `json:"maximumAttempts"`
	Stream                    bool                       `json:"stream"`
	ExtraParameters           map[string]json.RawMessage `json:"extraParameters,omitempty"`
}

type SecretSource struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

type ProviderSnapshot struct {
	Name    string       `json:"name"`
	Profile Profile      `json:"profile"`
	Auth    SecretSource `json:"auth"`
}

type Route struct {
	Primary        ProviderSnapshot  `json:"primary"`
	Fallback       *ProviderSnapshot `json:"fallback,omitempty"`
	FallbackOn     []string          `json:"fallbackOn"`
	RuntimeSecrets map[string]string `json:"-"`
}

type Overrides struct {
	Primary, Fallback, BaseURL, Model, APIKey, APIKeyEnv, APIKeyFile string
	NoFallback                                                       bool
	ContextLength, MaximumOutputTokens                               int
}

func DefaultPath() (string, error) {
	if path := os.Getenv("MARINADOREVIEW_CONFIG"); path != "" {
		return filepath.Abs(path)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "marina-the-reviewer", "config.json"), nil
}

func Load(path string) (Config, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, fmt.Errorf("cannot open provider configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Config{}, errors.New("provider configuration must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("cannot open provider configuration: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, errors.New("invalid provider configuration JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("provider configuration must contain one JSON object")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	for _, profile := range cfg.Profiles {
		if profile.APIKey != "" && info.Mode().Perm()&0077 != 0 {
			return Config{}, errors.New("configuration with literal API tokens must be private (0600)")
		}
	}
	return cfg, nil
}

func Init(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("cannot create configuration; existing files are preserved: %w", err)
	}
	defer f.Close()
	cfg := Config{Version: 1, Routing: Routing{Primary: "primary", Fallback: "backup", FallbackOn: DefaultFallbackErrors()}, Profiles: map[string]Profile{
		"primary": {Backend: "openai-compatible", BaseURL: "https://provider.example/v1", Model: "review-model", APIKeyEnv: "REVIEW_API_KEY", ContextLength: 131072},
		"backup":  {Backend: "openai-compatible", BaseURL: "https://backup-provider.example/v1", Model: "backup-review-model", APIKeyEnv: "BACKUP_REVIEW_API_KEY", ContextLength: 65536},
	}}
	for name, profile := range cfg.Profiles {
		if err := normalize(&profile); err != nil {
			return err
		}
		cfg.Profiles[name] = profile
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		return err
	}
	return f.Sync()
}

func DefaultFallbackErrors() []string {
	return []string{"network_error", "timeout", "rate_limit", "server_error"}
}

func (cfg Config) Validate() error {
	if cfg.Version != 1 {
		return errors.New("unsupported provider configuration version")
	}
	if cfg.Routing.Primary == "" {
		return errors.New("routing.primary is required")
	}
	if _, ok := cfg.Profiles[cfg.Routing.Primary]; !ok {
		return errors.New("primary profile does not exist")
	}
	if cfg.Routing.Fallback != "" {
		if cfg.Routing.Fallback == cfg.Routing.Primary {
			return errors.New("primary and fallback profiles must differ")
		}
		if _, ok := cfg.Profiles[cfg.Routing.Fallback]; !ok {
			return errors.New("fallback profile does not exist")
		}
	}
	for _, reason := range cfg.Routing.FallbackOn {
		if !contains(DefaultFallbackErrors(), reason) {
			return errors.New("unsupported fallback error category")
		}
	}
	for name, profile := range cfg.Profiles {
		if strings.TrimSpace(name) == "" {
			return errors.New("profile name is empty")
		}
		if err := normalize(&profile); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
	}
	return nil
}

func ResolveRouting(cfg Config, override Overrides) (Route, error) {
	if override.NoFallback && override.Fallback != "" {
		return Route{}, errors.New("--no-fallback conflicts with --fallback-profile")
	}
	if override.Primary != "" {
		cfg.Routing.Primary = override.Primary
	}
	if override.Fallback != "" {
		cfg.Routing.Fallback = override.Fallback
	}
	if override.NoFallback {
		cfg.Routing.Fallback = ""
	}
	if err := cfg.Validate(); err != nil {
		return Route{}, err
	}
	primary, err := ResolveProfile(cfg, cfg.Routing.Primary)
	if err != nil {
		return Route{}, err
	}
	route := Route{Primary: primary, FallbackOn: append([]string{}, cfg.Routing.FallbackOn...), RuntimeSecrets: map[string]string{}}
	if len(route.FallbackOn) == 0 {
		route.FallbackOn = DefaultFallbackErrors()
	}
	if cfg.Routing.Fallback != "" {
		backup, err := ResolveProfile(cfg, cfg.Routing.Fallback)
		if err != nil {
			return Route{}, err
		}
		route.Fallback = &backup
	}
	secretFlags := 0
	for _, value := range []string{override.APIKey, override.APIKeyEnv, override.APIKeyFile} {
		if value != "" {
			secretFlags++
		}
	}
	if secretFlags > 1 {
		return Route{}, errors.New("use one request credential source")
	}
	if override.BaseURL != "" {
		route.Primary.Profile.BaseURL = override.BaseURL
	}
	if override.Model != "" {
		route.Primary.Profile.Model = override.Model
	}
	if override.ContextLength != 0 {
		route.Primary.Profile.ContextLength = override.ContextLength
	}
	if override.MaximumOutputTokens != 0 {
		route.Primary.Profile.MaximumOutputTokens = override.MaximumOutputTokens
	}
	if override.APIKey != "" {
		route.Primary.Auth = SecretSource{Kind: "ephemeral"}
		route.RuntimeSecrets[route.Primary.Name] = override.APIKey
	} else if override.APIKeyEnv != "" {
		route.Primary.Auth = SecretSource{Kind: "env", Name: override.APIKeyEnv}
	} else if override.APIKeyFile != "" {
		route.Primary.Auth = SecretSource{Kind: "file", Name: override.APIKeyFile}
	}
	if err := normalize(&route.Primary.Profile); err != nil {
		return Route{}, err
	}
	return route, nil
}

func ResolveProfile(cfg Config, name string) (ProviderSnapshot, error) {
	if _, ok := cfg.Profiles[name]; !ok {
		return ProviderSnapshot{}, errors.New("provider profile does not exist")
	}
	profile := cfg.Profiles[name]
	if err := normalize(&profile); err != nil {
		return ProviderSnapshot{}, err
	}
	auth := SecretSource{Kind: "profile", Name: name}
	if profile.APIKey == "" && profile.APIKeyEnv == "" && profile.APIKeyFile == "" {
		auth = SecretSource{Kind: "none"}
	}
	profile.APIKey, profile.APIKeyEnv, profile.APIKeyFile = "", "", ""
	return ProviderSnapshot{Name: name, Profile: profile, Auth: auth}, nil
}

func ResolveToken(provider ProviderSnapshot, cfg Config, ephemeral map[string]string) (string, error) {
	if token, ok := ephemeral[provider.Name]; ok && token != "" {
		return token, nil
	}
	switch provider.Auth.Kind {
	case "ephemeral":
		if token := ephemeral[provider.Name]; token != "" {
			return token, nil
		}
		return "", errors.New("one-time API key is unavailable; provide a credential override")
	case "env":
		token := os.Getenv(provider.Auth.Name)
		if token == "" {
			return "", errors.New("API key environment variable is empty")
		}
		return token, nil
	case "file":
		return readSecretFile(provider.Auth.Name)
	case "none", "":
		return "", nil
	case "profile":
		profile, ok := cfg.Profiles[provider.Auth.Name]
		if !ok {
			return "", errors.New("saved credential profile is unavailable")
		}
		if err := normalize(&profile); err != nil || profile.BaseURL != provider.Profile.BaseURL {
			return "", errors.New("provider endpoint changed; an explicit credential override is required")
		}
		if profile.APIKeyEnv != "" {
			return ResolveToken(ProviderSnapshot{Auth: SecretSource{Kind: "env", Name: profile.APIKeyEnv}}, cfg, nil)
		}
		if profile.APIKeyFile != "" {
			return readSecretFile(profile.APIKeyFile)
		}
		return profile.APIKey, nil
	default:
		return "", errors.New("unsupported credential source")
	}
}

func readSecretFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("API key file is unavailable or not a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("API key file must be private (0600)")
	}
	if info.Size() > 16384 {
		return "", errors.New("API key file is too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("API key file could not be read")
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("API key file must contain one nonempty token")
	}
	return token, nil
}

func normalize(profile *Profile) error {
	if profile.Backend == "" {
		profile.Backend = "openai-compatible"
	}
	if profile.Backend != "openai-compatible" {
		return errors.New("unsupported backend")
	}
	u, err := url.Parse(profile.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("API URL must be an absolute URL without credentials, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return errors.New("remote API URLs require HTTPS")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	profile.BaseURL = u.String()
	if strings.TrimSpace(profile.Model) == "" {
		return errors.New("model is required")
	}
	if profile.ContextLength == 0 {
		profile.ContextLength = 131072
	}
	if profile.ContextLength < 1024 || profile.ContextLength > 16*1024*1024 || profile.MaximumOutputTokens < 0 || profile.MaximumOutputTokens >= profile.ContextLength {
		return errors.New("invalid context or output token budget")
	}
	if profile.ResponseFormat == "" {
		profile.ResponseFormat = "json_schema"
	}
	if profile.ResponseFormat != "json_schema" && profile.ResponseFormat != "json_object" {
		return errors.New("unsupported response format")
	}
	if profile.OutputTokenParameter == "" {
		profile.OutputTokenParameter = "max_tokens"
	}
	if profile.OutputTokenParameter != "max_tokens" && profile.OutputTokenParameter != "max_completion_tokens" {
		return errors.New("unsupported output token parameter")
	}
	if profile.MaximumConcurrentRequests == 0 {
		profile.MaximumConcurrentRequests = 1
	}
	if profile.TimeoutSeconds == 0 {
		profile.TimeoutSeconds = 300
	}
	if profile.MaximumAttempts == 0 {
		profile.MaximumAttempts = 2
	}
	if profile.MaximumConcurrentRequests < 1 || profile.MaximumConcurrentRequests > 64 || profile.TimeoutSeconds < 1 || profile.TimeoutSeconds > 86400 || profile.MaximumAttempts < 1 || profile.MaximumAttempts > 5 {
		return errors.New("invalid concurrency, timeout or retry limit")
	}
	secrets := 0
	for _, value := range []string{profile.APIKey, profile.APIKeyEnv, profile.APIKeyFile} {
		if value != "" {
			secrets++
		}
	}
	if secrets > 1 {
		return errors.New("use one profile credential source")
	}
	for key, value := range profile.ExtraParameters {
		if contains([]string{"model", "messages", "response_format", "max_tokens", "max_completion_tokens", "stream", "temperature", "authorization", "api_key"}, strings.ToLower(key)) || !json.Valid(value) {
			return errors.New("extra parameters contain a reserved key or invalid JSON")
		}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func ValidateRoute(route Route) error {
	check := func(provider ProviderSnapshot) error {
		if provider.Name == "" || provider.Profile.APIKey != "" || provider.Profile.APIKeyEnv != "" || provider.Profile.APIKeyFile != "" {
			return errors.New("provider snapshot must have a name and contain no credentials")
		}
		p := provider.Profile
		if err := normalize(&p); err != nil {
			return err
		}
		if p.BaseURL != provider.Profile.BaseURL || p.ContextLength != provider.Profile.ContextLength || p.MaximumAttempts != provider.Profile.MaximumAttempts {
			return errors.New("provider snapshot is not normalized")
		}
		switch provider.Auth.Kind {
		case "none", "":
		case "ephemeral":
		case "env", "file", "profile":
			if provider.Auth.Name == "" {
				return errors.New("credential reference is empty")
			}
		default:
			return errors.New("unsupported credential reference")
		}
		return nil
	}
	if err := check(route.Primary); err != nil {
		return err
	}
	if route.Fallback != nil {
		if route.Fallback.Name == route.Primary.Name {
			return errors.New("primary and fallback profiles must differ")
		}
		if err := check(*route.Fallback); err != nil {
			return err
		}
	}
	for _, code := range route.FallbackOn {
		if !contains(DefaultFallbackErrors(), code) {
			return errors.New("unsupported fallback error category")
		}
	}
	return nil
}
