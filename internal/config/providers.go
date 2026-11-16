package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits of MODEL_PROVIDERS. Six named endpoints are enough to offer a small catalogue next to the primary, and
// twenty models per provider keep the /model keyboard and the Mini App list readable.
const MaxModelProviders = 6
const MaxProviderModels = 20
const DefaultProviderTimeoutSeconds = int64(120)

// ProviderConfig is one named OpenAI-compatible endpoint from MODEL_PROVIDERS. A chat selects it with a
// "name:model" id; the primary STRATA endpoint is not in this list and is named by the reserved provider
// "strata". Models is the closed list of ids the provider may serve: a request for an id that is not in it is
// refused instead of being forwarded blindly. DefaultModel is the model used when a request says only
// "name:"; it must be one of Models and defaults to the first entry.
//
// ChatURL and ModelsURL are resolved by the loader, so main joins no URLs a second time.
type ProviderConfig struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
	// APIKeyFile is the optional absolute path of a file that carries APIKey (Docker secret, systemd credential).
	// The loader resolves it once and leaves the resolved value in APIKey; the two spellings are mutually
	// exclusive, exactly like the `*_FILE` rows of the process ENV. It is empty after a successful load.
	APIKeyFile     string   `json:"api_key_file,omitempty"`
	Models         []string `json:"models"`
	DefaultModel   string   `json:"default_model,omitempty"`
	TimeoutSeconds int64    `json:"timeout_seconds,omitempty"`
	// Model is the single-model spelling accepted from before the list existed. It is read only when Models is
	// empty, so a file that still says "model" keeps working as a one-element list.
	Model string `json:"model,omitempty"`

	ChatURL   string `json:"-"`
	ModelsURL string `json:"-"`
}

// providerNamePattern is the key a chat types before the colon. Lower-case only: the id is compared literally,
// so "Kimi:model" must not become a second spelling of "kimi:model".
var providerNamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// IsLocal reports whether the endpoint is a machine-local address. A provider without an api_key on a local
// endpoint (LM Studio and similar) is still usable; the same provider on a third-party host is skipped by main
// instead of being called unauthenticated.
func (p ProviderConfig) IsLocal() bool {
	parsed, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	return localProviderHost(parsed.Hostname())
}

func localProviderHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	switch host {
	case "localhost", "host.docker.internal", "host.containers.internal":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	if !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".localdomain", ".internal", ".lan", ".home"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// validateProviderBase accepts an absolute HTTP(S) URL without userinfo. Query and fragment are stripped by
// the loader, exactly like the primary and fallback endpoints: the path prefix is what decides the route.
func validateProviderBase(base string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "base_url must be an absolute HTTP(S) URL"
	}
	if parsed.User != nil {
		return "base_url must not contain userinfo or credentials"
	}
	return ""
}

func providerModelList(provider *ProviderConfig) []string {
	var raw = provider.Models
	if len(raw) == 0 {
		if legacy := strings.TrimSpace(provider.Model); legacy != "" {
			raw = []string{legacy}
		}
	}
	var models = make([]string, 0, len(raw))
	for _, model := range raw {
		models = append(models, strings.TrimSpace(model))
	}
	return models
}

// readProviderAPIKey resolves one api_key_file of MODEL_PROVIDERS. The rules mirror the `*_FILE` rows of the
// process ENV: an absolute path, a readable file of at most secretFileMaxBytes, valid UTF-8 without NUL bytes and
// a non-empty value after trimming the trailing newline every secret store adds. A provider with a half-read key
// must be refused by name, not silently called unauthenticated.
//
// Only the path and the reason are returned in a problem message; the content of the file never is.
func readProviderAPIKey(path string, readFile func(string) ([]byte, error)) (string, string) {
	if !strings.HasPrefix(path, "/") && !isWindowsAbsolutePath(path) {
		return "", fmt.Sprintf("api_key_file %q must be an absolute path to the key file ", path)
	}
	var payload, err = readFile(path)
	if err != nil {
		return "", fmt.Sprintf("api_key_file %q cannot be read (%v) ", path, err)
	}
	if len(payload) > secretFileMaxBytes {
		return "", fmt.Sprintf("api_key_file %q names a file larger than %d bytes: that is not a key ", path, secretFileMaxBytes)
	}
	var value = strings.TrimSpace(string(payload))
	if value == "" {
		return "", fmt.Sprintf("api_key_file %q names an empty file ", path)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return "", fmt.Sprintf("api_key_file %q names a file with NUL bytes or invalid UTF-8 ", path)
	}
	return value, ""
}

// parseModelProviders validates MODEL_PROVIDERS: a JSON array of at most six named providers. The row is all or
// nothing: one broken entry rejects the list, so a typo cannot leave a half-configured provider that looks
// available. An empty array means the feature is off.
//
// readFile is the reader used by api_key_file: the caller passes readSecretFile in production and a fake in
// tests, so key resolution is exercised without touching the filesystem.
func parseModelProviders(raw string, readFile func(string) ([]byte, error)) ([]ProviderConfig, []string) {
	var providers []ProviderConfig
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		return nil, []string{"ENV MODEL_PROVIDERS must be a JSON array of {name, base_url, api_key? | api_key_file?, models, default_model?, timeout_seconds?}: " + err.Error()}
	}
	if len(providers) == 0 {
		return nil, nil
	}
	if len(providers) > MaxModelProviders {
		return nil, []string{fmt.Sprintf("ENV MODEL_PROVIDERS lists %d providers, at most %d are allowed", len(providers), MaxModelProviders)}
	}

	var problems []string
	var names = map[string]bool{}
	for index := range providers {
		var provider = &providers[index]
		provider.Name = strings.TrimSpace(provider.Name)
		provider.BaseURL = strings.TrimSpace(provider.BaseURL)
		provider.APIKey = strings.TrimSpace(provider.APIKey)
		provider.APIKeyFile = strings.TrimSpace(provider.APIKeyFile)
		provider.DefaultModel = strings.TrimSpace(provider.DefaultModel)
		provider.Model = strings.TrimSpace(provider.Model)
		provider.Models = providerModelList(provider)

		var label = fmt.Sprintf("entry %d", index)
		if provider.Name != "" {
			label = fmt.Sprintf("entry %d (%s)", index, provider.Name)
		}
		if provider.Name == "" {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s has no name", label))
			continue
		}
		if provider.Name == "strata" {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s uses the reserved name \"strata\": the primary endpoint is selected as strata:<model>", label))
			continue
		}
		if !providerNamePattern.MatchString(provider.Name) {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s name must match [a-z0-9_-]+", label))
			continue
		}
		if names[provider.Name] {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS lists provider %q twice: names must be unique", provider.Name))
			continue
		}
		names[provider.Name] = true

		if provider.BaseURL == "" {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s has no base_url", label))
		} else if problem := validateProviderBase(provider.BaseURL); problem != "" {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s: %s", label, problem))
		} else {
			var clean = baseWithoutQueryOrFragment(provider.BaseURL)
			provider.ChatURL = JoinURL(clean, "/chat/completions")
			provider.ModelsURL = JoinURL(clean, "/models")
		}

		// A key may arrive as a file (Docker secret, systemd credential) instead of the JSON string. The two
		// spellings are refused together: guessing a precedence would make a rotation silently ineffective.
		// The resolved value replaces APIKey and APIKeyFile is cleared, so the rest of the process sees exactly
		// one shape and no code path can re-read a path whose content has changed under it.
		if provider.APIKey != "" && provider.APIKeyFile != "" {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s sets both api_key and api_key_file: choose one spelling ", label))
		} else if provider.APIKeyFile != "" {
			var key, problem = readProviderAPIKey(provider.APIKeyFile, readFile)
			if problem != "" {
				problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s %s", label, problem))
			} else {
				provider.APIKey = key
				provider.APIKeyFile = ""
			}
		}

		if len(provider.Models) == 0 {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s requires at least one model in models", label))
		} else if len(provider.Models) > MaxProviderModels {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s lists %d models, at most %d are allowed", label, len(provider.Models), MaxProviderModels))
		}
		var seenModels = map[string]bool{}
		for _, model := range provider.Models {
			if model == "" {
				problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s has an empty model id in models", label))
				continue
			}
			if seenModels[model] {
				problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s lists model %q twice: model ids must be unique within a provider", label, model))
				continue
			}
			seenModels[model] = true
		}
		if provider.DefaultModel == "" {
			if len(provider.Models) > 0 && provider.Models[0] != "" {
				provider.DefaultModel = provider.Models[0]
			}
		} else if !seenModels[provider.DefaultModel] {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s default_model %q is not one of its models", label, provider.DefaultModel))
		}

		if provider.TimeoutSeconds == 0 {
			provider.TimeoutSeconds = DefaultProviderTimeoutSeconds
		}
		if provider.TimeoutSeconds < 5 || provider.TimeoutSeconds > 600 {
			problems = append(problems, fmt.Sprintf("ENV MODEL_PROVIDERS %s timeout_seconds must be between 5 and 600", label))
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return providers, nil
}
