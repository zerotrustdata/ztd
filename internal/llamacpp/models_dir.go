// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package llamacpp holds llama.cpp provider conventions shared by the
// capabilities that read a llama-server model store: the router's --models-dir
// layout, the GGUF header format, and the ztd model digest rule over GGUF
// files. It performs no hashing and makes no provider calls; each capability
// owns how it reads and verifies the files this package names.
package llamacpp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

const ggufExtension = ".gguf"

// draftPrefixes mark speculative-decoding draft sidecars, as in llama.cpp
// common/preset.cpp.
var draftPrefixes = []string{"mtp-", "dspark-", "dflash-"}

// shardPattern matches llama.cpp's split naming: <prefix>-%05d-of-%05d.gguf.
var shardPattern = regexp.MustCompile(`^(.+)-(\d{5})-of-(\d{5})\.gguf$`)

// Model is one model a llama-server router serves from its --models-dir.
type Model struct {
	// ID is the router model id: a top-level file's name without ".gguf",
	// or a subdirectory's name.
	ID string
	// Shards are the model's GGUF files in load order. A single-file model
	// has one shard.
	Shards []string
	// MMProj is the multimodal projector sidecar, if any.
	MMProj string
	// Draft is the speculative-decoding draft sidecar, if any.
	Draft string
}

// Files returns every GGUF file the router loads for the model: the shards,
// then the projector and draft sidecars when present.
func (m Model) Files() []string {
	files := append([]string(nil), m.Shards...)
	if m.MMProj != "" {
		files = append(files, m.MMProj)
	}
	if m.Draft != "" {
		files = append(files, m.Draft)
	}
	return files
}

// ResolveModel resolves router model id beneath modelsDir with llama.cpp's
// --models-dir rules. Wherever llama.cpp's choice would depend on directory
// iteration order (several candidate model, projector, or draft files), or a
// shard set is incomplete, resolution fails closed with
// ErrLlamaCppModelLayoutInvalid.
func ResolveModel(modelsDir, id string) (Model, error) {
	if modelsDir == "" {
		return Model{}, fmt.Errorf("llamacpp: resolve model: %w", constants.ErrMissingRequiredField)
	}
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || id != filepath.Base(id) {
		return Model{}, fmt.Errorf("llamacpp: resolve model %q: %w", id, constants.ErrLlamaCppModelNotFound)
	}
	dirInfo, err := os.Stat(filepath.Join(modelsDir, id))
	if err == nil && dirInfo.IsDir() {
		return resolveSubdirModel(modelsDir, id)
	}
	return resolveTopLevelModel(modelsDir, id, id+ggufExtension)
}

// ListModels resolves every model llama-server would register from
// modelsDir, sorted by id. One ambiguous or incomplete entry fails the list.
func ListModels(modelsDir string) ([]Model, error) {
	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		return nil, fmt.Errorf("llamacpp: list models: %w", err)
	}
	result := make([]Model, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		info, err := os.Stat(filepath.Join(modelsDir, name))
		if err != nil {
			return nil, fmt.Errorf("llamacpp: list models: %w", err)
		}
		var model Model
		switch {
		case info.IsDir():
			model, err = resolveSubdirModel(modelsDir, name)
			if errors.Is(err, constants.ErrLlamaCppModelNotFound) {
				// llama.cpp registers no model for a directory without a model GGUF.
				continue
			}
		case info.Mode().IsRegular() && strings.HasSuffix(name, ggufExtension) && !isMMProjFile(name) && !isDraftFile(name):
			if match := shardPattern.FindStringSubmatch(name); match != nil && match[2] != "00001" {
				// A later shard loads with its first shard, not on its own.
				continue
			}
			model, err = resolveTopLevelModel(modelsDir, strings.ReplaceAll(name, ggufExtension, ""), name)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// resolveTopLevelModel resolves a model file placed directly in modelsDir.
// llama.cpp names it by removing ".gguf" from the file name.
func resolveTopLevelModel(modelsDir, id, name string) (Model, error) {
	info, err := os.Stat(filepath.Join(modelsDir, name))
	if err != nil || !info.Mode().IsRegular() || isMMProjFile(name) || isDraftFile(name) {
		return Model{}, fmt.Errorf("llamacpp: resolve model %q: %w", id, constants.ErrLlamaCppModelNotFound)
	}
	shards, err := shardSet(modelsDir, name)
	if err != nil {
		return Model{}, fmt.Errorf("llamacpp: resolve model %q: %w", id, err)
	}
	return Model{ID: id, Shards: shards}, nil
}

// resolveSubdirModel applies llama.cpp's non-recursive subdirectory scan: a
// first shard (name contains "-00001-of-") or otherwise the one non-sidecar
// GGUF is the model; "mmproj" files are projectors; draft-prefixed files are
// drafts.
func resolveSubdirModel(modelsDir, id string) (Model, error) {
	dir := filepath.Join(modelsDir, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Model{}, fmt.Errorf("llamacpp: resolve model %q: %w", id, err)
	}
	var firstShards, modelFiles, mmprojs, drafts []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ggufExtension) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return Model{}, fmt.Errorf("llamacpp: resolve model %q: %w", id, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		switch {
		case isMMProjFile(name):
			mmprojs = append(mmprojs, name)
		case isDraftFile(name):
			drafts = append(drafts, name)
		case strings.Contains(name, "-00001-of-"):
			firstShards = append(firstShards, name)
		default:
			modelFiles = append(modelFiles, name)
		}
	}
	if len(mmprojs) > 1 || len(drafts) > 1 || len(firstShards) > 1 {
		return Model{}, fmt.Errorf("llamacpp: resolve model %q: several candidate files: %w", id, constants.ErrLlamaCppModelLayoutInvalid)
	}
	model := Model{ID: id}
	switch {
	case len(firstShards) == 1:
		shards, err := shardSet(dir, firstShards[0])
		if err != nil {
			return Model{}, fmt.Errorf("llamacpp: resolve model %q: %w", id, err)
		}
		// Every other model GGUF must be one of the shards; a stray file
		// would make the served weights differ from the attested set.
		if len(modelFiles) != len(shards)-1 {
			return Model{}, fmt.Errorf("llamacpp: resolve model %q: files outside the shard set: %w", id, constants.ErrLlamaCppModelLayoutInvalid)
		}
		model.Shards = shards
	case len(modelFiles) == 1:
		if shardPattern.MatchString(modelFiles[0]) {
			return Model{}, fmt.Errorf("llamacpp: resolve model %q: shard set has no first shard: %w", id, constants.ErrLlamaCppModelLayoutInvalid)
		}
		model.Shards = []string{filepath.Join(dir, modelFiles[0])}
	case len(modelFiles) > 1:
		return Model{}, fmt.Errorf("llamacpp: resolve model %q: several candidate files: %w", id, constants.ErrLlamaCppModelLayoutInvalid)
	default:
		return Model{}, fmt.Errorf("llamacpp: resolve model %q: no model GGUF in directory: %w", id, constants.ErrLlamaCppModelNotFound)
	}
	if len(mmprojs) == 1 {
		model.MMProj = filepath.Join(dir, mmprojs[0])
	}
	if len(drafts) == 1 {
		model.Draft = filepath.Join(dir, drafts[0])
	}
	return model, nil
}

// shardSet returns the files llama.cpp loads from first in dir. A file
// without split naming is its own single shard; a split set is
// <prefix>-00001-of-N .. <prefix>-N-of-N, every one present.
func shardSet(dir, first string) ([]string, error) {
	match := shardPattern.FindStringSubmatch(first)
	if match == nil {
		return []string{filepath.Join(dir, first)}, nil
	}
	if match[2] != "00001" {
		return nil, fmt.Errorf("%q is not a first shard: %w", first, constants.ErrLlamaCppModelLayoutInvalid)
	}
	count, err := strconv.Atoi(match[3])
	if err != nil || count < 1 {
		return nil, fmt.Errorf("%q shard count: %w", first, constants.ErrLlamaCppModelLayoutInvalid)
	}
	shards := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		path := filepath.Join(dir, fmt.Sprintf("%s-%05d-of-%05d%s", match[1], index, count, ggufExtension))
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("shard %d of %d missing: %w", index, count, constants.ErrLlamaCppModelLayoutInvalid)
		}
		shards = append(shards, path)
	}
	return shards, nil
}

func isMMProjFile(name string) bool {
	return strings.Contains(name, "mmproj")
}

func isDraftFile(name string) bool {
	for _, prefix := range draftPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
