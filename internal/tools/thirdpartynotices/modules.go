// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GoModule is one module recorded in vendor/modules.txt.
type GoModule struct {
	Path    string // import path; also the directory under vendor/
	Version string // empty for local-directory replacements
	// Replacement is the "path version" target when the module is replaced.
	Replacement string
	Files       []LicenseFile
}

// LicenseFile is a license or notice file copied from a module root.
type LicenseFile struct {
	Name string
	Text string
}

// parseModulesTxt parses vendor/modules.txt. Module lines have the form
//
//	# path version
//	# path version => replacement version
//	# path => local-directory
//
// Every other line is either a "## explicit" annotation or a package path
// and is ignored.
func parseModulesTxt(r io.Reader) ([]GoModule, error) {
	var mods []GoModule
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		body := strings.TrimPrefix(line, "# ")
		left, right, replaced := strings.Cut(body, " => ")
		lf := strings.Fields(left)
		if len(lf) == 0 || len(lf) > 2 {
			return nil, fmt.Errorf("malformed module line %q", line)
		}
		mod := GoModule{Path: lf[0]}
		if len(lf) == 2 {
			mod.Version = lf[1]
		}
		if replaced {
			mod.Replacement = strings.TrimSpace(right)
		}
		mods = append(mods, mod)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read modules.txt: %w", err)
	}
	sort.SliceStable(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// collectGoModules reads vendor/modules.txt under repo and attaches the
// license and notice files found in each module root.
func collectGoModules(repo string) ([]GoModule, error) {
	vendorDir := filepath.Join(repo, "vendor")
	f, err := os.Open(filepath.Join(vendorDir, "modules.txt"))
	if err != nil {
		return nil, fmt.Errorf("open modules.txt: %w", err)
	}
	defer f.Close()

	mods, err := parseModulesTxt(f)
	if err != nil {
		return nil, err
	}
	for i := range mods {
		files, err := readLicenseFiles(filepath.Join(vendorDir, filepath.FromSlash(mods[i].Path)))
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", mods[i].Path, err)
		}
		mods[i].Files = files
	}
	return mods, nil
}

// licensePrefixes are the upper-case file-name prefixes treated as license or
// notice material in a module or package root.
var licensePrefixes = []string{"LICENSE", "LICENCE", "COPYING", "NOTICE"}

// isLicenseFileName reports whether name looks like a license or notice file.
func isLicenseFileName(name string) bool {
	upper := strings.ToUpper(name)
	for _, p := range licensePrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// readLicenseFiles returns the license and notice files directly inside dir,
// sorted by name. A missing directory yields no files and no error.
func readLicenseFiles(dir string) ([]LicenseFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var files []LicenseFile
	for _, e := range entries {
		if e.IsDir() || !isLicenseFileName(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		files = append(files, LicenseFile{Name: e.Name(), Text: string(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}
