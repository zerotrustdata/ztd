// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package llamacpp

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func writeFile(t *testing.T, path string, body []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, body, 0o600))
}

func TestResolveModelTopLevelFile(t *testing.T) {
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "qwen3-8b-q4_k_m.gguf"), []byte("w"))

	model, err := ResolveModel(root, "qwen3-8b-q4_k_m")
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(root, "qwen3-8b-q4_k_m.gguf")}, model.Shards)
	assert.Empty(t, model.MMProj)
}

func TestResolveModelTopLevelSplit(t *testing.T) {
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "big-00001-of-00002.gguf"), []byte("a"))
	writeFile(t, filepath.Join(root, "big-00002-of-00002.gguf"), []byte("b"))

	model, err := ResolveModel(root, "big-00001-of-00002")
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(root, "big-00001-of-00002.gguf"),
		filepath.Join(root, "big-00002-of-00002.gguf"),
	}, model.Shards)

	require.NoError(t, os.Remove(filepath.Join(root, "big-00002-of-00002.gguf")))
	_, err = ResolveModel(root, "big-00001-of-00002")
	require.ErrorIs(t, err, constants.ErrLlamaCppModelLayoutInvalid)
}

func TestResolveModelSubdirectory(t *testing.T) {
	root := testutil.TempDir(t)
	dir := filepath.Join(root, "gemma")
	writeFile(t, filepath.Join(dir, "gemma-00001-of-00002.gguf"), []byte("a"))
	writeFile(t, filepath.Join(dir, "gemma-00002-of-00002.gguf"), []byte("b"))
	writeFile(t, filepath.Join(dir, "mmproj-f16.gguf"), []byte("p"))
	writeFile(t, filepath.Join(dir, "mtp-gemma.gguf"), []byte("d"))
	writeFile(t, filepath.Join(dir, "README.md"), []byte("x"))

	model, err := ResolveModel(root, "gemma")
	require.NoError(t, err)
	assert.Equal(t, "gemma", model.ID)
	assert.Equal(t, []string{
		filepath.Join(dir, "gemma-00001-of-00002.gguf"),
		filepath.Join(dir, "gemma-00002-of-00002.gguf"),
		filepath.Join(dir, "mmproj-f16.gguf"),
		filepath.Join(dir, "mtp-gemma.gguf"),
	}, model.Files())
}

func TestResolveModelFailsClosed(t *testing.T) {
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "two", "a.gguf"), []byte("a"))
	writeFile(t, filepath.Join(root, "two", "b.gguf"), []byte("b"))
	writeFile(t, filepath.Join(root, "stray", "s-00001-of-00002.gguf"), []byte("a"))
	writeFile(t, filepath.Join(root, "stray", "s-00002-of-00002.gguf"), []byte("b"))
	writeFile(t, filepath.Join(root, "stray", "other.gguf"), []byte("c"))
	writeFile(t, filepath.Join(root, "proj", "mmproj-a.gguf"), []byte("p"))
	writeFile(t, filepath.Join(root, "mmproj-top.gguf"), []byte("p"))

	_, err := ResolveModel(root, "two")
	require.ErrorIs(t, err, constants.ErrLlamaCppModelLayoutInvalid)
	_, err = ResolveModel(root, "stray")
	require.ErrorIs(t, err, constants.ErrLlamaCppModelLayoutInvalid)
	for _, id := range []string{"proj", "mmproj-top", "absent", "", "..", "two/a"} {
		_, err = ResolveModel(root, id)
		require.ErrorIs(t, err, constants.ErrLlamaCppModelNotFound, id)
	}
}

func TestListModels(t *testing.T) {
	root := testutil.TempDir(t)
	writeFile(t, filepath.Join(root, "solo.gguf"), []byte("a"))
	writeFile(t, filepath.Join(root, "big-00001-of-00002.gguf"), []byte("a"))
	writeFile(t, filepath.Join(root, "big-00002-of-00002.gguf"), []byte("b"))
	writeFile(t, filepath.Join(root, "mmproj-top.gguf"), []byte("p"))
	writeFile(t, filepath.Join(root, "dir", "m.gguf"), []byte("m"))
	writeFile(t, filepath.Join(root, "empty", "notes.txt"), []byte("n"))

	listed, err := ListModels(root)
	require.NoError(t, err)
	ids := make([]string, 0, len(listed))
	for _, model := range listed {
		ids = append(ids, model.ID)
	}
	assert.Equal(t, []string{"big-00001-of-00002", "dir", "solo"}, ids)
}

func TestComposeModelDigest(t *testing.T) {
	first := models.SHA256Hex([]byte("a"))
	second := models.SHA256Hex([]byte("b"))

	single, err := ComposeModelDigest([]string{first})
	require.NoError(t, err)
	assert.Equal(t, first, single)

	split, err := ComposeModelDigest([]string{first, second})
	require.NoError(t, err)
	assert.Equal(t, models.SHA256Hex([]byte(first+"\n"+second)), split)

	_, err = ComposeModelDigest(nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	_, err = ComposeModelDigest([]string{strings.ToUpper(first)})
	require.ErrorIs(t, err, constants.ErrInferenceEvidenceHashInvalid)
}

// ggufBuilder writes a minimal little-endian GGUF v3 header.
type ggufBuilder struct {
	kv      bytes.Buffer
	kvCount uint64
	tensors bytes.Buffer
	tensorN uint64
}

func (b *ggufBuilder) str(buf *bytes.Buffer, value string) {
	_ = binary.Write(buf, binary.LittleEndian, uint64(len(value)))
	buf.WriteString(value)
}

func (b *ggufBuilder) kvString(key, value string) {
	b.str(&b.kv, key)
	_ = binary.Write(&b.kv, binary.LittleEndian, ggufTypeString)
	b.str(&b.kv, value)
	b.kvCount++
}

func (b *ggufBuilder) kvUint32(key string, value uint32) {
	b.str(&b.kv, key)
	_ = binary.Write(&b.kv, binary.LittleEndian, ggufTypeUint32)
	_ = binary.Write(&b.kv, binary.LittleEndian, value)
	b.kvCount++
}

func (b *ggufBuilder) kvStringArray(key string, values ...string) {
	b.str(&b.kv, key)
	_ = binary.Write(&b.kv, binary.LittleEndian, ggufTypeArray)
	_ = binary.Write(&b.kv, binary.LittleEndian, ggufTypeString)
	_ = binary.Write(&b.kv, binary.LittleEndian, uint64(len(values)))
	for _, value := range values {
		b.str(&b.kv, value)
	}
	b.kvCount++
}

func (b *ggufBuilder) tensor(name string, dims ...uint64) {
	b.str(&b.tensors, name)
	_ = binary.Write(&b.tensors, binary.LittleEndian, uint32(len(dims)))
	for _, dim := range dims {
		_ = binary.Write(&b.tensors, binary.LittleEndian, dim)
	}
	_ = binary.Write(&b.tensors, binary.LittleEndian, uint32(0))
	_ = binary.Write(&b.tensors, binary.LittleEndian, uint64(0))
	b.tensorN++
}

func (b *ggufBuilder) bytes() []byte {
	var out bytes.Buffer
	out.WriteString(ggufMagic)
	_ = binary.Write(&out, binary.LittleEndian, uint32(3))
	_ = binary.Write(&out, binary.LittleEndian, b.tensorN)
	_ = binary.Write(&out, binary.LittleEndian, b.kvCount)
	out.Write(b.kv.Bytes())
	out.Write(b.tensors.Bytes())
	return out.Bytes()
}

func TestReadMetadataAcrossShards(t *testing.T) {
	root := testutil.TempDir(t)
	var first ggufBuilder
	first.kvString("general.architecture", "qwen3")
	first.kvStringArray("tokenizer.ggml.tokens", "a", "b", "c")
	first.kvString("general.size_label", "8B")
	first.kvUint32("general.file_type", 15)
	first.kvUint32("llama.context_length", 4096)
	first.kvUint32("qwen3.context_length", 40960)
	first.tensor("token_embd.weight", 4, 3)
	var second ggufBuilder
	second.kvUint32("split.no", 1)
	second.tensor("output.weight", 2, 5)

	firstPath := filepath.Join(root, "m-00001-of-00002.gguf")
	secondPath := filepath.Join(root, "m-00002-of-00002.gguf")
	writeFile(t, firstPath, first.bytes())
	writeFile(t, secondPath, second.bytes())

	metadata, err := ReadMetadata([]string{firstPath, secondPath})
	require.NoError(t, err)
	assert.Equal(t, Metadata{
		Architecture:   "qwen3",
		Quantization:   "Q4_K_M",
		SizeLabel:      "8B",
		ContextLength:  40960,
		ParameterCount: 22,
	}, metadata)
}

func TestReadMetadataRejectsInvalidHeaders(t *testing.T) {
	root := testutil.TempDir(t)
	var valid ggufBuilder
	valid.kvString("general.architecture", "llama")
	body := valid.bytes()

	cases := map[string][]byte{
		"magic":     append([]byte("GGUX"), body[4:]...),
		"truncated": body[:len(body)-2],
		"huge-string": func() []byte {
			var b ggufBuilder
			b.str(&b.kv, "general.architecture")
			_ = binary.Write(&b.kv, binary.LittleEndian, ggufTypeString)
			_ = binary.Write(&b.kv, binary.LittleEndian, uint64(ggufMaxStringBytes+1))
			b.kvCount++
			return b.bytes()
		}(),
		"zero-dims": func() []byte {
			var b ggufBuilder
			b.tensor("t")
			return b.bytes()
		}(),
	}
	for name, content := range cases {
		path := filepath.Join(root, name+".gguf")
		writeFile(t, path, content)
		_, err := ReadMetadata([]string{path})
		require.ErrorIs(t, err, constants.ErrLlamaCppGGUFInvalid, name)
	}
}
