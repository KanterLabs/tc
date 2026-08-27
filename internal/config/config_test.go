package config

import (
	"strings"
	"testing"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ROADMAP_ADDR", "ROADMAP_DB", "ROADMAP_AUTH_MODE", "ROADMAP_PUBLIC_ORIGIN",
		"ROADMAP_ADMIN_EMAIL", "ROADMAP_RELEASE_SHA", "ROADMAP_CLOUDFLARE_ISSUER", "ROADMAP_CF_ACCESS_ISSUER",
		"ROADMAP_CLOUDFLARE_AUDIENCE", "ROADMAP_CLOUDFLARE_AUD", "ROADMAP_CF_ACCESS_AUDIENCES", "ROADMAP_CLOUDFLARE_AUDIENCES",
		"ROADMAP_CLOUDFLARE_JWKS_URL", "ROADMAP_CF_ACCESS_JWKS_URL", "ROADMAP_CLOUDFLARE_CERTS_URL", "ROADMAP_SECURE_COOKIES",
		"ROADMAP_DEMO_SEED",
	} {
		t.Setenv(name, "")
	}
}

func validCloudflareEnv(t *testing.T) {
	t.Helper()
	clearConfigEnv(t)
	t.Setenv("ROADMAP_AUTH_MODE", "cloudflare")
	t.Setenv("ROADMAP_ADDR", "127.0.0.1:8080")
	t.Setenv("ROADMAP_PUBLIC_ORIGIN", "https://roadmap.example/")
	t.Setenv("ROADMAP_ADMIN_EMAIL", "owner@example.com")
	t.Setenv("ROADMAP_CLOUDFLARE_ISSUER", "https://team.cloudflareaccess.com")
	t.Setenv("ROADMAP_CF_ACCESS_AUDIENCES", "ui-audience, api-audience")
}

func TestFromEnvRequiresNormalizedOrigin(t *testing.T) {
	t.Run("local missing", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("ROADMAP_AUTH_MODE", "local")
		t.Setenv("ROADMAP_ADDR", "127.0.0.1:8080")
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "ROADMAP_PUBLIC_ORIGIN") {
			t.Fatalf("missing origin error = %v", err)
		}
	})

	t.Run("normalizes trailing slash", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("ROADMAP_AUTH_MODE", "local")
		t.Setenv("ROADMAP_ADDR", "127.0.0.1:8080")
		t.Setenv("ROADMAP_PUBLIC_ORIGIN", " http://localhost:8080/// ")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.PublicOrigin != "http://localhost:8080" {
			t.Fatalf("normalized origin = %q", cfg.PublicOrigin)
		}
	})

	for _, origin := range []string{"https://roadmap.example/path", "https://roadmap.example?x=1", "https://roadmap.example?", "https://roadmap.example#", "roadmap.example", "https://"} {
		t.Run("rejects "+origin, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("ROADMAP_AUTH_MODE", "local")
			t.Setenv("ROADMAP_ADDR", "127.0.0.1:8080")
			t.Setenv("ROADMAP_PUBLIC_ORIGIN", origin)
			if _, err := FromEnv(); err == nil {
				t.Fatalf("origin %q unexpectedly accepted", origin)
			}
		})
	}
}

func TestFromEnvCloudflareRequirements(t *testing.T) {
	validCloudflareEnv(t)
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.CloudflareAudiences, []string{"ui-audience", "api-audience"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("audiences = %#v, want %#v", got, want)
	}
	if cfg.CloudflareAudience != "ui-audience" {
		t.Fatalf("legacy audience = %q", cfg.CloudflareAudience)
	}
	if cfg.PublicOrigin != "https://roadmap.example" {
		t.Fatalf("origin = %q", cfg.PublicOrigin)
	}

	for name, value := range map[string]string{
		"missing issuer": "",
		"invalid issuer": "http://team.cloudflareaccess.com",
	} {
		t.Run(name, func(t *testing.T) {
			validCloudflareEnv(t)
			t.Setenv("ROADMAP_CLOUDFLARE_ISSUER", value)
			if _, err := FromEnv(); err == nil {
				t.Fatalf("issuer %q unexpectedly accepted", value)
			}
		})
	}
	t.Run("missing administrator", func(t *testing.T) {
		validCloudflareEnv(t)
		t.Setenv("ROADMAP_ADMIN_EMAIL", "")
		if _, err := FromEnv(); err == nil {
			t.Fatal("missing administrator unexpectedly accepted")
		}
	})
	t.Run("invalid JWKS URL", func(t *testing.T) {
		validCloudflareEnv(t)
		t.Setenv("ROADMAP_CLOUDFLARE_JWKS_URL", "http://127.0.0.1/certs")
		if _, err := FromEnv(); err == nil {
			t.Fatal("insecure JWKS URL unexpectedly accepted")
		}
	})
	for name, env := range map[string]string{
		"non-loopback address": "0.0.0.0:8080",
		"insecure cookies":     "ROADMAP_SECURE_COOKIES",
		"demo seed":            "ROADMAP_DEMO_SEED",
	} {
		t.Run(name, func(t *testing.T) {
			validCloudflareEnv(t)
			switch env {
			case "ROADMAP_SECURE_COOKIES":
				t.Setenv(env, "false")
			case "ROADMAP_DEMO_SEED":
				t.Setenv(env, "true")
			default:
				t.Setenv("ROADMAP_ADDR", env)
			}
			if _, err := FromEnv(); err == nil {
				t.Fatalf("unsafe Cloudflare setting %q unexpectedly accepted", env)
			}
		})
	}
}

func TestFromEnvDisabledRequiresLoopback(t *testing.T) {
	for _, addr := range []string{"", ":8080", "0.0.0.0:8080", "[::]:8080"} {
		t.Run("rejects "+addr, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("ROADMAP_AUTH_MODE", "disabled")
			t.Setenv("ROADMAP_ADDR", addr)
			if _, err := FromEnv(); err == nil {
				t.Fatalf("disabled address %q unexpectedly accepted", addr)
			}
		})
	}
	for _, addr := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		t.Run("accepts "+addr, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("ROADMAP_AUTH_MODE", "disabled")
			t.Setenv("ROADMAP_ADDR", addr)
			if _, err := FromEnv(); err != nil {
				t.Fatalf("loopback address %q rejected: %v", addr, err)
			}
		})
	}
}

func TestFromEnvValidatesReleaseSHA(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ROADMAP_AUTH_MODE", "disabled")
	t.Setenv("ROADMAP_ADDR", "127.0.0.1:8080")
	for _, sha := range []string{"not-a-sha", strings.Repeat("a", 39), strings.Repeat("A", 40), strings.Repeat("g", 40)} {
		t.Setenv("ROADMAP_RELEASE_SHA", sha)
		if _, err := FromEnv(); err == nil {
			t.Fatalf("release SHA %q unexpectedly accepted", sha)
		}
	}
	valid := strings.Repeat("a", 40)
	t.Setenv("ROADMAP_RELEASE_SHA", valid)
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReleaseSHA != valid {
		t.Fatalf("release SHA = %q", cfg.ReleaseSHA)
	}
}
