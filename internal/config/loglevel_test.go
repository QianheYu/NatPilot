package config

import "testing"

// TestDefaultLogLevelIsOff locks in the requested default: NatPilot records
// nothing until the user turns logging on, so 日志与审计 starts empty.
func TestDefaultLogLevelIsOff(t *testing.T) {
	c := Default()
	if c.Log.Level != "off" {
		t.Fatalf("default log.level = %q, want off", c.Log.Level)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if c.Log.Level != "off" {
		t.Fatalf("Validate() rewrote log.level to %q", c.Log.Level)
	}
}

func TestValidateLogLevelAcceptsOff(t *testing.T) {
	for _, raw := range []string{"off", "OFF", "none", "silent", "disabled"} {
		c := Default()
		c.Log.Level = raw
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate() rejected %q: %v", raw, err)
		}
		if c.Log.Level != "off" {
			t.Fatalf("Validate(%q) normalised to %q, want off", raw, c.Log.Level)
		}
	}
}

func TestValidateLogLevelRejectsGarbage(t *testing.T) {
	c := Default()
	c.Log.Level = "verbose"
	if err := c.Validate(); err == nil {
		t.Fatal("expected an error for an unknown log level")
	}
}

// TestLogLevelOverrideIsAccepted makes sure the settings endpoint (which writes
// through SetOverride) can flip the level at runtime.
func TestLogLevelOverrideIsAccepted(t *testing.T) {
	c := Default()
	if !c.SetOverride("log.level", "info") {
		t.Fatal("SetOverride rejected log.level")
	}
	if c.Log.Level != "info" {
		t.Fatalf("log.level = %q, want info", c.Log.Level)
	}
	if !c.SetOverride("log.level", "off") {
		t.Fatal("SetOverride rejected log.level=off")
	}
	if c.Log.Level != "off" {
		t.Fatalf("log.level = %q, want off", c.Log.Level)
	}
}
