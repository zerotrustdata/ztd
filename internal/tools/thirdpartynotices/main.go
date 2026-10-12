// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// thirdpartynotices writes THIRD_PARTY_NOTICES.md at the repository root.
//
// It covers the two dependency sets that ship inside the ztd binary:
//   - Go modules, read from vendor/modules.txt with each module's license and
//     notice files taken from its vendored module root.
//   - Production npm packages of console/ and evaluation-explorer/ (both are
//     embedded in the binary), read from each package-lock.json. License text
//     comes from node_modules when it is installed; otherwise only the
//     name, version, and license identifier from the lock are recorded.
//
// Output is deterministic (sorted, no timestamps) so `make
// third-party-notices-check` can diff a regenerated file against the committed
// one. Never hand-edit THIRD_PARTY_NOTICES.md.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	repo := flag.String("repo", ".", "repository root containing vendor/, console/, and evaluation-explorer/")
	out := flag.String("out", "THIRD_PARTY_NOTICES.md", "output file path")
	flag.Parse()

	if err := run(*repo, *out); err != nil {
		fmt.Fprintf(os.Stderr, "third-party-notices: %v\n", err)
		os.Exit(1)
	}
}

func run(repo, out string) error {
	goMods, err := collectGoModules(repo)
	if err != nil {
		return err
	}
	npmPkgs, err := collectNPMPackages(repo, npmApps)
	if err != nil {
		return err
	}
	data := renderNotices(goMods, npmPkgs)
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	return nil
}
