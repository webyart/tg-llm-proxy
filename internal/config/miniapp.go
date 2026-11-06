package config

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	DefaultMiniAppEnabled = false
	DefaultMiniAppHost    = "127.0.0.1"
	DefaultMiniAppPort    = int64(8084)
)

func (cfg Config) MiniAppAddress() string {
	return net.JoinHostPort(cfg.MiniAppHost, strconv.FormatInt(cfg.MiniAppPort, 10))
}

func loadMiniApp(raw map[string]string, cfg *Config) []string {
	cfg.MiniAppPublicURL = strings.TrimSpace(raw["MINIAPP_PUBLIC_URL"])
	cfg.MiniAppHost = strings.TrimSpace(raw["MINIAPP_LISTEN_HOST"])
	if cfg.MiniAppHost == "" {
		cfg.MiniAppHost = DefaultMiniAppHost
	}
	port, problem := Num(raw, "MINIAPP_PORT", DefaultMiniAppPort, 1, PortMax)
	cfg.MiniAppPort = port
	problems := Push(nil, problem)
	if !cfg.MiniAppEnabled {
		return problems
	}

	ip := net.ParseIP(cfg.MiniAppHost)
	if ip == nil || !ip.IsLoopback() && !ip.IsUnspecified() {
		problems = Push(problems, "ENV MINIAPP_LISTEN_HOST must be a loopback IP address or 0.0.0.0 for a container bridge")
	}
	public, err := url.Parse(cfg.MiniAppPublicURL)
	if err != nil || public.Scheme != "https" || public.Host == "" || public.Hostname() == "" || public.User != nil ||
		public.RawQuery != "" || public.ForceQuery || public.Fragment != "" || strings.Contains(cfg.MiniAppPublicURL, "#") {
		problems = Push(problems, "ENV MINIAPP_PUBLIC_URL must be a valid HTTPS URL without userinfo/query/fragment when MINIAPP_ENABLED=true")
	}
	return problems
}
