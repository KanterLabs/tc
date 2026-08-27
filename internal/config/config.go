// Package config contains environment-backed server configuration.
package config

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr         string
	DB           string
	AuthMode     string
	PublicOrigin string
	AdminEmail   string
	// ReleaseSHA is an optional immutable deployment revision exposed by
	// health/discovery responses. It is accepted only in canonical git SHA
	// form so an env-file typo cannot masquerade as a release identifier.
	ReleaseSHA string
	// CloudflareIssuer and CloudflareAudience identify the Access team and
	// applications which are allowed to authenticate human requests. The JWKS
	// URL is optional; when omitted it is derived from the issuer's
	// /cdn-cgi/access/certs endpoint.
	CloudflareIssuer    string
	CloudflareAudience  string
	CloudflareAudiences []string
	CloudflareJWKSURL   string
	SecureCookies       bool
	DemoSeed            bool
}

func FromEnv() (Config, error) {
	c := Config{
		Addr:               envOr("ROADMAP_ADDR", ":8080"),
		DB:                 envOr("ROADMAP_DB", "data/roadmap.db"),
		AuthMode:           strings.ToLower(envOr("ROADMAP_AUTH_MODE", "local")),
		PublicOrigin:       strings.TrimRight(strings.TrimSpace(os.Getenv("ROADMAP_PUBLIC_ORIGIN")), "/"),
		AdminEmail:         strings.TrimSpace(os.Getenv("ROADMAP_ADMIN_EMAIL")),
		ReleaseSHA:         strings.TrimSpace(os.Getenv("ROADMAP_RELEASE_SHA")),
		CloudflareIssuer:   envOrAny("ROADMAP_CLOUDFLARE_ISSUER", "ROADMAP_CF_ACCESS_ISSUER"),
		CloudflareAudience: envOrAny("ROADMAP_CLOUDFLARE_AUDIENCE", "ROADMAP_CLOUDFLARE_AUD"),
		CloudflareJWKSURL:  envOrAny("ROADMAP_CLOUDFLARE_JWKS_URL", "ROADMAP_CF_ACCESS_JWKS_URL", "ROADMAP_CLOUDFLARE_CERTS_URL"),
		SecureCookies:      true,
	}
	if value := envOrAny("ROADMAP_CF_ACCESS_AUDIENCES", "ROADMAP_CLOUDFLARE_AUDIENCES"); value != "" {
		for _, audience := range strings.Split(value, ",") {
			if audience = strings.TrimSpace(audience); audience != "" {
				c.CloudflareAudiences = append(c.CloudflareAudiences, audience)
			}
		}
	}
	if len(c.CloudflareAudiences) == 0 && c.CloudflareAudience != "" {
		c.CloudflareAudiences = []string{c.CloudflareAudience}
	}
	if len(c.CloudflareAudiences) > 0 {
		c.CloudflareAudience = c.CloudflareAudiences[0]
	}
	if value := strings.TrimSpace(os.Getenv("ROADMAP_SECURE_COOKIES")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("ROADMAP_SECURE_COOKIES must be true or false: %w", err)
		}
		c.SecureCookies = parsed
	}
	if value := strings.TrimSpace(os.Getenv("ROADMAP_DEMO_SEED")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("ROADMAP_DEMO_SEED must be true or false: %w", err)
		}
		c.DemoSeed = parsed
	}
	if c.AuthMode != "local" && c.AuthMode != "cloudflare" && c.AuthMode != "disabled" {
		return Config{}, fmt.Errorf("ROADMAP_AUTH_MODE must be local, cloudflare, or disabled")
	}
	if c.ReleaseSHA != "" && !validReleaseSHA(c.ReleaseSHA) {
		return Config{}, fmt.Errorf("ROADMAP_RELEASE_SHA must be 40 lowercase hexadecimal characters")
	}
	if c.AuthMode == "local" || c.AuthMode == "cloudflare" {
		origin, err := normalizeOrigin(c.PublicOrigin, c.AuthMode == "cloudflare")
		if err != nil {
			return Config{}, err
		}
		c.PublicOrigin = origin
	}
	if c.AuthMode == "disabled" && !loopbackAddr(c.Addr) {
		return Config{}, fmt.Errorf("ROADMAP_AUTH_MODE=disabled requires ROADMAP_ADDR to bind to loopback")
	}
	if c.AuthMode == "cloudflare" {
		if !loopbackAddr(c.Addr) {
			return Config{}, fmt.Errorf("ROADMAP_AUTH_MODE=cloudflare requires ROADMAP_ADDR to bind to loopback")
		}
		if !c.SecureCookies {
			return Config{}, fmt.Errorf("ROADMAP_SECURE_COOKIES must be true in cloudflare mode")
		}
		if c.DemoSeed {
			return Config{}, fmt.Errorf("ROADMAP_DEMO_SEED must be false in cloudflare mode")
		}
		if !validEmail(c.AdminEmail) {
			return Config{}, fmt.Errorf("ROADMAP_ADMIN_EMAIL must be a valid email when ROADMAP_AUTH_MODE=cloudflare")
		}
		if c.CloudflareIssuer == "" {
			return Config{}, fmt.Errorf("ROADMAP_CLOUDFLARE_ISSUER is required when ROADMAP_AUTH_MODE=cloudflare")
		}
		if err := validateURL("ROADMAP_CLOUDFLARE_ISSUER", c.CloudflareIssuer, true); err != nil {
			return Config{}, err
		}
		if len(c.CloudflareAudiences) < 2 {
			return Config{}, fmt.Errorf("ROADMAP_CF_ACCESS_AUDIENCES must include the UI and API application audiences")
		}
		seenAudiences := make(map[string]struct{}, len(c.CloudflareAudiences))
		for _, audience := range c.CloudflareAudiences {
			if strings.TrimSpace(audience) == "" || strings.ContainsAny(audience, "\r\n,") {
				return Config{}, fmt.Errorf("ROADMAP_CF_ACCESS_AUDIENCES contains an invalid audience")
			}
			if _, exists := seenAudiences[audience]; exists {
				return Config{}, fmt.Errorf("ROADMAP_CF_ACCESS_AUDIENCES must contain distinct application audiences")
			}
			seenAudiences[audience] = struct{}{}
		}
		if c.CloudflareJWKSURL != "" {
			if err := validateURL("ROADMAP_CLOUDFLARE_JWKS_URL", c.CloudflareJWKSURL, true); err != nil {
				return Config{}, err
			}
		}
	}
	return c, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envOrAny(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func validateURL(name, value string, requireHTTPS bool) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.ForceQuery || parsed.Fragment != "" || requireHTTPS && parsed.Scheme != "https" {
		if requireHTTPS {
			return fmt.Errorf("%s must be an https URL", name)
		}
		return fmt.Errorf("%s must be an http(s) URL", name)
	}
	return nil
}

func normalizeOrigin(value string, requireHTTPS bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("ROADMAP_PUBLIC_ORIGIN is required when ROADMAP_AUTH_MODE is local or cloudflare")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(value, "#") || (parsed.Path != "" && parsed.Path != "/") || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("ROADMAP_PUBLIC_ORIGIN must be a normalized http(s) origin")
	}
	if requireHTTPS && parsed.Scheme != "https" {
		return "", fmt.Errorf("ROADMAP_PUBLIC_ORIGIN must use https in cloudflare mode")
	}
	normalized := strings.TrimRight(value, "/")
	if normalized == "" {
		return "", fmt.Errorf("ROADMAP_PUBLIC_ORIGIN must be a normalized http(s) origin")
	}
	return normalized, nil
}

func validEmail(value string) bool {
	if value == "" || len(value) > 320 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value
}

func loopbackAddr(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	host := value
	if parsedHost, _, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
	} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		host = strings.Trim(value, "[]")
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validReleaseSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
