// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package conformancetest

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"io"
	"testing"
)

// doctrineRecordGzip is the gzip -9 of the canonical record bytes, exactly as the
// live run sealed them.
//
//go:embed testdata/doctrine-record.json.gz
var doctrineRecordGzip []byte

// doctrineRecordSHA256 is the hex sha256 of the decompressed canonical record.
const doctrineRecordSHA256 = "9add5c2873cae3dee8590112e45cd86f6a633258df2da0cfb285187b1edbf6d6"

// embeddedDoctrineRecordGzip is the gzip -9 of the canonical record bytes of the
// embedded-only doctrine run, exactly as the live run sealed them.
//
//go:embed testdata/doctrine-embedded-record.json.gz
var embeddedDoctrineRecordGzip []byte

// embeddedDoctrineRecordSHA256 is the hex sha256 of the decompressed embedded-only record.
const embeddedDoctrineRecordSHA256 = "d0f288c23b0bb9e2f78fa0867b52ebec6c34e19b8a5763a87cb2b4ffd61b3af3"

// DoctrineRecord returns the pinned canonical record bytes and the hex sha256 that
// their digest sidecar holds. The decompressed bytes are pinned against
// doctrineRecordSHA256, so a changed testdata file fails the test instead of
// being trusted. Each call returns fresh bytes that the caller may mutate.
func DoctrineRecord(t testing.TB) ([]byte, string) {
	t.Helper()
	return decodePinnedRecord(t, doctrineRecordGzip, doctrineRecordSHA256)
}

// EmbeddedDoctrineRecord returns the pinned canonical record of a doctrine run
// whose Data Operator was the embedded Operator, so every case, including the
// outbound-role ingresses, ran on the embedded executor. It is pinned the same
// way as DoctrineRecord.
func EmbeddedDoctrineRecord(t testing.TB) ([]byte, string) {
	t.Helper()
	return decodePinnedRecord(t, embeddedDoctrineRecordGzip, embeddedDoctrineRecordSHA256)
}

// decodePinnedRecord decompresses gz and returns the bytes with their hex sha256.
// It fails the test unless the sha256 equals want.
func decodePinnedRecord(t testing.TB, gz []byte, want string) ([]byte, string) {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("conformancetest: open pinned doctrine record: %v", err)
		return nil, ""
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("conformancetest: decompress pinned doctrine record: %v", err)
		return nil, ""
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("conformancetest: close pinned doctrine record reader: %v", err)
		return nil, ""
	}
	sum := sha256.Sum256(raw)
	digestHex := hex.EncodeToString(sum[:])
	if digestHex != want {
		t.Fatalf("conformancetest: pinned doctrine record sha256 = %s, want %s", digestHex, want)
		return nil, ""
	}
	return raw, digestHex
}
