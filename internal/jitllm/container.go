// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package jitllm names, resolves, and describes the .jlm containers in a
// jitllm models folder with jitllm's own folder scan and container reader.
// It never loads weights: the Inference Operator runs the engine itself
// (inference.JitLLMBackend), and the Provenance Operator only hashes files.
package jitllm

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jitllm/jitllm/format/jlm"
	"github.com/jitllm/jitllm/server"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Metadata is the identity a campaign can freeze from a container without
// loading it. ParameterCount is the sum of every tensor's elements, and
// Quantization is the packed type that holds the most of them, named as
// GGUF names it.
type Metadata struct {
	Architecture   string
	ParameterCount uint64
	Quantization   string
	ContextLength  uint32
	Vision         bool
}

// Container is one .jlm file in a models folder. ID is the file name without
// .jlm, the id the engine loads it under on demand.
type Container struct {
	Path     string
	ID       string
	Aliases  []string
	Disabled bool
	Size     uint64
}

// List returns every container directly in root, including disabled ones,
// with the aliases and disabled flags of the folder's jitllm state file.
func List(root string) ([]Container, error) {
	dir, err := cleanRoot(root)
	if err != nil {
		return nil, err
	}
	// New touches no hardware and starts nothing; ScanModels only reads dir.
	files, _, err := server.New(server.Config{ModelDir: dir, NoOnDemand: true}).ScanModels(dir)
	if err != nil {
		return nil, fmt.Errorf("jitllm: list models: %w", err)
	}
	containers := make([]Container, 0, len(files))
	for _, file := range files {
		if !file.IsContainer {
			continue
		}
		containers = append(containers, Container{Path: file.Path, ID: file.Name, Aliases: file.Aliases, Disabled: file.Disabled, Size: file.Size})
	}
	return containers, nil
}

// Resolve finds the container name refers to the way the engine resolves a
// model in its folder: its id, its file name, or an alias. name must match
// exactly one container.
func Resolve(root, name string) (Container, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Container{}, fmt.Errorf("jitllm: resolve model: %w", constants.ErrInferenceModelRefInvalid)
	}
	containers, err := List(root)
	if err != nil {
		return Container{}, err
	}
	var matched *Container
	for i := range containers {
		if containers[i].Names(name) {
			if matched != nil {
				return Container{}, fmt.Errorf("jitllm: resolve model %q: %w", name, constants.ErrJitLLMModelAmbiguous)
			}
			matched = &containers[i]
		}
	}
	if matched == nil {
		return Container{}, fmt.Errorf("jitllm: resolve model %q: %w", name, constants.ErrInferenceModelNotFound)
	}
	return *matched, nil
}

// Names reports whether name is this container's id, file name, or alias.
func (c Container) Names(name string) bool {
	if name == c.ID || name == filepath.Base(c.Path) {
		return true
	}
	for _, alias := range c.Aliases {
		if alias == name {
			return true
		}
	}
	return false
}

// ReadMetadata opens the container with jitllm's reader, which validates the
// header, config, vocabulary, and tensor table without reading a weight page.
func ReadMetadata(path string) (Metadata, error) {
	file, err := jlm.Open(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("jitllm: read container: %w: %w", constants.ErrJitLLMContainerInvalid, err)
	}
	defer file.Close()
	var total uint64
	byType := map[jlm.Type]uint64{}
	for _, entry := range file.Entries() {
		elements := uint64(1)
		for _, dim := range entry.Dims[:entry.NDim] {
			elements *= dim
		}
		total += elements
		byType[entry.Type] += elements
	}
	cfg := file.Config()
	return Metadata{
		Architecture:   cfg.Arch.String(),
		ParameterCount: total,
		Quantization:   dominantQuantization(byType),
		ContextLength:  cfg.NCtx,
		Vision:         file.Vision() != nil,
	}, nil
}

// dominantQuantization names the packed type holding the most elements, or
// the float type holding the most when nothing is packed.
func dominantQuantization(byType map[jlm.Type]uint64) string {
	var best jlm.Type
	var bestN uint64
	bestPacked := false
	for typ, n := range byType {
		packed := !floatTypes[typ]
		if bestPacked && !packed {
			continue
		}
		if packed && !bestPacked || n > bestN || n == bestN && typ < best {
			best, bestN, bestPacked = typ, n, packed
		}
	}
	if name, ok := ggufNames[best]; ok {
		return name
	}
	return strings.ToUpper(best.String())
}

var floatTypes = map[jlm.Type]bool{jlm.TypeF32: true, jlm.TypeF16: true, jlm.TypeBF16: true}

// ggufNames are the GGUF type names jitllm's packed types are converted from,
// the quantization labels formations already use for local providers.
var ggufNames = map[jlm.Type]string{
	jlm.TypeF32:   "F32",
	jlm.TypeF16:   "F16",
	jlm.TypeBF16:  "BF16",
	jlm.TypeQ4:    "Q4_0",
	jlm.TypeQ8:    "Q8_0",
	jlm.TypeQ3S:   "Q3_K",
	jlm.TypeQ4S:   "Q4_K",
	jlm.TypeQ5S:   "Q5_K",
	jlm.TypeQ6S:   "Q6_K",
	jlm.TypeQ5:    "Q5_0",
	jlm.TypeQ51:   "Q5_1",
	jlm.TypeMX4:   "MXFP4",
	jlm.TypeIQ4NL: "IQ4_NL",
	jlm.TypeTQ2:   "TQ2_0",
	jlm.TypeTQ2F:  "TQ2_0",
}

func cleanRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("jitllm: model root: %w", constants.ErrInferenceJitLLMModelRootRequired)
	}
	return filepath.Clean(root), nil
}
