package api

import "testing"

// The web console submits real booleans and numbers; decoding straight into
// map[string]string used to reject every settings save with HTTP 400.
func TestSettingValueToString(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
		ok   bool
	}{
		{"string", "abc", "abc", true},
		{"bool true", true, "true", true},
		{"bool false", false, "false", true},
		{"float whole", float64(800), "800", true},
		{"float fraction", 1.5, "1.5", true},
		{"int", 42, "42", true},
		{"nil", nil, "", true},
		{"any slice", []any{"igd2", "pcp"}, "igd2,pcp", true},
		{"string slice", []string{"a", "b"}, "a,b", true},
		{"nested object", map[string]any{"x": 1}, "", false},
	}
	for _, c := range cases {
		got, ok := settingValueToString(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: settingValueToString(%v) = (%q, %v), want (%q, %v)",
				c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}

// Bad values used to be written into the settings table and only break the next
// start-up, so they are rejected up front.
func TestValidateSetting(t *testing.T) {
	valid := []struct{ k, v string }{
		{"http.mode", "desktop"},
		{"http.mode", "server"},
		{"http.mode", "AUTO"},
		{"http.port", "8787"},
		{"http.port", "0"},
		{"mapper.dry_run", "true"},
	}
	for _, c := range valid {
		if err := validateSetting(c.k, c.v); err != nil {
			t.Errorf("validateSetting(%q, %q) = %v, want nil", c.k, c.v, err)
		}
	}

	invalid := []struct{ k, v string }{
		{"http.mode", "turbo"},
		{"http.mode", ""},
		{"http.port", "70000"},
		{"http.port", "abc"},
	}
	for _, c := range invalid {
		if err := validateSetting(c.k, c.v); err == nil {
			t.Errorf("validateSetting(%q, %q) = nil, want an error", c.k, c.v)
		}
	}
}

// Regression guard for the live auth bypass: these keys change the security or
// network boundary of the process, so they must never be hot-applied. One PATCH
// used to be enough to turn a running server into an unauthenticated console.
func TestSecuritySettingsRequireRestart(t *testing.T) {
	for _, k := range []string{
		"http.mode", "http.host", "http.port",
		"auth.enabled", "auth.require_for_web",
	} {
		if !restartOnlySettings[k] {
			t.Errorf("%q must be restart-only (it weakens the security boundary)", k)
		}
	}
	// Everyday tuning must keep applying without a restart.
	for _, k := range []string{"mapper.dry_run", "mapper.enabled", "scanner.timeout_ms"} {
		if restartOnlySettings[k] {
			t.Errorf("%q should apply immediately, not require a restart", k)
		}
	}
}

// allowedSetting is the whitelist gate; secrets must stay writable only through
// the dedicated credential endpoints.
func TestSecretKeysAreNotPlainSettings(t *testing.T) {
	for _, k := range []string{"auth.bootstrap_pass", "http.session_secret", "foo.token"} {
		if !isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = false, want true", k)
		}
	}
	if isSecretKey("mapper.dry_run") {
		t.Errorf("isSecretKey(mapper.dry_run) = true, want false")
	}
}
