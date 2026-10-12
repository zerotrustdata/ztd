// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// versionsync keeps every first-party version declaration equal to the
// platform VERSION file. VERSION is the single source of truth. Declarations:
//
//   - protocol/docs: "Version: v<VERSION>" spec headers
//   - first-party package.json files and their package-lock.json root versions
//
// Usage:
//
//	go run ./internal/tools/versionsync -check   exit 1 and list every drifted declaration (CI)
//	go run ./internal/tools/versionsync -write   rewrite drifted declarations in place (make release)
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	npmPackages  = []string{"console", "evaluation-explorer", "ztd-adapter", "protocol/node", "website"}
	protocolDocs = []string{"a2a.md", "constants.md", "mcp.md", "spec.md"}

	semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	// The top-level "version" key is the only one indented by exactly two spaces.
	npmTopLevelVersion = `(?m)^  "version": "(?P<v>[^"]+)"`
	npmRootPackage     = `(?m)^    "": \{\n(?:      .*\n)*?      "version": "(?P<v>[^"]+)"`
	docVersionHeader   = `(?m)^Version: v(?P<v>\S+)`
)

// declaration is one version span in one file. The pattern must match
// exactly once; its named group "v" is the version span.
type declaration struct {
	label   string
	relPath string
	pattern *regexp.Regexp
}

func newDeclaration(label, relPath, pattern string) declaration {
	return declaration{label: label, relPath: relPath, pattern: regexp.MustCompile(pattern)}
}

// span returns the [start, end) byte offsets of the version group, or ok=false
// when the pattern does not match exactly once.
func (d declaration) span(text string) (int, int, bool) {
	matches := d.pattern.FindAllStringSubmatchIndex(text, 2)
	if len(matches) != 1 {
		return 0, 0, false
	}
	group := d.pattern.SubexpIndex("v")
	return matches[0][2*group], matches[0][2*group+1], true
}

// read returns the declared version, or "" when the declaration is missing or
// ambiguous.
func (d declaration) read(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, d.relPath))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", d.relPath, err)
	}
	text := string(raw)
	start, end, ok := d.span(text)
	if !ok {
		return "", nil
	}
	return text[start:end], nil
}

func (d declaration) write(root, version string) error {
	path := filepath.Join(root, d.relPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", d.relPath, err)
	}
	text := string(raw)
	start, end, ok := d.span(text)
	if !ok {
		return fmt.Errorf("%s: expected exactly one version declaration", d.label)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", d.relPath, err)
	}
	if err := os.WriteFile(path, []byte(text[:start]+version+text[end:]), info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", d.relPath, err)
	}
	return nil
}

func main() {
	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "versionsync: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], root, os.Stdout, os.Stderr))
}

func run(args []string, root string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("versionsync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check", false, "exit 1 and list every drifted declaration")
	write := flags.Bool("write", false, "rewrite drifted declarations in place")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *check == *write {
		fmt.Fprintln(stderr, "versionsync: exactly one of -check or -write is required")
		return 2
	}

	version, err := platformVersion(root)
	if err != nil {
		fmt.Fprintf(stderr, "versionsync: %v\n", err)
		return 1
	}
	decls, err := declarations(root)
	if err != nil {
		fmt.Fprintf(stderr, "versionsync: %v\n", err)
		return 1
	}
	drift, err := findDrift(root, decls, version)
	if err != nil {
		fmt.Fprintf(stderr, "versionsync: %v\n", err)
		return 1
	}

	if *write {
		for _, d := range drift {
			if err := d.decl.write(root, version); err != nil {
				fmt.Fprintf(stderr, "versionsync: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "synced %s: %s -> %s\n", d.decl.label, d.found, version)
		}
		if drift, err = findDrift(root, decls, version); err != nil {
			fmt.Fprintf(stderr, "versionsync: %v\n", err)
			return 1
		}
	}

	if len(drift) > 0 {
		fmt.Fprintln(stderr, "Version sync FAILED (VERSION is the source of truth):")
		for _, d := range drift {
			fmt.Fprintf(stderr, "  - %s: %q != %s\n", d.decl.label, d.found, version)
		}
		fmt.Fprintln(stderr, "Run: make version-sync")
		return 1
	}
	fmt.Fprintf(stdout, "Version sync OK: %s (%d declarations)\n", version, len(decls))
	return 0
}

type drifted struct {
	decl  declaration
	found string
}

func findDrift(root string, decls []declaration, version string) ([]drifted, error) {
	var out []drifted
	for _, d := range decls {
		found, err := d.read(root)
		if err != nil {
			return nil, err
		}
		if found != version {
			out = append(out, drifted{decl: d, found: found})
		}
	}
	return out, nil
}

func platformVersion(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "", fmt.Errorf("read VERSION: %w", err)
	}
	trimmed := strings.TrimSpace(string(raw))
	version := strings.TrimPrefix(trimmed, "v")
	if !semverPattern.MatchString(version) {
		return "", fmt.Errorf("VERSION file is not semver: %q", trimmed)
	}
	return version, nil
}

func declarations(root string) ([]declaration, error) {
	decls := make([]declaration, 0, len(protocolDocs)+3*len(npmPackages))
	for _, doc := range protocolDocs {
		rel := "protocol/docs/" + doc
		decls = append(decls, newDeclaration(rel+" Version header", rel, docVersionHeader))
	}
	npm, err := npmDeclarations(root)
	if err != nil {
		return nil, err
	}
	return append(decls, npm...), nil
}

type packageManifest struct {
	Version *string `json:"version"`
}

type packageLock struct {
	Version  *string                    `json:"version"`
	Packages map[string]packageManifest `json:"packages"`
}

func npmDeclarations(root string) ([]declaration, error) {
	var out []declaration
	for _, pkg := range npmPackages {
		manifestRel := pkg + "/package.json"
		var manifest packageManifest
		if err := readJSON(root, manifestRel, &manifest); err != nil {
			return nil, err
		}
		if manifest.Version != nil {
			out = append(out, newDeclaration(manifestRel, manifestRel, npmTopLevelVersion))
		}

		lockRel := pkg + "/package-lock.json"
		var lock packageLock
		if err := readJSON(root, lockRel, &lock); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if lock.Version != nil {
			out = append(out, newDeclaration(lockRel+" root", lockRel, npmTopLevelVersion))
		}
		if rootPkg, ok := lock.Packages[""]; ok && rootPkg.Version != nil {
			out = append(out, newDeclaration(lockRel+` packages[""]`, lockRel, npmRootPackage))
		}
	}
	return out, nil
}

func readJSON(root, rel string, dst any) error {
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode %s: %w", rel, err)
	}
	return nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above working directory")
		}
		dir = parent
	}
}
