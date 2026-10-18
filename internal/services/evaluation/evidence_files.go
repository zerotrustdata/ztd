// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// EvidenceVault is the runtime vault surface evaluation records need. The
// runtime vault (*vault.Vault) satisfies it, so evaluation records use the
// same key hierarchy as the audit store and the file ledger.
type EvidenceVault interface {
	EncryptWithAAD(plaintext, aad []byte) ([]byte, error)
	DecryptWithAAD(ciphertext, aad []byte) ([]byte, error)
}

// evidenceAAD is the associated data bound into every evaluation record. It
// separates evaluation ciphertexts from every other vault-sealed store (audit
// columns, ledger snapshots, KV values, compliance report bundles), so a blob
// from another store fails authentication here. It is a domain label, not a
// path: the archiver moves whole run and campaign trees, and each record
// carries its own identity and digest.
var evidenceAAD = []byte("ztd-eval-evidence-v1")

// complianceReportAAD is the associated data bound into every persisted
// compliance report bundle file. A report bundle carries the raw evaluation
// and operational sources its verifier replays, so it is sealed like the
// evidence it was built from, under its own domain label.
var complianceReportAAD = []byte("ztd-compliance-report-v1")

// sealedRoot is a runtime subtree whose every file is encrypted at rest, and
// the associated data that binds its ciphertexts to that subtree.
type sealedRoot struct {
	dir string
	aad []byte
}

func sealedRoots() []sealedRoot {
	return []sealedRoot{
		{dir: evalRoot(), aad: evidenceAAD},
		{dir: constants.ComplianceBundlesDirname, aad: complianceReportAAD},
	}
}

// EvidenceVaultProvider opens the vault whose key chain (constants.VaultHeaderRelPath,
// constants.VaultKeyRelPath, and any sealed master key blob) lives in keys.
// The evidence file service calls it with its own runtime the first time an
// evaluation record is read or written, so a command that never touches
// evaluation evidence never opens it. A restore calls it with a scratch runtime
// holding a backup snapshot's key chain. A failure is returned to the caller,
// never cached or downgraded to plaintext.
type EvidenceVaultProvider func(keys fs.RuntimeFileService) (EvidenceVault, error)

// evidenceFiles is a RuntimeFileService whose evaluation records and
// compliance report bundles are encrypted at rest. Every file under
// .ztd/data/eval/ and .ztd/reports/ is sealed with the runtime vault on write
// and opened on read, so a writer cannot persist prompts, model output, tool
// arguments, grading detail, or the report sources that replay them in the
// clear, and a reader never sees ciphertext. There is no plaintext branch: a
// missing or locked vault, or a file that does not authenticate, is an error.
//
// Every file under a sealed root is ciphertext, without exception. Clear
// output lives outside them: ExportRun writes its disclosure-safe public
// projection, including bounded allowlisted model/tool transcripts
// (INV-EVAL-EVID-04), through SealedFiles to a directory it refuses to place
// under a sealed root, and "compliance report export" writes an owner-chosen
// plaintext copy of a report bundle outside the runtime root, because those
// are meant to be opened by people and tools outside ztd.
type evidenceFiles struct {
	fs.RuntimeFileService
	provider EvidenceVaultProvider

	mu    sync.Mutex
	vault EvidenceVault
}

// NewEvidenceFileService returns files with every evaluation record under
// .ztd/data/eval/ and every compliance report bundle under .ztd/reports/
// encrypted at rest by the runtime vault that provider opens.
func NewEvidenceFileService(files fs.RuntimeFileService, provider EvidenceVaultProvider) (fs.RuntimeFileService, error) {
	if files == nil || provider == nil {
		return nil, fmt.Errorf("evaluation: evidence file service: %w", constants.ErrMissingRequiredField)
	}
	if _, sealed := files.(*evidenceFiles); sealed {
		return files, nil
	}
	return &evidenceFiles{RuntimeFileService: files, provider: provider}, nil
}

// SealedFiles returns the file service beneath an evidence file service, which
// reads and writes the ciphertext as stored. Backup and restore use it to carry
// evidence in its encrypted form, so no plaintext leaves the runtime and no
// vault is needed to move it. A file service that does not encrypt is returned
// as is.
func SealedFiles(files fs.RuntimeFileService) fs.RuntimeFileService {
	if sealed, ok := files.(*evidenceFiles); ok {
		return sealed.RuntimeFileService
	}
	return files
}

// SealsPath reports whether files encrypts relPath at rest. Writers whose
// output must never land in the clear check it and fail closed instead of
// trusting their caller to have composed the evidence file service.
func SealsPath(files fs.RuntimeFileService, relPath string) bool {
	sealed, ok := files.(*evidenceFiles)
	if !ok {
		return false
	}
	_, seals := sealed.sealedAAD(relPath)
	return seals
}

func (f *evidenceFiles) vaultFor() (EvidenceVault, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.vault != nil {
		return f.vault, nil
	}
	v, err := f.provider(f.RuntimeFileService)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrEvaluationEvidenceVaultRequired, err)
	}
	if v == nil {
		return nil, constants.ErrEvaluationEvidenceVaultRequired
	}
	f.vault = v
	return v, nil
}

// openKeyChain opens the vault a captured key chain holds. chain maps each
// key chain file's runtime-relative path to its stored bytes, all of them
// ciphertext or wrapped keys. They are laid out in a scratch runtime under the
// runtime tmp directory for the provider and removed before it returns; the
// opened vault keeps only its unwrapped key, in memory.
func (f *evidenceFiles) openKeyChain(ctx context.Context, chain map[string][]byte) (EvidenceVault, error) {
	scratch, err := f.MkdirTemp(ctx, "eval-restore-keys-*")
	if err != nil {
		return nil, fmt.Errorf("evaluation: open backup key chain: %w", err)
	}
	defer func() { _ = f.RemoveAll(context.WithoutCancel(ctx), scratch) }()
	keys, err := fs.NewRuntimeFileService(f.Resolve(scratch), nil)
	if err != nil {
		return nil, fmt.Errorf("evaluation: open backup key chain: %w", err)
	}
	for relPath, data := range chain {
		if err := keys.WriteFile(ctx, relPath, data, constants.PermFilePrivate); err != nil {
			return nil, fmt.Errorf("evaluation: open backup key chain: stage %s: %w", relPath, err)
		}
	}
	v, err := f.provider(keys)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrEvaluationBackupKeyChainInvalid, err)
	}
	if v == nil {
		return nil, constants.ErrEvaluationBackupKeyChainInvalid
	}
	return v, nil
}

// sealedAAD returns the associated data for relPath when it is encrypted at
// rest, and false when it is stored as is.
func (f *evidenceFiles) sealedAAD(relPath string) ([]byte, bool) {
	if filepath.IsAbs(relPath) {
		rel, err := f.Rel(relPath)
		if err != nil {
			// The base service rejects a path outside the runtime root.
			return nil, false
		}
		relPath = rel
	}
	return sealedPathAAD(relPath)
}

func (f *evidenceFiles) sealed(relPath string) bool {
	_, sealed := f.sealedAAD(relPath)
	return sealed
}

func (f *evidenceFiles) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	data, err := f.RuntimeFileService.ReadFile(ctx, relPath)
	if err != nil {
		return data, err
	}
	aad, sealed := f.sealedAAD(relPath)
	if !sealed {
		return data, nil
	}
	v, err := f.vaultFor()
	if err != nil {
		return nil, err
	}
	plaintext, err := v.DecryptWithAAD(data, aad)
	if errors.Is(err, constants.ErrVaultLocked) {
		// A locked vault says nothing about the stored bytes; only an
		// authentication failure under an open vault is a decrypt failure.
		return nil, fmt.Errorf("evidence read %s: %w", filepath.ToSlash(relPath), err)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", constants.ErrEvaluationEvidenceDecryptFailed, filepath.ToSlash(relPath), err)
	}
	return plaintext, nil
}

func (f *evidenceFiles) WriteFile(ctx context.Context, relPath string, data []byte, mode os.FileMode) error {
	aad, sealed := f.sealedAAD(relPath)
	if !sealed {
		return f.RuntimeFileService.WriteFile(ctx, relPath, data, mode)
	}
	v, err := f.vaultFor()
	if err != nil {
		return err
	}
	ciphertext, err := v.EncryptWithAAD(data, aad)
	if err != nil {
		return fmt.Errorf("evaluation: encrypt evidence %s: %w", filepath.ToSlash(relPath), err)
	}
	return f.RuntimeFileService.WriteFile(ctx, relPath, ciphertext, mode)
}

// OpenForAppend refuses encrypted records: an append-only stream cannot be
// sealed record by record, and a plaintext stream under a sealed root is exactly
// what this service exists to prevent.
func (f *evidenceFiles) OpenForAppend(ctx context.Context, relPath string, mode os.FileMode) (*os.File, error) {
	if f.sealed(relPath) {
		return nil, fmt.Errorf("%w: %s", constants.ErrEvaluationEvidenceStreamUnsupported, filepath.ToSlash(relPath))
	}
	return f.RuntimeFileService.OpenForAppend(ctx, relPath, mode)
}

// OpenForRead refuses encrypted records for the same reason as OpenForAppend:
// the raw handle would expose ciphertext as if it were the record.
func (f *evidenceFiles) OpenForRead(ctx context.Context, relPath string) (*os.File, error) {
	if f.sealed(relPath) {
		return nil, fmt.Errorf("%w: %s", constants.ErrEvaluationEvidenceStreamUnsupported, filepath.ToSlash(relPath))
	}
	return f.RuntimeFileService.OpenForRead(ctx, relPath)
}

// sealedPathAAD returns the associated data for a runtime-relative path that
// is encrypted at rest: everything under .ztd/data/eval/ and .ztd/reports/.
func sealedPathAAD(relPath string) ([]byte, bool) {
	clean := path.Clean(filepath.ToSlash(relPath))
	for _, root := range sealedRoots() {
		dir := filepath.ToSlash(root.dir)
		if clean == dir || strings.HasPrefix(clean, dir+"/") {
			return root.aad, true
		}
	}
	return nil, false
}
