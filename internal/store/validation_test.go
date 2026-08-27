package store

import "testing"

func TestColorValidationRejectsCSSInjection(t *testing.T) {
	valid := []string{"#2563EB", "#94a3b8", "#000000", "#ffffff"}
	for _, value := range valid {
		if !colorPattern.MatchString(value) {
			t.Fatalf("expected %q to be valid", value)
		}
	}

	invalid := []string{
		"red",
		"#fff",
		"#12345678",
		"red;background:url(https://a.co)",
		"#000000;display:none",
		"var(--purple)",
	}
	for _, value := range invalid {
		if colorPattern.MatchString(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}
