// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/services/ztdbinaries"
)

func main() {
	root := flag.String("root", "bin", "artifact directory")
	version := flag.String("version", "unknown", "ztd version")
	buildID := flag.String("build-id", "unknown", "build identifier")
	buildTime := flag.String("build-time", "", "RFC3339 build time")
	revision := flag.String("source-revision", "unknown", "source revision")
	treeHash := flag.String("source-tree-hash", "unknown", "source tree hash")
	listTargets := flag.Bool("list-targets", false, "print catalogued target platforms")
	flag.Parse()
	if *listTargets {
		fmt.Print(formatTargets())
		return
	}
	if err := generate(*root, *version, *buildID, *buildTime, *revision, *treeHash); err != nil {
		fmt.Fprintf(os.Stderr, "ztd-binaries: %v\n", err)
		os.Exit(1)
	}
}

// formatTargets renders the catalogued platforms as "os/arch " tokens, each
// followed by a space, which the Makefile splits into its PLATFORMS list.
func formatTargets() string {
	var b strings.Builder
	for _, target := range ztdbinaries.Targets() {
		fmt.Fprintf(&b, "%s/%s ", target.OS, target.Arch)
	}
	return b.String()
}

func generate(root, version, buildID, buildTime, revision, treeHash string) error {
	if buildTime == "" {
		buildTime = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := time.Parse(time.RFC3339, buildTime); err != nil {
		return fmt.Errorf("invalid build time: %w", err)
	}
	if strings.TrimSpace(version) == "" || strings.TrimSpace(buildID) == "" || strings.TrimSpace(treeHash) == "" {
		return fmt.Errorf("provenance fields must not be empty")
	}
	artifacts := make([]ztdbinaries.Artifact, 0, len(ztdbinaries.Targets()))
	for _, target := range ztdbinaries.Targets() {
		path := filepath.Join(root, target.Filename)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", target.Filename, err)
		}
		digest := sha256.Sum256(data)
		checksum := filepath.Join(root, target.Checksum)
		if err := os.WriteFile(checksum, []byte(hex.EncodeToString(digest[:])+"  "+target.Filename+"\n"), 0644); err != nil {
			return fmt.Errorf("write %s: %w", target.Checksum, err)
		}
		artifacts = append(artifacts, ztdbinaries.Artifact{Target: target, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])})
	}
	manifest := ztdbinaries.Manifest{SchemaVersion: 1, Version: version, BuildID: buildID, BuildTime: buildTime, SourceRevision: revision, SourceTreeHash: treeHash, Targets: artifacts}
	if err := manifest.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	staging := filepath.Join(root, ".ztd-binaries.json.new")
	if err := os.WriteFile(staging, data, 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := os.Rename(staging, filepath.Join(root, "ztd-binaries.json")); err != nil {
		return fmt.Errorf("publish manifest: %w", err)
	}
	return nil
}
