// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package llamacpp

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Metadata is the model description read from GGUF headers.
type Metadata struct {
	// Architecture is general.architecture, for example "llama" or "qwen3".
	Architecture string
	// Quantization is the llama.cpp file-type name for general.file_type,
	// for example "Q4_K_M"; empty when the header does not declare one.
	Quantization string
	// SizeLabel is general.size_label, for example "8B"; empty when absent.
	SizeLabel string
	// ContextLength is <architecture>.context_length; zero when absent.
	ContextLength uint64
	// ParameterCount is the element count summed over every tensor in every shard.
	ParameterCount uint64
}

// GGUF header bounds. A header that exceeds them fails closed rather than
// driving an unbounded read.
const (
	ggufMagic          = "GGUF"
	ggufMaxKeyBytes    = 1 << 16
	ggufMaxStringBytes = 16 << 20
	ggufMaxArrayItems  = 1 << 26
	ggufMaxKVCount     = 1 << 20
	ggufMaxTensorCount = 1 << 20
	ggufMaxTensorDims  = 4
	ggufReadBufferSize = 1 << 20
)

// GGUF metadata value types.
const (
	ggufTypeUint8 uint32 = iota
	ggufTypeInt8
	ggufTypeUint16
	ggufTypeInt16
	ggufTypeUint32
	ggufTypeInt32
	ggufTypeFloat32
	ggufTypeBool
	ggufTypeString
	ggufTypeArray
	ggufTypeUint64
	ggufTypeInt64
	ggufTypeFloat64
)

// ggufFileTypes names llama.cpp's llama_ftype values.
var ggufFileTypes = map[uint64]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 7: "Q8_0", 8: "Q5_0", 9: "Q5_1",
	10: "Q2_K", 11: "Q3_K_S", 12: "Q3_K_M", 13: "Q3_K_L", 14: "Q4_K_S", 15: "Q4_K_M",
	16: "Q5_K_S", 17: "Q5_K_M", 18: "Q6_K", 19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S",
	22: "IQ3_XS", 23: "IQ3_XXS", 24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S", 27: "IQ3_M",
	28: "IQ2_S", 29: "IQ2_M", 30: "IQ4_XS", 31: "IQ1_M", 32: "BF16", 36: "TQ1_0",
	37: "TQ2_0", 38: "MXFP4_MOE",
}

// ReadMetadata reads the model description from a model's shard headers.
// Key-value metadata comes from the first shard; tensors from every shard.
func ReadMetadata(shards []string) (Metadata, error) {
	if len(shards) == 0 {
		return Metadata{}, fmt.Errorf("llamacpp: read GGUF metadata: %w", constants.ErrMissingRequiredField)
	}
	var metadata Metadata
	for index, path := range shards {
		header, err := readGGUFHeader(path, index == 0)
		if err != nil {
			return Metadata{}, fmt.Errorf("llamacpp: read GGUF metadata %s: %w", path, err)
		}
		if index == 0 {
			metadata = header.metadata
			continue
		}
		if metadata.ParameterCount > math.MaxUint64-header.metadata.ParameterCount {
			return Metadata{}, fmt.Errorf("llamacpp: read GGUF metadata %s: parameter count overflow: %w", path, constants.ErrLlamaCppGGUFInvalid)
		}
		metadata.ParameterCount += header.metadata.ParameterCount
	}
	return metadata, nil
}

type ggufHeader struct {
	metadata Metadata
}

type ggufReader struct {
	r *bufio.Reader
}

func readGGUFHeader(path string, keepKV bool) (ggufHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return ggufHeader{}, err
	}
	defer file.Close()
	reader := ggufReader{r: bufio.NewReaderSize(file, ggufReadBufferSize)}

	magic := make([]byte, len(ggufMagic))
	if _, err := io.ReadFull(reader.r, magic); err != nil || string(magic) != ggufMagic {
		return ggufHeader{}, fmt.Errorf("magic: %w", constants.ErrLlamaCppGGUFInvalid)
	}
	version, err := reader.uint32()
	if err != nil {
		return ggufHeader{}, err
	}
	// Version 1 used 32-bit lengths; a byte-swapped version is a big-endian file.
	if version != 2 && version != 3 {
		return ggufHeader{}, fmt.Errorf("unsupported version %d: %w", version, constants.ErrLlamaCppGGUFInvalid)
	}
	tensorCount, err := reader.uint64()
	if err != nil {
		return ggufHeader{}, err
	}
	kvCount, err := reader.uint64()
	if err != nil {
		return ggufHeader{}, err
	}
	if tensorCount > ggufMaxTensorCount || kvCount > ggufMaxKVCount {
		return ggufHeader{}, fmt.Errorf("header counts out of bounds: %w", constants.ErrLlamaCppGGUFInvalid)
	}

	var header ggufHeader
	contextKey := ""
	var contextLengths = map[string]uint64{}
	for range kvCount {
		key, err := reader.string(ggufMaxKeyBytes)
		if err != nil {
			return ggufHeader{}, err
		}
		valueType, err := reader.uint32()
		if err != nil {
			return ggufHeader{}, err
		}
		switch {
		case keepKV && key == "general.architecture" && valueType == ggufTypeString:
			header.metadata.Architecture, err = reader.string(ggufMaxStringBytes)
			contextKey = header.metadata.Architecture + ".context_length"
		case keepKV && key == "general.size_label" && valueType == ggufTypeString:
			header.metadata.SizeLabel, err = reader.string(ggufMaxStringBytes)
		case keepKV && key == "general.file_type":
			var fileType uint64
			fileType, err = reader.unsigned(valueType)
			header.metadata.Quantization = ggufFileTypes[fileType]
		case keepKV && strings.HasSuffix(key, ".context_length"):
			var length uint64
			length, err = reader.unsigned(valueType)
			contextLengths[key] = length
		default:
			err = reader.skipValue(valueType)
		}
		if err != nil {
			return ggufHeader{}, err
		}
	}
	if contextKey != "" {
		header.metadata.ContextLength = contextLengths[contextKey]
	}

	for range tensorCount {
		if _, err := reader.string(ggufMaxKeyBytes); err != nil {
			return ggufHeader{}, err
		}
		dims, err := reader.uint32()
		if err != nil {
			return ggufHeader{}, err
		}
		if dims == 0 || dims > ggufMaxTensorDims {
			return ggufHeader{}, fmt.Errorf("tensor dimensions %d: %w", dims, constants.ErrLlamaCppGGUFInvalid)
		}
		elements := uint64(1)
		for range dims {
			dim, err := reader.uint64()
			if err != nil {
				return ggufHeader{}, err
			}
			if dim != 0 && elements > math.MaxUint64/dim {
				return ggufHeader{}, fmt.Errorf("tensor size overflow: %w", constants.ErrLlamaCppGGUFInvalid)
			}
			elements *= dim
		}
		// Tensor type and data offset.
		if err := reader.skip(4 + 8); err != nil {
			return ggufHeader{}, err
		}
		if header.metadata.ParameterCount > math.MaxUint64-elements {
			return ggufHeader{}, fmt.Errorf("parameter count overflow: %w", constants.ErrLlamaCppGGUFInvalid)
		}
		header.metadata.ParameterCount += elements
	}
	return header, nil
}

func (g ggufReader) read(size int) ([]byte, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(g.r, buf); err != nil {
		return nil, fmt.Errorf("truncated header: %w: %w", constants.ErrLlamaCppGGUFInvalid, err)
	}
	return buf, nil
}

func (g ggufReader) uint32() (uint32, error) {
	buf, err := g.read(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf), nil
}

func (g ggufReader) uint64() (uint64, error) {
	buf, err := g.read(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf), nil
}

func (g ggufReader) skip(size uint64) error {
	if _, err := io.CopyN(io.Discard, g.r, int64(size)); err != nil {
		return fmt.Errorf("truncated header: %w: %w", constants.ErrLlamaCppGGUFInvalid, err)
	}
	return nil
}

func (g ggufReader) string(limit uint64) (string, error) {
	length, err := g.uint64()
	if err != nil {
		return "", err
	}
	if length > limit {
		return "", fmt.Errorf("string length %d out of bounds: %w", length, constants.ErrLlamaCppGGUFInvalid)
	}
	buf, err := g.read(int(length))
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// unsigned reads a non-negative integer value of any integer type.
func (g ggufReader) unsigned(valueType uint32) (uint64, error) {
	var value int64
	switch valueType {
	case ggufTypeUint8, ggufTypeInt8:
		buf, err := g.read(1)
		if err != nil {
			return 0, err
		}
		if valueType == ggufTypeUint8 {
			return uint64(buf[0]), nil
		}
		value = int64(int8(buf[0]))
	case ggufTypeUint16, ggufTypeInt16:
		buf, err := g.read(2)
		if err != nil {
			return 0, err
		}
		if valueType == ggufTypeUint16 {
			return uint64(binary.LittleEndian.Uint16(buf)), nil
		}
		value = int64(int16(binary.LittleEndian.Uint16(buf)))
	case ggufTypeUint32, ggufTypeInt32:
		raw, err := g.uint32()
		if err != nil {
			return 0, err
		}
		if valueType == ggufTypeUint32 {
			return uint64(raw), nil
		}
		value = int64(int32(raw))
	case ggufTypeUint64, ggufTypeInt64:
		raw, err := g.uint64()
		if err != nil {
			return 0, err
		}
		if valueType == ggufTypeUint64 {
			return raw, nil
		}
		value = int64(raw)
	default:
		return 0, fmt.Errorf("value type %d is not an integer: %w", valueType, constants.ErrLlamaCppGGUFInvalid)
	}
	if value < 0 {
		return 0, fmt.Errorf("negative integer value: %w", constants.ErrLlamaCppGGUFInvalid)
	}
	return uint64(value), nil
}

func ggufScalarSize(valueType uint32) (uint64, bool) {
	switch valueType {
	case ggufTypeUint8, ggufTypeInt8, ggufTypeBool:
		return 1, true
	case ggufTypeUint16, ggufTypeInt16:
		return 2, true
	case ggufTypeUint32, ggufTypeInt32, ggufTypeFloat32:
		return 4, true
	case ggufTypeUint64, ggufTypeInt64, ggufTypeFloat64:
		return 8, true
	default:
		return 0, false
	}
}

func (g ggufReader) skipValue(valueType uint32) error {
	if size, ok := ggufScalarSize(valueType); ok {
		return g.skip(size)
	}
	switch valueType {
	case ggufTypeString:
		length, err := g.uint64()
		if err != nil {
			return err
		}
		if length > ggufMaxStringBytes {
			return fmt.Errorf("string length %d out of bounds: %w", length, constants.ErrLlamaCppGGUFInvalid)
		}
		return g.skip(length)
	case ggufTypeArray:
		itemType, err := g.uint32()
		if err != nil {
			return err
		}
		count, err := g.uint64()
		if err != nil {
			return err
		}
		if count > ggufMaxArrayItems {
			return fmt.Errorf("array length %d out of bounds: %w", count, constants.ErrLlamaCppGGUFInvalid)
		}
		if size, ok := ggufScalarSize(itemType); ok {
			return g.skip(size * count)
		}
		if itemType != ggufTypeString {
			// Nested arrays are not part of the GGUF metadata model llama.cpp writes.
			return fmt.Errorf("array item type %d: %w", itemType, constants.ErrLlamaCppGGUFInvalid)
		}
		for range count {
			if err := g.skipValue(ggufTypeString); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("value type %d: %w", valueType, constants.ErrLlamaCppGGUFInvalid)
	}
}
