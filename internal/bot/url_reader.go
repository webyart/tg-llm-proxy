package bot

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"bot/internal/model"
)

const (
	urlReaderMaxURLBytes  = 2048
	urlReaderMaxBodyBytes = 1 << 20
	urlReaderMaxRunes     = 8000
	urlReaderMaxRedirects = 5
	urlReaderTimeout      = 15 * time.Second
)

type urlReaderResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

type urlReaderDialFunc func(ctx context.Context, network, address string) (net.Conn, error)

type urlReaderHooks struct {
	resolver   urlReaderResolver
	dial       urlReaderDialFunc
	tlsRootCAs *x509.CertPool
}

func urlReaderTools(enabled bool) []model.ToolDefinition {
	if !enabled {
		return nil
	}
	return []model.ToolDefinition{{
		Name:        "read_url",
		Description: "Read text from a public HTTPS web page.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "Public HTTPS URL to read"},
			},
			"required": []string{"url"},
		},
	}}
}

func executeURLReader(ctx context.Context, call model.ToolCall) (string, error) {
	return executeURLReaderWithClient(ctx, call, newURLReaderClient(urlReaderHooks{}))
}

func executeURLReaderWithClient(ctx context.Context, call model.ToolCall, client *http.Client) (string, error) {
	if call.Name != "read_url" {
		return "", errors.New("unknown tool")
	}
	decoder := json.NewDecoder(strings.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	var input struct {
		URL string `json:"url"`
	}
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return "", err
	}
	text, err := readURL(ctx, client, input.URL)
	if err != nil {
		payload, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
		return string(payload), marshalErr
	}
	payload, err := json.Marshal(map[string]string{"result": text})
	return string(payload), err
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func newURLReaderClient(hooks urlReaderHooks) *http.Client {
	resolver := hooks.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dial := hooks.dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: urlReaderTimeout, KeepAlive: 30 * time.Second}
		dial = dialer.DialContext
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if hooks.tlsRootCAs != nil {
		tlsConfig.RootCAs = hooks.tlsRootCAs
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           urlReaderDialContext(resolver, dial),
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   urlReaderTimeout,
		ResponseHeaderTimeout: urlReaderTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   urlReaderTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > urlReaderMaxRedirects {
				return errors.New("too many redirects")
			}
			_, err := parseURLReaderURL(req.URL.String())
			return err
		},
	}
}

func urlReaderDialContext(resolver urlReaderResolver, dial urlReaderDialFunc) urlReaderDialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid dial address")
		}
		var addresses []netip.Addr
		if parsed, err := netip.ParseAddr(host); err == nil {
			addresses = append(addresses, parsed)
		} else {
			ips, err := resolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, errors.New("DNS resolution failed")
			}
			for _, ip := range ips {
				parsed, ok := netip.AddrFromSlice(ip)
				if ok {
					addresses = append(addresses, parsed)
				}
			}
		}
		var dialErr error
		for _, ip := range addresses {
			ip = ip.Unmap()
			if !safeURLReaderIP(ip) {
				continue
			}
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		if dialErr != nil {
			return nil, errors.New("connection failed")
		}
		return nil, errors.New("no safe IP address")
	}
}

func parseURLReaderURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > urlReaderMaxURLBytes {
		return nil, errors.New("URL is empty or too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("only HTTPS URLs with a host are allowed")
	}
	if u.User != nil {
		return nil, errors.New("URL userinfo is not allowed")
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return nil, errors.New("localhost is not allowed")
	}
	if strings.Contains(host, "%") {
		return nil, errors.New("IP zones are not allowed")
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("invalid URL port")
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil && !safeURLReaderIP(ip.Unmap()) {
		return nil, errors.New("IP address is not allowed")
	}
	return u, nil
}

func safeURLReaderIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range urlReaderBlockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var urlReaderBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fec0::/10"),
}

func readURL(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	u, err := parseURLReaderURL(rawURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("invalid URL")
	}
	req.Header.Set("Accept", "text/*, application/json, application/xml, application/xhtml+xml")
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("failed to read URL")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	mediaType, err := urlReaderMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return "", err
	}
	if resp.ContentLength > urlReaderMaxBodyBytes {
		return "", errors.New("response body exceeds 1 MiB")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, urlReaderMaxBodyBytes+1))
	if err != nil {
		return "", errors.New("failed to read response body")
	}
	if len(body) > urlReaderMaxBodyBytes {
		return "", errors.New("response body exceeds 1 MiB")
	}
	text := string(body)
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		text = htmlURLReaderText(text)
	} else {
		text = strings.ToValidUTF8(text, "")
	}
	return truncateURLReaderText(cleanURLReaderText(text)), nil
}

func urlReaderMediaType(header string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return "", errors.New("unsupported content type")
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml" || mediaType == "application/xhtml+xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") {
		return mediaType, nil
	}
	return "", errors.New("unsupported content type")
}

func htmlURLReaderText(input string) string {
	input = strings.ToValidUTF8(input, "")
	lower := asciiLowerURLReader(input)
	var output strings.Builder
	skipTag := ""
	skipDepth := 0
	for pos := 0; pos < len(input); {
		open := strings.IndexByte(lower[pos:], '<')
		if open < 0 {
			if skipTag == "" {
				output.WriteString(input[pos:])
			}
			break
		}
		open += pos
		if skipTag == "" {
			output.WriteString(input[pos:open])
			output.WriteByte(' ')
		}
		if strings.HasPrefix(lower[open:], "<!--") {
			end := strings.Index(lower[open+4:], "-->")
			if end < 0 {
				break
			}
			pos = open + 4 + end + 3
			continue
		}
		close := htmlTagEndURLReader(input, open+1)
		if close < 0 {
			break
		}
		name, closing, opening := htmlTagNameURLReader(lower[open+1 : close])
		if skipTag != "" {
			if name == skipTag {
				if closing {
					skipDepth--
					if skipDepth == 0 {
						skipTag = ""
					}
				} else if opening {
					skipDepth++
				}
			}
		} else if opening && (name == "script" || name == "style" || name == "noscript" || name == "template") {
			skipTag = name
			skipDepth = 1
		}
		pos = close + 1
	}
	return html.UnescapeString(output.String())
}

func htmlTagEndURLReader(input string, start int) int {
	var quote byte
	for i := start; i < len(input); i++ {
		if quote != 0 {
			if input[i] == quote {
				quote = 0
			}
			continue
		}
		if input[i] == '\'' || input[i] == '"' {
			quote = input[i]
		} else if input[i] == '>' {
			return i
		}
	}
	return -1
}

func htmlTagNameURLReader(tag string) (name string, closing, opening bool) {
	tag = strings.TrimLeft(tag, " \t\r\n")
	if tag == "" || tag[0] == '!' || tag[0] == '?' {
		return "", false, false
	}
	if tag[0] == '/' {
		closing = true
		tag = strings.TrimLeft(tag[1:], " \t\r\n")
	}
	end := 0
	for end < len(tag) && (tag[end] >= 'a' && tag[end] <= 'z' || tag[end] >= '0' && tag[end] <= '9' || tag[end] == ':' || tag[end] == '-' || tag[end] == '_') {
		end++
	}
	if end == 0 {
		return "", closing, false
	}
	return tag[:end], closing, !closing
}

func asciiLowerURLReader(input string) string {
	bytes := []byte(input)
	for i, value := range bytes {
		if value >= 'A' && value <= 'Z' {
			bytes[i] = value + ('a' - 'A')
		}
	}
	return string(bytes)
}

func cleanURLReaderText(input string) string {
	var output strings.Builder
	space := false
	for _, value := range input {
		if unicode.IsSpace(value) {
			space = output.Len() > 0
			continue
		}
		if unicode.IsControl(value) || value == utf8.RuneError {
			continue
		}
		if space {
			output.WriteByte(' ')
			space = false
		}
		output.WriteRune(value)
	}
	return strings.TrimSpace(output.String())
}

func truncateURLReaderText(input string) string {
	if utf8.RuneCountInString(input) <= urlReaderMaxRunes {
		return input
	}
	runes := []rune(input)
	return string(runes[:urlReaderMaxRunes])
}
