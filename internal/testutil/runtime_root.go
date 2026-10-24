// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"context"
	"os"
	"path"
	"sort"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// runtimeEvidenceStoreDirs are the per-runtime evidence stores that live in
// the data directory next to the database and ledger. INV-STOR-ONE-01 limits
// databases and ledgers, not these: docs/architecture/storage.md lists the
// inference tree (provider attempts, provider-observer and model-provenance
// windows, under data/inference/) and the evaluation tree (campaigns, runs,
// suites, under data/eval/) as runtime-root contents. They are allowed by
// name only; any other directory still fails.
var runtimeEvidenceStoreDirs = map[string]bool{
	constants.InferenceDirname:  true,
	constants.EvaluationDirname: true,
}

type runtimeFileService interface {
	ReadDir(context.Context, string) ([]os.DirEntry, error)
	Resolve(string) string
	Stat(context.Context, string) (os.FileInfo, error)
}

// AssertOneDatabaseAndLedger enforces INV-STOR-ONE-01 on a runtime data
// directory: it must hold the canonical SQLite database (with its SQLite
// sidecars) and the file ledger directory, plus only the named evidence-store
// directories in runtimeEvidenceStoreDirs. It is an allow-list, so any
// unexpected entry fails the test by name. Data-bearing entries must also be
// private: the data directory and every allowed directory 0700, the database
// and its sidecars 0600.
func AssertOneDatabaseAndLedger(t *testing.T, fileSvc runtimeFileService) {
	t.Helper()

	allowedFiles := map[string]bool{
		constants.DbFilename:                                 true,
		constants.DbFilename + constants.SQLiteWALSuffix:     true,
		constants.DbFilename + constants.SQLiteSHMSuffix:     true,
		constants.DbFilename + constants.SQLiteJournalSuffix: true,
	}

	ctx := t.Context()
	dataDir := constants.DataDirname
	dataDirAbs := fileSvc.Resolve(dataDir)
	entries, err := fileSvc.ReadDir(ctx, dataDir)
	if err != nil {
		t.Fatalf("read runtime data dir %s: %v", dataDirAbs, err)
	}

	assertMode(t, fileSvc, ctx, dataDir, constants.PermDirPrivate, true)

	var sawDB, sawLedger bool
	var unexpected []string
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == constants.LedgerDirname && entry.IsDir():
			sawLedger = true
			assertMode(t, fileSvc, ctx, path.Join(dataDir, name), constants.PermDirPrivate, true)
		case runtimeEvidenceStoreDirs[name] && entry.IsDir():
			assertMode(t, fileSvc, ctx, path.Join(dataDir, name), constants.PermDirPrivate, true)
		case allowedFiles[name] && !entry.IsDir():
			if name == constants.DbFilename {
				sawDB = true
			}
			assertMode(t, fileSvc, ctx, path.Join(dataDir, name), constants.PermFilePrivate, false)
		default:
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)

	if len(unexpected) > 0 {
		t.Errorf("INV-STOR-ONE-01: runtime data dir %s holds unexpected entries %q; only %s (plus SQLite sidecars), %s/, %s/, and %s/ are allowed",
			dataDir, unexpected, constants.DbFilename, constants.LedgerDirname, constants.InferenceDirname, constants.EvaluationDirname)
	}
	if !sawDB {
		t.Errorf("INV-STOR-ONE-01: runtime data dir %s has no %s", dataDir, constants.DbFilename)
	}
	if !sawLedger {
		t.Errorf("INV-STOR-ONE-01: runtime data dir %s has no %s/ directory", dataDir, constants.LedgerDirname)
	}
}

// assertMode fails the test unless path has exactly the wanted permission bits
// (adjusted for Windows, which has no group or other bits).
func assertMode(t *testing.T, fileSvc runtimeFileService, ctx context.Context, path string, want os.FileMode, directory bool) {
	t.Helper()
	info, err := fileSvc.Stat(ctx, path)
	if err != nil {
		t.Errorf("stat %s: %v", fileSvc.Resolve(path), err)
		return
	}
	if got, wantMode := info.Mode().Perm(), FileMode(want, directory); got != wantMode {
		t.Errorf("runtime data entry %s has mode %#o, want %#o", fileSvc.Resolve(path), got, wantMode)
	}
}
