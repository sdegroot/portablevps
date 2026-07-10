// Package config resolves operator inputs with a clear precedence:
// command-line flag > environment variable > (future) portablevps.toml > default.
// For now it resolves the consumer repository root; command-specific settings
// are layered on as commands are ported.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolveRepoRoot returns the absolute path of the consumer repository the CLI
// operates on: the --project flag, else $PORTABLEVPS_PROJECT, else the current
// working directory.
func ResolveRepoRoot(flagProject string) (string, error) {
	root := flagProject
	if root == "" {
		root = os.Getenv("PORTABLEVPS_PROJECT")
	}
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("determining current directory: %w", err)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving project root %q: %w", root, err)
	}
	return abs, nil
}
