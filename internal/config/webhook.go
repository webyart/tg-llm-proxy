package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	DefaultWebhookEnabled = false
	DefaultWebhookHost    = "127.0.0.1"
	DefaultWebhookPort    = int64(8082)
	DefaultWebhookPath    = "/telegram/webhook"
)

// WebhookAddress is the local listener behind the HTTPS reverse proxy or tunnel.
func (cfg Config) WebhookAddress() string {
	return net.JoinHostPort(cfg.WebhookHost, strconv.FormatInt(cfg.WebhookPort, 10))
}

func loadWebhook(raw map[string]string, cfg *Config) []string {
	cfg.WebhookPublicURL = strings.TrimSpace(raw["WEBHOOK_PUBLIC_URL"])
	cfg.WebhookSecretToken = strings.TrimSpace(raw["WEBHOOK_SECRET_TOKEN"])
	cfg.WebhookHost = strings.TrimSpace(raw["WEBHOOK_LISTEN_HOST"])
	if cfg.WebhookHost == "" {
		cfg.WebhookHost = DefaultWebhookHost
	}
	cfg.WebhookPath = strings.TrimSpace(raw["WEBHOOK_PATH"])
	if cfg.WebhookPath == "" {
		cfg.WebhookPath = DefaultWebhookPath
	}
	port, portProblem := Num(raw, "WEBHOOK_PORT", DefaultWebhookPort, 1, PortMax)
	cfg.WebhookPort = port
	problems := Push(nil, portProblem)
	if !cfg.WebhookEnabled {
		return problems
	}

	if net.ParseIP(cfg.WebhookHost) == nil {
		problems = Push(problems, "ENV WEBHOOK_LISTEN_HOST must be an IP address such as 127.0.0.1 or 0.0.0.0")
	}
	if !strings.HasPrefix(cfg.WebhookPath, "/") || strings.ContainsAny(cfg.WebhookPath, "?# ") {
		problems = Push(problems, "ENV WEBHOOK_PATH must be an absolute URL path without query, fragment, or spaces")
	}
	secretLen := len(cfg.WebhookSecretToken)
	if secretLen < 16 || secretLen > 256 {
		problems = Push(problems, "ENV WEBHOOK_SECRET_TOKEN must contain 16..256 ASCII letters, digits, '_' or '-' when WEBHOOK_ENABLED=true")
	} else {
		for _, symbol := range cfg.WebhookSecretToken {
			if (symbol >= 'a' && symbol <= 'z') || (symbol >= 'A' && symbol <= 'Z') || (symbol >= '0' && symbol <= '9') || symbol == '_' || symbol == '-' {
				continue
			}
			problems = Push(problems, "ENV WEBHOOK_SECRET_TOKEN may contain only ASCII letters, digits, '_' or '-'")
			break
		}
	}
	public, err := url.Parse(cfg.WebhookPublicURL)
	if err != nil || public.Scheme != "https" || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || public.Path != cfg.WebhookPath {
		problems = Push(problems, fmt.Sprintf("ENV WEBHOOK_PUBLIC_URL must be an HTTPS URL without userinfo/query/fragment whose path exactly matches WEBHOOK_PATH (%s)", cfg.WebhookPath))
	}
	return problems
}
