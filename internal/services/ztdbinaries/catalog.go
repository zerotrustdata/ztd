// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package ztdbinaries

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Target describes one artifact in the ztd deployment matrix.
type Target struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Filename string `json:"filename"`
	Checksum string `json:"checksum"`
	Mode     uint32 `json:"mode"`
	FIPS     string `json:"fips_module,omitempty"`
}

var targets = []Target{
	{OS: "linux", Arch: "amd64", Filename: "ztd-linux-amd64", Checksum: "ztd-linux-amd64.sha256", Mode: constants.PermFileExecutable, FIPS: "v1.0.0"},
	{OS: "linux", Arch: "arm64", Filename: "ztd-linux-arm64", Checksum: "ztd-linux-arm64.sha256", Mode: constants.PermFileExecutable},
	{OS: "linux", Arch: "386", Filename: "ztd-linux-386", Checksum: "ztd-linux-386.sha256", Mode: constants.PermFileExecutable},
	{OS: "windows", Arch: "amd64", Filename: "ztd-windows-amd64.exe", Checksum: "ztd-windows-amd64.exe.sha256", Mode: constants.PermFileExecutable},
	{OS: "windows", Arch: "arm64", Filename: "ztd-windows-arm64.exe", Checksum: "ztd-windows-arm64.exe.sha256", Mode: constants.PermFileExecutable},
	{OS: "darwin", Arch: "amd64", Filename: "ztd-darwin-amd64", Checksum: "ztd-darwin-amd64.sha256", Mode: constants.PermFileExecutable},
	{OS: "darwin", Arch: "arm64", Filename: "ztd-darwin-arm64", Checksum: "ztd-darwin-arm64.sha256", Mode: constants.PermFileExecutable},
}

// Targets returns a copy of the supported deployment matrix.
func Targets() []Target {
	result := append([]Target(nil), targets...)
	return result
}

func isSupportedArtifactFilename(name string) bool {
	for _, target := range targets {
		if target.Filename == name || target.Checksum == name {
			return true
		}
	}
	return false
}

func ValidateArtifactName(name string) error {
	if name == "" || filepath.Base(name) != name || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q", constants.ErrZtdBinaryArtifact, name)
	}
	if !isSupportedArtifactFilename(name) {
		return fmt.Errorf("%w: unsupported filename %q", constants.ErrZtdBinaryArtifact, name)
	}
	return nil
}

func sortedTargets(entries []Target) []Target {
	result := append([]Target(nil), entries...)
	sort.Slice(result, func(i, j int) bool { return result[i].Filename < result[j].Filename })
	return result
}
