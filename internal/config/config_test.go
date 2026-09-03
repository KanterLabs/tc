package config

import (
	"strings"
	"testing"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"HELM_ADDR", "HELM_DB", "HELM_AUTH_MODE", "HELM_PUBLIC_ORIGIN",
		"HELM_ADMIN_EMAIL", "HELM_RELEASE_SHA", "HELM_CLOUDFLARE_ISSUER", "HELM_CF_ACCESS_ISSUER",
		"HELM_CLOUDFLARE_AUDIENCE", "HELM_CLOUDFLARE_AUD", "HELM_CF_ACCESS_AUDIENCES", "HELM_CLOUDFLARE_AUDIENCES",
		"HELM_CLOUDFLARE_JWKS_URL", "HELM_CF_ACCESS_JWKS_URL", "HELM_CLOUDFLARE_CERTS_URL", "HELM_SECURE_COOKIES",
		"HELM_DEMO_SEED", "ROADMAP_ADDR", "ROADMAP_DB", "ROADMAP_AUTH_MODE", "ROADMAP_PUBLIC_ORIGIN",
		"ROADMAP_ADMIN_EMAIL", "ROADMAP_RELEASE_SHA", "ROADMAP_CLOUDFLARE_ISSUER", "ROADMAP_CF_ACCESS_ISSUER",
		"ROADMAP_CLOUDFLARE_AUDIENCE", "ROADMAP_CLOUDFLARE_AUD", "ROADMAP_CF_ACCESS_AUDIENCES", "ROADMAP_CLOUDFLARE_AUDIENCES",
		"ROADMAP_CLOUDFLARE_JWKS_URL", "ROADMAP_CF_ACCESS_JWKS_URL", "ROADMAP_CLOUDFLARE_CERTS_URL", "ROADMAP_SECURE_COOKIES",
		"ROADMAP_DEMO_SEED",
	} {
		t.Setenv(name, "")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HELM_PUBLIC_ORIGIN", "http://localhost:8080/")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("default address = %q, want :8080", cfg.Addr)
	}
	if cfg.DB != "data/roadmap.db" {
		t.Fatalf("default database = %q, want data/roadmap.db", cfg.DB)
	}
	if cfg.AuthMode != "local" {
		t.Fatalf("default auth mode = %q, want local", cfg.AuthMode)
	}
	if cfg.PublicOrigin != "http://localhost:8080" {
		t.Fatalf("normalized default origin = %q", cfg.PublicOrigin)
	}
	if !cfg.SecureCookies {
		t.Fatal("secure cookies defaulted to false")
	}
	if cfg.DemoSeed {
		t.Fatal("demo seed defaulted to true")
	}
}

func TestFromEnvCanonicalAndLegacySettings(t *testing.T) {
	tests := []struct {
		name      string
		canonical string
		legacy    string
		value     string
		want      string
		field     func(Config) string
	}{
		{
			name: "address", canonical: "HELM_ADDR", legacy: "ROADMAP_ADDR",
			value: "127.0.0.1:9090", want: "127.0.0.1:9090",
			field: func(cfg Config) string { return cfg.Addr },
		},
		{
			name: "database", canonical: "HELM_DB", legacy: "ROADMAP_DB",
			value: "/var/lib/helm/roadmap.db", want: "/var/lib/helm/roadmap.db",
			field: func(cfg Config) string { return cfg.DB },
		},
		{
			name: "auth mode", canonical: "HELM_AUTH_MODE", legacy: "ROADMAP_AUTH_MODE",
			value: "disabled", want: "disabled",
			field: func(cfg Config) string { return cfg.AuthMode },
		},
		{
			name: "public origin", canonical: "HELM_PUBLIC_ORIGIN", legacy: "ROADMAP_PUBLIC_ORIGIN",
			value: " https://helm.example/ ", want: "https://helm.example",
			field: func(cfg Config) string { return cfg.PublicOrigin },
		},
		{
			name: "administrator", canonical: "HELM_ADMIN_EMAIL", legacy: "ROADMAP_ADMIN_EMAIL",
			value: "owner@example.com", want: "owner@example.com",
			field: func(cfg Config) string { return cfg.AdminEmail },
		},
		{
			name: "release SHA", canonical: "HELM_RELEASE_SHA", legacy: "ROADMAP_RELEASE_SHA",
			value: strings.Repeat("a", 40), want: strings.Repeat("a", 40),
			field: func(cfg Config) string { return cfg.ReleaseSHA },
		},
		{
			name: "secure cookies", canonical: "HELM_SECURE_COOKIES", legacy: "ROADMAP_SECURE_COOKIES",
			value: "false", want: "false",
			field: func(cfg Config) string {
				if cfg.SecureCookies {
					return "true"
				}
				return "false"
			},
		},
		{
			name: "demo seed", canonical: "HELM_DEMO_SEED", legacy: "ROADMAP_DEMO_SEED",
			value: "true", want: "true",
			field: func(cfg Config) string {
				if cfg.DemoSeed {
					return "true"
				}
				return "false"
			},
		},
		{
			name: "Cloudflare issuer", canonical: "HELM_CLOUDFLARE_ISSUER", legacy: "ROADMAP_CLOUDFLARE_ISSUER",
			value: "https://team.cloudflareaccess.com", want: "https://team.cloudflareaccess.com",
			field: func(cfg Config) string { return cfg.CloudflareIssuer },
		},
		{
			name: "Cloudflare audience", canonical: "HELM_CLOUDFLARE_AUDIENCE", legacy: "ROADMAP_CLOUDFLARE_AUDIENCE",
			value: "ui-audience", want: "ui-audience",
			field: func(cfg Config) string { return cfg.CloudflareAudience },
		},
		{
			name: "Cloudflare audiences", canonical: "HELM_CF_ACCESS_AUDIENCES", legacy: "ROADMAP_CF_ACCESS_AUDIENCES",
			value: "ui-audience, api-audience", want: "ui-audience,api-audience",
			field: func(cfg Config) string { return strings.Join(cfg.CloudflareAudiences, ",") },
		},
		{
			name: "Cloudflare JWKS URL", canonical: "HELM_CLOUDFLARE_JWKS_URL", legacy: "ROADMAP_CLOUDFLARE_JWKS_URL",
			value: "https://team.cloudflareaccess.com/certs", want: "https://team.cloudflareaccess.com/certs",
			field: func(cfg Config) string { return cfg.CloudflareJWKSURL },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, variant := range []string{"canonical", "legacy", "equal"} {
				t.Run(variant, func(t *testing.T) {
					clearConfigEnv(t)
					t.Setenv("HELM_AUTH_MODE", "disabled")
					if tt.canonical != "HELM_ADDR" {
						t.Setenv("HELM_ADDR", "127.0.0.1:8080")
					}
					switch variant {
					case "canonical":
						t.Setenv(tt.canonical, tt.value)
					case "legacy":
						t.Setenv(tt.legacy, tt.value)
					case "equal":
						t.Setenv(tt.canonical, tt.value)
						t.Setenv(tt.legacy, tt.value)
					}
					cfg, err := FromEnv()
					if err != nil {
						t.Fatal(err)
					}
					if got := tt.field(cfg); got != tt.want {
						t.Fatalf("%s value = %q, want %q", variant, got, tt.want)
					}
				})
			}
		})
	}
}

func TestFromEnvConflictingSettingsFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		canonical  string
		legacy     string
		leftValue  string
		rightValue string
	}{
		{name: "address", canonical: "HELM_ADDR", legacy: "ROADMAP_ADDR", leftValue: "127.0.0.1:8080", rightValue: "127.0.0.1:9090"},
		{name: "database", canonical: "HELM_DB", legacy: "ROADMAP_DB", leftValue: "/var/lib/helm-a.db", rightValue: "/var/lib/helm-b.db"},
		{name: "auth mode", canonical: "HELM_AUTH_MODE", legacy: "ROADMAP_AUTH_MODE", leftValue: "disabled", rightValue: "local"},
		{name: "public origin", canonical: "HELM_PUBLIC_ORIGIN", legacy: "ROADMAP_PUBLIC_ORIGIN", leftValue: "https://helm-a.example", rightValue: "https://helm-b.example"},
		{name: "administrator", canonical: "HELM_ADMIN_EMAIL", legacy: "ROADMAP_ADMIN_EMAIL", leftValue: "a@example.com", rightValue: "b@example.com"},
		{name: "release SHA", canonical: "HELM_RELEASE_SHA", legacy: "ROADMAP_RELEASE_SHA", leftValue: strings.Repeat("a", 40), rightValue: strings.Repeat("b", 40)},
		{name: "secure cookies", canonical: "HELM_SECURE_COOKIES", legacy: "ROADMAP_SECURE_COOKIES", leftValue: "true", rightValue: "false"},
		{name: "demo seed", canonical: "HELM_DEMO_SEED", legacy: "ROADMAP_DEMO_SEED", leftValue: "true", rightValue: "false"},
		{name: "issuer", canonical: "HELM_CLOUDFLARE_ISSUER", legacy: "ROADMAP_CLOUDFLARE_ISSUER", leftValue: "https://issuer-a.example", rightValue: "https://issuer-b.example"},
		{name: "audience", canonical: "HELM_CLOUDFLARE_AUDIENCE", legacy: "ROADMAP_CLOUDFLARE_AUDIENCE", leftValue: "audience-a", rightValue: "audience-b"},
		{name: "audiences", canonical: "HELM_CF_ACCESS_AUDIENCES", legacy: "ROADMAP_CF_ACCESS_AUDIENCES", leftValue: "ui-a,api-a", rightValue: "ui-b,api-b"},
		{name: "JWKS URL", canonical: "HELM_CLOUDFLARE_JWKS_URL", legacy: "ROADMAP_CLOUDFLARE_JWKS_URL", leftValue: "https://jwks-a.example/certs", rightValue: "https://jwks-b.example/certs"},
		{name: "issuer compatibility aliases", canonical: "ROADMAP_CLOUDFLARE_ISSUER", legacy: "ROADMAP_CF_ACCESS_ISSUER", leftValue: "https://issuer-a.example", rightValue: "https://issuer-b.example"},
		{name: "audience compatibility aliases", canonical: "ROADMAP_CLOUDFLARE_AUDIENCE", legacy: "ROADMAP_CLOUDFLARE_AUD", leftValue: "audience-a", rightValue: "audience-b"},
		{name: "audiences compatibility aliases", canonical: "ROADMAP_CF_ACCESS_AUDIENCES", legacy: "ROADMAP_CLOUDFLARE_AUDIENCES", leftValue: "ui-a,api-a", rightValue: "ui-b,api-b"},
		{name: "JWKS compatibility aliases", canonical: "ROADMAP_CLOUDFLARE_JWKS_URL", legacy: "ROADMAP_CF_ACCESS_JWKS_URL", leftValue: "https://jwks-a.example/certs", rightValue: "https://jwks-b.example/certs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("HELM_AUTH_MODE", "disabled")
			if tt.canonical != "HELM_ADDR" && tt.legacy != "ROADMAP_ADDR" {
				t.Setenv("HELM_ADDR", "127.0.0.1:8080")
			}
			t.Setenv(tt.canonical, tt.leftValue)
			t.Setenv(tt.legacy, tt.rightValue)
			_, err := FromEnv()
			if err == nil {
				t.Fatal("conflicting settings unexpectedly accepted")
			}
			message := err.Error()
			if !strings.Contains(message, tt.canonical) || !strings.Contains(message, tt.legacy) {
				t.Fatalf("conflict error = %q, want both variable names", message)
			}
			if strings.Contains(message, tt.leftValue) || strings.Contains(message, tt.rightValue) {
				t.Fatalf("conflict error leaked a value: %q", message)
			}
		})
	}
}

func TestFromEnvExistingCloudflareAliases(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
		field func(Config) string
		want  string
	}{
		{
			name: "Access issuer", env: "ROADMAP_CF_ACCESS_ISSUER", value: "https://team.cloudflareaccess.com",
			field: func(cfg Config) string { return cfg.CloudflareIssuer }, want: "https://team.cloudflareaccess.com",
		},
		{
			name: "short audience", env: "ROADMAP_CLOUDFLARE_AUD", value: "ui-audience",
			field: func(cfg Config) string { return cfg.CloudflareAudience }, want: "ui-audience",
		},
		{
			name: "Access audiences", env: "ROADMAP_CF_ACCESS_AUDIENCES", value: "ui-audience,api-audience",
			field: func(cfg Config) string { return strings.Join(cfg.CloudflareAudiences, ",") }, want: "ui-audience,api-audience",
		},
		{
			name: "Cloudflare audiences", env: "ROADMAP_CLOUDFLARE_AUDIENCES", value: "ui-audience,api-audience",
			field: func(cfg Config) string { return strings.Join(cfg.CloudflareAudiences, ",") }, want: "ui-audience,api-audience",
		},
		{
			name: "Access JWKS URL", env: "ROADMAP_CF_ACCESS_JWKS_URL", value: "https://team.cloudflareaccess.com/certs",
			field: func(cfg Config) string { return cfg.CloudflareJWKSURL }, want: "https://team.cloudflareaccess.com/certs",
		},
		{
			name: "Cloudflare certs URL", env: "ROADMAP_CLOUDFLARE_CERTS_URL", value: "https://team.cloudflareaccess.com/certs",
			field: func(cfg Config) string { return cfg.CloudflareJWKSURL }, want: "https://team.cloudflareaccess.com/certs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("HELM_AUTH_MODE", "disabled")
			t.Setenv("HELM_ADDR", "127.0.0.1:8080")
			t.Setenv(tt.env, tt.value)
			cfg, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if got := tt.field(cfg); got != tt.want {
				t.Fatalf("alias value = %q, want %q", got, tt.want)
			}
		})
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
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "HELM_PUBLIC_ORIGIN") {
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
