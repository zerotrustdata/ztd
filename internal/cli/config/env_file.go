// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// LoadEnvFile exports the KEY=VALUE lines of the env file at path (the
// project-root .env, template .env.example) into the process environment, so
// every reader resolves them through the typed constants.EnvVar keys
// (INV-ENV-04). A missing file loads nothing. Every key must be a registry
// secret or user_endpoint key; a variable already in the environment wins. The
// file holds secrets, so on Linux and macOS it must be owned by the user or root
// with mode 0600 or 0400. Values are literal: blank lines and # comments are
// skipped, a leading "export " is accepted, and one pair of matching
// surrounding quotes is removed.
func LoadEnvFile(path string) error {
	f, err := openEnvFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cli config: open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cli config: stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cli config: %s: %w", path, constants.ErrEnvFileInvalid)
	}
	if err := validateEnvFileMetadata(info); err != nil {
		return fmt.Errorf("cli config: %s is mode %04o: %w", path, info.Mode().Perm(), err)
	}

	allowed := userEnvKeys()

	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("cli config: %s:%d: %w", path, n, constants.ErrEnvFileMalformed)
		}
		if !allowed[key] {
			return fmt.Errorf("cli config: %s:%d: %s: %w", path, n, key, constants.ErrEnvFileUnknownKey)
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, unquoteEnvValue(strings.TrimSpace(value))); err != nil {
			return fmt.Errorf("cli config: %s:%d: set %s: %w", path, n, key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("cli config: read %s: %w", path, err)
	}
	return nil
}

// userEnvKeys is the set of registry keys an env file may supply: every
// secret and user_endpoint key.
func userEnvKeys() map[string]bool {
	keys := make(map[string]bool, len(constants.SecretEnvVars)+len(constants.UserEndpointEnvVars))
	for _, key := range constants.SecretEnvVars {
		keys[string(key)] = true
	}
	for _, key := range constants.UserEndpointEnvVars {
		keys[string(key)] = true
	}
	return keys
}

// WithoutUserEnv returns environ without the secret and user_endpoint keys, so
// test and smoke child processes run isolated from the developer's .env and
// shell credentials.
func WithoutUserEnv(environ []string) []string {
	withheld := userEnvKeys()
	env := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if !withheld[key] {
			env = append(env, kv)
		}
	}
	return env
}

func unquoteEnvValue(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}

// InferenceOllamaEndpoint resolves the Ollama provider endpoint for the
// --inference-ollama-endpoint flags: the flag value, else ZTD_OLLAMA_ENDPOINT,
// else blank.
func InferenceOllamaEndpoint(flag string) string {
	return cmp.Or(strings.TrimSpace(flag), strings.TrimSpace(os.Getenv(string(constants.EnvVar.OllamaEndpoint))))
}
