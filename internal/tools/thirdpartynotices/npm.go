// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// npmApp is a frontend project whose production dependencies are embedded in
// the ztd binary.
type npmApp struct {
	Name string // label shown in the notices file
	Dir  string // repo-relative directory holding package-lock.json
}

var npmApps = []npmApp{
	{Name: "console", Dir: "console"},
	{Name: "evaluation-explorer", Dir: "evaluation-explorer"},
}

// NPMPackage is one production npm package, merged across apps when the same
// name@version is used by more than one.
type NPMPackage struct {
	Name    string
	Version string
	License string
	Apps    []string
	Files   []LicenseFile // empty when node_modules was not installed
	// HasNodeModules records whether the owning app had node_modules on disk.
	HasNodeModules bool
}

type npmLockFile struct {
	Packages map[string]npmLockPackage `json:"packages"`
}

type npmLockPackage struct {
	Version string          `json:"version"`
	Dev     bool            `json:"dev"`
	Link    bool            `json:"link"`
	License json.RawMessage `json:"license"`
}

// parseNPMLicense accepts the string form ("MIT"), the legacy object form
// ({"type": "MIT"}), and absent values.
func parseNPMLicense(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "UNKNOWN"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s
	}
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Type != "" {
		return obj.Type
	}
	return "UNKNOWN"
}

// npmPackageName returns the package name for a lock "packages" key such as
// "node_modules/@scope/pkg" or "node_modules/a/node_modules/b".
func npmPackageName(key string) string {
	const marker = "node_modules/"
	idx := strings.LastIndex(key, marker)
	if idx < 0 {
		return ""
	}
	return key[idx+len(marker):]
}

// collectNPMPackages returns the production packages of every app, merged by
// name@version and sorted by name then version.
func collectNPMPackages(repo string, apps []npmApp) ([]NPMPackage, error) {
	merged := map[string]*NPMPackage{}
	for _, app := range apps {
		appDir := filepath.Join(repo, filepath.FromSlash(app.Dir))
		data, err := os.ReadFile(filepath.Join(appDir, "package-lock.json"))
		if err != nil {
			return nil, fmt.Errorf("read %s lock: %w", app.Name, err)
		}
		var lock npmLockFile
		if err := json.Unmarshal(data, &lock); err != nil {
			return nil, fmt.Errorf("parse %s lock: %w", app.Name, err)
		}
		hasNodeModules := dirExists(filepath.Join(appDir, "node_modules"))

		for key, pkg := range lock.Packages {
			name := npmPackageName(key)
			if name == "" || pkg.Dev || pkg.Link {
				continue
			}
			id := name + "@" + pkg.Version
			entry, ok := merged[id]
			if !ok {
				entry = &NPMPackage{
					Name:    name,
					Version: pkg.Version,
					License: parseNPMLicense(pkg.License),
				}
				if hasNodeModules {
					files, err := readLicenseFiles(filepath.Join(appDir, filepath.FromSlash(key)))
					if err != nil {
						return nil, fmt.Errorf("%s: %w", id, err)
					}
					entry.Files = files
					entry.HasNodeModules = true
				}
				merged[id] = entry
			}
			entry.Apps = appendUnique(entry.Apps, app.Name)
		}
	}

	pkgs := make([]NPMPackage, 0, len(merged))
	for _, p := range merged {
		sort.Strings(p.Apps)
		pkgs = append(pkgs, *p)
	}
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].Name != pkgs[j].Name {
			return pkgs[i].Name < pkgs[j].Name
		}
		return pkgs[i].Version < pkgs[j].Version
	})
	return pkgs, nil
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
