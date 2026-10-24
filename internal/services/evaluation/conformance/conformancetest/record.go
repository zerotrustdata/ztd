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
const doctrineRecordSHA256 = "1091115366eeec850959c8ec2900714d3137f68e352ae5d66a9fcc58927ff23a"

// embeddedDoctrineRecordGzip is the gzip -9 of the canonical record bytes of the
// embedded-only doctrine run, exactly as the live run sealed them.
//
//go:embed testdata/doctrine-embedded-record.json.gz
var embeddedDoctrineRecordGzip []byte

// embeddedDoctrineRecordSHA256 is the hex sha256 of the decompressed embedded-only record.
const embeddedDoctrineRecordSHA256 = "8b65b90be2f9dfdf4c0d1b22bcac64a6ea2f71500f1bbbe000d48f7f0879eec0"

// DoctrineRecord returns the pinned canonical record bytes and the hex sha256 that
// their digest sidecar holds. The decompressed bytes are pinned against
// doctrineRecordSHA256, so a changed testdata file fails the test instead of
// being trusted. Each call returns fresh bytes that the caller may mutate.
func DoctrineRecord(tb testing.TB) ([]byte, string) {
	tb.Helper()
	return decodePinnedRecord(tb, doctrineRecordGzip, doctrineRecordSHA256)
}

// EmbeddedDoctrineRecord returns the pinned canonical record of a doctrine run
// whose Data Operator was the embedded Operator, so every case, including the
// outbound-role ingresses, ran on the embedded executor. It is pinned the same
// way as DoctrineRecord.
func EmbeddedDoctrineRecord(tb testing.TB) ([]byte, string) {
	tb.Helper()
	return decodePinnedRecord(tb, embeddedDoctrineRecordGzip, embeddedDoctrineRecordSHA256)
}

// decodePinnedRecord decompresses gz and returns the bytes with their hex sha256.
// It fails the test unless the sha256 equals want.
func decodePinnedRecord(tb testing.TB, gz []byte, want string) ([]byte, string) {
	tb.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		tb.Fatalf("conformancetest: open pinned doctrine record: %v", err)
		return nil, ""
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		tb.Fatalf("conformancetest: decompress pinned doctrine record: %v", err)
		return nil, ""
	}
	if err := reader.Close(); err != nil {
		tb.Fatalf("conformancetest: close pinned doctrine record reader: %v", err)
		return nil, ""
	}
	sum := sha256.Sum256(raw)
	digestHex := hex.EncodeToString(sum[:])
	if digestHex != want {
		tb.Fatalf("conformancetest: pinned doctrine record sha256 = %s, want %s", digestHex, want)
		return nil, ""
	}
	return raw, digestHex
}
