package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"

	"github.com/ekko-studio/natpilot/internal/auth"
	"github.com/ekko-studio/natpilot/internal/config"
)

func randomPasswordCLI(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "changeme-please"
	}
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

func hashPasswordCLI(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// configLoadForCLI loads configuration without touching the database.
func configLoadForCLI(flags map[string]string) (*config.Config, error) {
	return config.Load(flags["config"], flags)
}

func marshalYAML(v any) ([]byte, error) {
	return yaml.Marshal(v)
}

// configInit writes an example configuration file.
func configInit(flags map[string]string, args []string) int {
	path := flags["config"]
	if path == "" {
		if len(args) > 0 {
			path = args[0]
		}
	}
	if path == "" {
		path = "natpilot.yaml"
	}
	if _, err := os.Stat(path); err == nil {
		return fatal("%s already exists", path)
	}
	cfg := config.Default()
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return fatal("%v", err)
	}
	header := "# NatPilot configuration\n# Values can be overridden by NATPILOT_* environment variables and CLI flags.\n"
	if err := os.WriteFile(path, append([]byte(header), b...), 0o640); err != nil {
		return fatal("%v", err)
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("wrote %s\n", abs)
	return 0
}

var _ = auth.HashPassword
