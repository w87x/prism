package config

import (
	"os"
	"strings"
	"testing"
)

func TestEnvOverridesAreNotPersisted(t *testing.T) {
	t.Setenv("PRISM_HOME", t.TempDir())
	t.Setenv("PRISM_LISTEN", "127.0.0.1:9999")
	c, err := Load()
	if err != nil || c.Listen != "127.0.0.1:9999" {
		t.Fatalf("override not applied: %v %v", c, err)
	}
	if err := c.SetDSN("postgres://u@h/db"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(c.path)
	if strings.Contains(string(b), "9999") || !strings.Contains(string(b), "postgres://u@h/db") || !strings.Contains(string(b), "127.0.0.1:7777") {
		t.Fatalf("config.json should keep file values, not env overrides:\n%s", b)
	}
	if st, _ := os.Stat(c.path); st.Mode().Perm() != 0o600 {
		t.Fatalf("config must be owner-only, got %v", st.Mode().Perm())
	}
}

func TestExposedAndNoAuthPersist(t *testing.T) {
	for host, want := range map[string]bool{"127.0.0.1": false, "::1": false, "localhost": false, "0.0.0.0": true, "100.64.0.5": true, "mac.local": true} {
		if Exposed(host) != want {
			t.Fatalf("Exposed(%q) = %v, want %v", host, !want, want)
		}
	}
	t.Setenv("PRISM_HOME", t.TempDir())
	c, _ := Load()
	c.NoAuth = true
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2, _ := Load()
	if !c2.NoAuth {
		t.Fatal("no_auth did not round-trip through config.json")
	}
}
