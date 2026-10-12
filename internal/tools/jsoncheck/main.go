// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// jsoncheck verifies that every named file is well-formed JSON. It replaces
// interpreter one-liners in Makefile recipes (for example doctrines-validate).
//
//	go run ./internal/tools/jsoncheck protocol/constants/doctrine/*.json
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(paths []string, stdout, stderr io.Writer) int {
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "jsoncheck: at least one JSON file is required")
		return 2
	}
	failed := 0
	for _, path := range paths {
		if err := checkFile(path); err != nil {
			fmt.Fprintf(stderr, "jsoncheck: %v\n", err)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "valid JSON: %s\n", path)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func checkFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
