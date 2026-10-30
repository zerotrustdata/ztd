// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	fdiff "github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	gitdiff "github.com/go-git/go-git/v5/utils/diff"
	dmp "github.com/sergi/go-diff/diffmatchpatch"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
)

// ── Types ───────────────────────────────────────────────────────────────

// LedgerConfig holds configuration for the git-backed ledger service.
type LedgerConfig struct {
	EncryptionVault *vault.Vault // Vault for encryption at rest
}

// GitLedgerService maintains a git-backed version control of all files modified by the operator.
type GitLedgerService struct {
	config      *LedgerConfig
	logger      *slog.Logger
	fileSvc     fs.RuntimeFileService
	baseRelPath string
	mu          sync.Mutex
}

// LedgerResult contains the result of a file ledger operation.
type LedgerResult struct {
	FilePath         string
	Operation        FileMutationOperation
	LedgerHashBefore string
	LedgerHashAfter  string
	DiffStat         string
	DiffContent      string // Full diff content (raw, unscrubbed)
	LedgerPath       string
	Success          bool
	Error            string

	// target is the governed FILE_EDIT target the mirror reads. The caller owns
	// it and keeps it open until the matching Complete* call returns.
	target *fs.FileEditTarget
}

// FileHistoryEntry represents a single entry in a file's history.
type FileHistoryEntry struct {
	CommitHash string
	Timestamp  time.Time
	Message    string
	FilePath   string
}

// LedgerCommit represents a single git commit in the ledger for export.
type LedgerCommit struct {
	CommitHash   string
	ParentHash   string
	TimestampUTC time.Time
	Message      string
	FilesChanged int
	DiffStat     string
}

// ── Constructor ─────────────────────────────────────────────────────────

// NewGitLedgerService creates a new GitLedgerService.
// EncryptionVault in config is required for encryption at rest.
// The base directory, files/ git repo, and sessions/ directory are eagerly
// initialized at construction time so the ledger is ready before any
// transactions occur.
func NewGitLedgerService(config *LedgerConfig, logger *slog.Logger, fileSvc fs.RuntimeFileService) (*GitLedgerService, error) {
	if config == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrLedgerConfigRequired)
	}

	if config.EncryptionVault == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}

	s := &GitLedgerService{
		config:      config,
		logger:      logger,
		fileSvc:     fileSvc,
		baseRelPath: filepath.Join(constants.DataDirname, constants.LedgerDirname),
	}

	if err := s.bootstrap(); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}

	return s, nil
}

// bootstrap creates the base directory structure and initializes the default
// files/ git repo and sessions/ directory eagerly at construction time.
func (s *GitLedgerService) bootstrap() error {
	filesRelPath := filepath.Join(s.baseRelPath, constants.FilesDirname)
	sessionsRelPath := filepath.Join(s.baseRelPath, constants.SessionsDirname)

	dirs := []string{
		s.baseRelPath,
		filesRelPath,
		sessionsRelPath,
	}

	for _, dir := range dirs {
		if err := s.fileSvc.MkdirAll(context.Background(), dir, constants.PermDirPrivate); err != nil {
			return fmt.Errorf("%w: %w", constants.ErrInternal, err)
		}
	}

	if err := s.initGitRepo(filesRelPath); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInternal, err)
	}

	s.logger.Info("Ledger bootstrapped",
		"base_dir", s.fileSvc.Resolve(s.baseRelPath),
		"files_dir", s.fileSvc.Resolve(filesRelPath),
		"sessions_dir", s.fileSvc.Resolve(sessionsRelPath))

	return nil
}

// ── State ───────────────────────────────────────────────────────────────

// ── Session management ──────────────────────────────────────────────────

// openSessionLedger returns the ledger path of an existing session. It is the read-path
// lookup: it never creates anything. A session id that has no ledger yet returns
// ErrLedgerSessionNotFound. The empty session id is the default ledger, which bootstrap
// always creates.
func (s *GitLedgerService) openSessionLedger(operatorSessionID string) (string, error) {
	if operatorSessionID == "" {
		return s.fileSvc.Resolve(filepath.Join(s.baseRelPath, constants.FilesDirname)), nil
	}

	sessionRelPath := filepath.Join(s.baseRelPath, constants.SessionsDirname, operatorSessionID)

	// Creation holds s.mu for the whole init, so a reader sees no repository or a complete one.
	s.mu.Lock()
	defer s.mu.Unlock()

	exists, err := s.fileSvc.FileExists(context.Background(), filepath.Join(sessionRelPath, constants.GitDirname))
	if err != nil {
		return "", fmt.Errorf("ledger: session lookup: %w", err)
	}
	if !exists {
		return "", fmt.Errorf("ledger: %w", constants.ErrLedgerSessionNotFound)
	}
	return s.fileSvc.Resolve(sessionRelPath), nil
}

// createSessionLedger returns the ledger path of a session, creating its repository when it
// does not exist yet. Only the mutation-begin paths call it; every read uses openSessionLedger.
func (s *GitLedgerService) createSessionLedger(operatorSessionID string) (string, error) {
	if operatorSessionID == "" {
		return s.fileSvc.Resolve(filepath.Join(s.baseRelPath, constants.FilesDirname)), nil
	}

	sessionRelPath := filepath.Join(s.baseRelPath, constants.SessionsDirname, operatorSessionID)
	gitRelPath := filepath.Join(sessionRelPath, constants.GitDirname)

	s.mu.Lock()
	defer s.mu.Unlock()

	exists, err := s.fileSvc.FileExists(context.Background(), gitRelPath)
	if err != nil {
		return "", fmt.Errorf("ledger: session lookup: %w", err)
	}
	if exists {
		return s.fileSvc.Resolve(sessionRelPath), nil
	}

	if err := s.fileSvc.MkdirAll(context.Background(), sessionRelPath, constants.PermDirPrivate); err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	if err := s.initGitRepo(sessionRelPath); err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	s.logger.Info("Initialized new session ledger", "operator_session_id", operatorSessionID)
	return s.fileSvc.Resolve(sessionRelPath), nil
}

// initGitRepo initializes a git repository in the specified directory using native go-git.
func (s *GitLedgerService) initGitRepo(relPath string) error {
	gitRelPath := filepath.Join(relPath, constants.GitDirname)

	exists, _ := s.fileSvc.FileExists(context.Background(), gitRelPath)
	if exists {
		return nil
	}

	absPath := s.fileSvc.Resolve(relPath)
	repo, err := git.PlainInit(absPath, false)
	if err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	gitignoreRelPath := filepath.Join(relPath, constants.GitignoreFilename)
	if err := s.fileSvc.WriteFile(context.Background(), gitignoreRelPath, []byte("# ztd Ledger\n"), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	w, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	if _, err := w.Add(constants.GitignoreFilename); err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	_, err = w.Commit("Initial ledger commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "ztd-operator",
			Email: "ztd-operator@system",
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	return nil
}

// ── Path helpers ────────────────────────────────────────────────────────

// normalizeToGitPath converts a file path to a git-relative path with forward slashes.
// This is the single source of truth for path normalization across the ledger service.
func (s *GitLedgerService) normalizeToGitPath(filePath string) string {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}

	cleanPath := absPath
	// Remove Windows drive letter (e.g., "C:")
	if len(cleanPath) >= 2 && cleanPath[1] == ':' {
		cleanPath = cleanPath[2:]
	}
	// Remove leading slash/backslash
	cleanPath = strings.TrimPrefix(cleanPath, "/")
	cleanPath = strings.TrimPrefix(cleanPath, "\\")
	// Convert backslashes to forward slashes for consistent git paths
	cleanPath = strings.ReplaceAll(cleanPath, "\\", "/")

	return cleanPath
}

// ledgerPathKeyLabel separates the ledger file-name key from every other key derived
// from the vault DEK.
const ledgerPathKeyLabel = "ztd-ledger-path-v1"

// snapshotName returns the on-disk and in-tree name of the snapshot of a host path:
// hex(HMAC-SHA256(path)), keyed from the vault. Paths are content, so neither the ledger
// directory, the git tree, nor any commit message may carry one; the keyed name lets
// history, diff and restore find a path's snapshot without a path-to-name index, and
// reveals nothing about the path without the vault. It fails closed with
// ErrLedgerVaultRequired while the vault is locked.
func (s *GitLedgerService) snapshotName(filePath string) (string, error) {
	if s.config.EncryptionVault == nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	mac, err := s.config.EncryptionVault.HMACSHA256(ledgerPathKeyLabel, []byte(s.normalizeToGitPath(filePath)))
	if err != nil {
		if errors.Is(err, constants.ErrVaultLocked) {
			return "", fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
		}
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return hex.EncodeToString(mac), nil
}

// getLedgerPath returns the path where a file is mirrored in the ledger, without the
// snapshot suffix. The final element is the keyed snapshot name, never a host path.
func (s *GitLedgerService) getLedgerPath(ledgerDir, filePath string) (string, error) {
	name, err := s.snapshotName(filePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(ledgerDir, constants.FilesDirname, name), nil
}

// getGitRelativePath returns the git-relative path of a file's snapshot, without the
// snapshot suffix (always with forward slashes).
func (s *GitLedgerService) getGitRelativePath(filePath string) (string, error) {
	name, err := s.snapshotName(filePath)
	if err != nil {
		return "", err
	}
	return constants.FilesDirname + "/" + name, nil
}

// ── File copy ───────────────────────────────────────────────────────────

// ledgerSnapshotSuffix is appended to every ledger file name. Ledger content is
// stored only as encrypted snapshots; no plaintext copy ever exists in the ledger.
const ledgerSnapshotSuffix = ".enc"

// ledgerSnapshotAADVersion versions the associated data bound into every snapshot.
const ledgerSnapshotAADVersion = "ztd-ledger-snapshot-v1"

// ledgerSnapshotAAD binds a snapshot ciphertext to its git-relative snapshot path
// (files/<keyed name>.enc), so a blob moved to another file's snapshot fails
// authentication instead of decrypting as that file's content.
func ledgerSnapshotAAD(snapshotPath string) []byte {
	return []byte(ledgerSnapshotAADVersion + "|" + snapshotPath)
}

// ledgerSnapshotGitPath returns the git-relative snapshot path of a ledger path
// (an absolute or runtime-relative path ending in files/<keyed name>).
func ledgerSnapshotGitPath(ledgerPath string) string {
	return constants.FilesDirname + "/" + filepath.Base(ledgerPath) + ledgerSnapshotSuffix
}

// vaultUnlocked reports whether the runtime vault can encrypt and decrypt ledger content.
func (s *GitLedgerService) vaultUnlocked() bool {
	return s.config.EncryptionVault != nil && s.config.EncryptionVault.IsUnlocked()
}

// copyToLedger copies a governed target into the ledger as an encrypted snapshot
// at dstPath+".enc". It fails closed with ErrLedgerVaultRequired when the vault is
// nil or locked and writes nothing in that case. Reads go through the target's
// rooted handle, never a raw host path.
func (s *GitLedgerService) copyToLedger(target *fs.FileEditTarget, dstPath string) (err error) {
	if target == nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	if !s.vaultUnlocked() {
		return fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	relDstPath, err := s.fileSvc.Rel(dstPath)
	if err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	info, err := target.Stat()
	if err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrStatFailed)
	}

	const maxEncryptedSize = 100 * 1024 * 1024 // 100MB safety limit
	if info.Size() > maxEncryptedSize {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	content, err := target.ReadFile()
	if err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrFileOpenFailed)
	}

	encrypted, err := s.config.EncryptionVault.EncryptWithAAD(content, ledgerSnapshotAAD(ledgerSnapshotGitPath(dstPath)))
	if err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	if err := s.fileSvc.WriteFile(context.Background(), relDstPath+ledgerSnapshotSuffix, encrypted, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return nil
}

// ── Two-phase mirror: Write ─────────────────────────────────────────────

// Commit messages carry fixed text, operation kind, and identifiers only. A host path is
// content and is never written to a commit message.
const (
	ledgerMsgPreMutation   = "Pre-mutation backup"
	ledgerMsgPostMutation  = "Post-mutation"
	ledgerMsgPreCreation   = "Pre-creation state"
	ledgerMsgPostCreation  = "Post-creation"
	ledgerMsgViaSessionFmt = "%s via OperatorSession %s"
)

// LedgerFileWrite begins the two-phase commit for a file write. Call CompleteMirrorWrite after the write.
// A pre-mutation snapshot that cannot be taken or committed returns an error; the caller refuses the mutation.
func (s *GitLedgerService) LedgerFileWrite(operatorSessionID string, target *fs.FileEditTarget) (*LedgerResult, error) {
	if target == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	if !s.vaultUnlocked() {
		return nil, fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	filePath := target.Path()

	ledgerDir, err := s.createSessionLedger(operatorSessionID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result := &LedgerResult{
		FilePath:  filePath,
		Operation: FileMutationWrite,
		target:    target,
	}

	ledgerPath, err := s.getLedgerPath(ledgerDir, filePath)
	if err != nil {
		return nil, err
	}
	result.LedgerPath = ledgerPath

	if _, err := target.Stat(); err == nil {
		if err := s.copyToLedger(target, ledgerPath); err != nil {
			return nil, fmt.Errorf("ledger: begin write snapshot: %w", err)
		}
	}

	hashBefore, err := s.snapshotLedger(ledgerDir, ledgerMsgPreMutation)
	if err != nil {
		return nil, fmt.Errorf("ledger: pre-mutation commit: %w", err)
	}
	result.LedgerHashBefore = hashBefore

	result.Success = true
	return result, nil
}

// CompleteMirrorWrite completes the mirror operation after the file write. Any snapshot, commit
// or diff failure is returned: the mutation happened but is not recorded, so the caller fails it.
func (s *GitLedgerService) CompleteMirrorWrite(result *LedgerResult, operatorSessionID string) error {
	if result == nil {
		return nil
	}

	ledgerDir, err := s.openSessionLedger(operatorSessionID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.copyToLedger(result.target, result.LedgerPath); err != nil {
		result.Error = err.Error()
		return err
	}

	hashAfter, err := s.snapshotLedger(ledgerDir, fmt.Sprintf(ledgerMsgViaSessionFmt, ledgerMsgPostMutation, operatorSessionID))
	if err != nil {
		result.Error = err.Error()
		return fmt.Errorf("ledger: post-mutation commit: %w", err)
	}
	result.LedgerHashAfter = hashAfter

	diff, err := s.diffBetween(ledgerDir, result.LedgerHashBefore, result.LedgerHashAfter, result.FilePath)
	if err != nil {
		result.Error = err.Error()
		return fmt.Errorf("ledger: post-mutation diff: %w", err)
	}
	result.DiffStat = diff.stat
	result.DiffContent = diff.content

	s.logger.Info("File mutation mirrored",
		"hash_before", truncateHash(result.LedgerHashBefore),
		"hash_after", truncateHash(result.LedgerHashAfter),
		"diff_size", len(result.DiffContent))

	return nil
}

// ── Two-phase mirror: Create ────────────────────────────────────────────

// MirrorFileCreate begins the two-phase commit for a file creation. Call CompleteMirrorCreate after the creation.
func (s *GitLedgerService) MirrorFileCreate(operatorSessionID string, target *fs.FileEditTarget) (*LedgerResult, error) {
	if target == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	if !s.vaultUnlocked() {
		return nil, fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	filePath := target.Path()

	ledgerDir, err := s.createSessionLedger(operatorSessionID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result := &LedgerResult{
		FilePath:  filePath,
		Operation: FileMutationCreate,
		target:    target,
	}

	ledgerPath, err := s.getLedgerPath(ledgerDir, filePath)
	if err != nil {
		return nil, err
	}
	result.LedgerPath = ledgerPath

	hashBefore, err := s.snapshotLedger(ledgerDir, ledgerMsgPreCreation)
	if err != nil {
		return nil, fmt.Errorf("ledger: pre-creation commit: %w", err)
	}
	result.LedgerHashBefore = hashBefore

	result.Success = true
	return result, nil
}

// CompleteMirrorCreate completes the mirror operation after file creation. Any snapshot, commit
// or diff failure is returned: the creation happened but is not recorded, so the caller fails it.
func (s *GitLedgerService) CompleteMirrorCreate(result *LedgerResult, operatorSessionID string) error {
	if result == nil {
		return nil
	}

	ledgerDir, err := s.openSessionLedger(operatorSessionID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.copyToLedger(result.target, result.LedgerPath); err != nil {
		result.Error = err.Error()
		return err
	}

	hashAfter, err := s.snapshotLedger(ledgerDir, fmt.Sprintf(ledgerMsgViaSessionFmt, ledgerMsgPostCreation, operatorSessionID))
	if err != nil {
		result.Error = err.Error()
		return fmt.Errorf("ledger: post-creation commit: %w", err)
	}
	result.LedgerHashAfter = hashAfter

	content, err := result.target.ReadFile()
	if err != nil {
		result.Error = err.Error()
		return fmt.Errorf("ledger: %w", constants.ErrFileOpenFailed)
	}
	result.DiffStat = fmt.Sprintf("+%d lines, %d bytes (new file)", countLines(content), len(content))

	diff, err := s.diffBetween(ledgerDir, result.LedgerHashBefore, result.LedgerHashAfter, result.FilePath)
	if err != nil {
		result.Error = err.Error()
		return fmt.Errorf("ledger: post-creation diff: %w", err)
	}
	result.DiffContent = diff.content

	s.logger.Info("File creation mirrored",
		"hash_after", truncateHash(result.LedgerHashAfter),
		"diff_size", len(result.DiffContent))

	return nil
}

// ── History & Retrieval ─────────────────────────────────────────────────

// GetStateMerkleRoot returns the current git commit hash as the state merkle root.
// This provides a BFT-verifiable snapshot of the ledger state at a point in time.
func (s *GitLedgerService) GetStateMerkleRoot() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ledgerDir := s.fileSvc.Resolve(filepath.Join(s.baseRelPath, constants.FilesDirname))
	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	ref, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return ref.Hash().String(), nil
}

// ListCommits lists commits from the ledger repo for a session (or the files repo if sessionID is empty).
// Returns up to limit commits ordered newest-first (repo.Log order), then reversed to oldest-first for the CSV.
func (s *GitLedgerService) ListCommits(sessionID string, limit int) ([]LedgerCommit, error) {
	if limit <= 0 {
		limit = 500
	}

	ledgerDir, err := s.openSessionLedger(sessionID)
	if err != nil {
		return nil, fmt.Errorf("ledger: list commits: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return nil, fmt.Errorf("ledger: list commits: %w", constants.ErrInternal)
	}

	cIter, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("ledger: list commits: %w", constants.ErrInternal)
	}
	defer cIter.Close()

	var commits []LedgerCommit
	count := 0
	err = cIter.ForEach(func(c *object.Commit) error {
		if count >= limit {
			return storer.ErrStop
		}

		parentHash := ""
		if c.NumParents() > 0 {
			parentHash = c.ParentHashes[0].String()
		}

		stats, _ := c.Stats()
		filesChanged := 0
		diffStat := ""
		if stats != nil {
			filesChanged = len(stats)
			diffStat = stats.String()
		}

		commits = append(commits, LedgerCommit{
			CommitHash:   c.Hash.String(),
			ParentHash:   parentHash,
			TimestampUTC: c.Author.When.UTC(),
			Message:      strings.TrimSpace(c.Message),
			FilesChanged: filesChanged,
			DiffStat:     diffStat,
		})
		count++
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: list commits: %w", constants.ErrInternal)
	}

	return commits, nil
}

// GetFileHistory retrieves the git history for a specific file.
func (s *GitLedgerService) GetFileHistory(ctx context.Context, filePath string, limit int, operatorSessionID string) ([]FileHistoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("ledger: file history: %w", err)
	}
	if limit <= 0 {
		limit = 50
	}

	ledgerDir, err := s.openSessionLedger(operatorSessionID)
	if err != nil {
		return nil, err
	}

	relPath, err := s.getGitRelativePath(filePath)
	if err != nil {
		return nil, err
	}

	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	// Look up only this snapshot in each commit and its parents. Enumerating
	// every file in every historical tree scales with the entire ledger and
	// incorrectly reports unrelated commits as file changes.
	encryptedPath := relPath + ledgerSnapshotSuffix
	cIter, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("ledger: file history log: %w", err)
	}
	defer cIter.Close()

	var entries []FileHistoryEntry
	err = cIter.ForEach(func(c *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(entries) >= limit {
			return storer.ErrStop
		}
		snapshot, err := c.File(encryptedPath)
		if errors.Is(err, object.ErrFileNotFound) {
			return nil // Only restorable snapshots belong in file history.
		}
		if err != nil {
			return err
		}
		for i := range c.ParentHashes {
			if err := ctx.Err(); err != nil {
				return err
			}
			parent, err := c.Parent(i)
			if err != nil {
				return err
			}
			previous, err := parent.File(encryptedPath)
			if errors.Is(err, object.ErrFileNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if snapshot.Hash == previous.Hash {
				return nil
			}
		}
		entries = append(entries, FileHistoryEntry{
			CommitHash: c.Hash.String(), Timestamp: c.Author.When,
			Message: strings.TrimSpace(c.Message), FilePath: filePath,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: file history: %w", err)
	}

	s.logger.Debug("GetFileHistory result", "entries", len(entries))
	return entries, nil
}

// GetFileAtCommit retrieves the content of a file at a specific commit, decrypting if the vault is unlocked.
func (s *GitLedgerService) GetFileAtCommit(filePath, commitHash, operatorSessionID string) (string, error) {
	ledgerDir, err := s.openSessionLedger(operatorSessionID)
	if err != nil {
		return "", err
	}

	relPath, err := s.getGitRelativePath(filePath)
	if err != nil {
		return "", err
	}

	encryptedRelPath := relPath + ledgerSnapshotSuffix
	content, err := s.gitShowFile(ledgerDir, commitHash, encryptedRelPath)
	if err != nil {
		return "", err
	}

	decrypted, err := s.config.EncryptionVault.DecryptWithAAD([]byte(content), ledgerSnapshotAAD(encryptedRelPath))
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return string(decrypted), nil
}

// RestoreFileFromCommit restores a file to its content at commitHash in sourceSessionID's ledger.
// A restore is a governed file write and is mirrored like one, through operatorSessionID's ledger
// (the executing session): it refuses to touch the host file unless the pre-mutation snapshot
// succeeds, and any failure to mirror the restored file is returned. The result carries the
// before/after hashes and diff stat for the file mutation record.
func (s *GitLedgerService) RestoreFileFromCommit(target *fs.FileEditTarget, commitHash, sourceSessionID, operatorSessionID string) (*LedgerResult, error) {
	if target == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	content, err := s.GetFileAtCommit(target.Path(), commitHash, sourceSessionID)
	if err != nil {
		return nil, err
	}

	result, err := s.LedgerFileWrite(operatorSessionID, target)
	if err != nil {
		return nil, err
	}
	if err := target.WriteFile([]byte(content), constants.PermFilePrivate); err != nil {
		return nil, fmt.Errorf("ledger: %w: %w", constants.ErrInternal, err)
	}
	if err := s.CompleteMirrorWrite(result, operatorSessionID); err != nil {
		return nil, fmt.Errorf("ledger: restored file written but not mirrored: %w", err)
	}
	result.Operation = FileMutationRestore

	s.logger.Info("File restored from commit",
		"commit", truncateHash(commitHash),
		string(constants.SessionKeyPrefixOperator), operatorSessionID)

	return result, nil
}

// GetDiffContent returns the plaintext diff of one file between two commits of one session. Only
// the named file's snapshot is compared, so a concurrent mutation of another file that landed
// between the two commits is never attributed to this one. Fail-closed: a missing hash, a locked
// vault, or an unreadable snapshot is an error, never an empty diff.
func (s *GitLedgerService) GetDiffContent(hashBefore, hashAfter, filePath, operatorSessionID string) (string, error) {
	diff, err := s.diffForSession(hashBefore, hashAfter, filePath, operatorSessionID)
	if err != nil {
		return "", err
	}
	return diff.content, nil
}

// GetDiffStat returns the diff statistics of one file between two commits of one session. It fails
// closed in the same way as GetDiffContent.
func (s *GitLedgerService) GetDiffStat(hashBefore, hashAfter, filePath, operatorSessionID string) (string, error) {
	diff, err := s.diffForSession(hashBefore, hashAfter, filePath, operatorSessionID)
	if err != nil {
		return "", err
	}
	return diff.stat, nil
}

// diffForSession resolves the existing session ledger and computes the diff, failing closed.
func (s *GitLedgerService) diffForSession(hashBefore, hashAfter, filePath, operatorSessionID string) (ledgerDiff, error) {
	ledgerDir, err := s.openSessionLedger(operatorSessionID)
	if err != nil {
		return ledgerDiff{}, fmt.Errorf("ledger: session path: %w", err)
	}
	return s.diffBetween(ledgerDir, hashBefore, hashAfter, filePath)
}

// ── Internal git helpers ────────────────────────────────────────────────

// snapshotLedger creates a git commit and returns the commit hash.
func (s *GitLedgerService) snapshotLedger(ledgerDir, message string) (string, error) {
	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	w, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	err = w.AddWithOptions(&git.AddOptions{All: true})
	if err != nil && err != git.ErrEmptyCommit {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	commitMsg := fmt.Sprintf("[%s] %s", time.Now().UTC().Format(time.RFC3339), message)
	hash, err := w.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "ztd-operator",
			Email: "ztd-operator@system",
			When:  time.Now(),
		},
		AllowEmptyCommits: true,
	})
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	return hash.String(), nil
}

// ledgerDiff is the rendered plaintext diff between two ledger commits.
type ledgerDiff struct {
	stat    string
	content string
}

// ledgerDiffFile is the logical (decrypted) path and identity of one side of a diff.
type ledgerDiffFile struct {
	path string
	hash plumbing.Hash
	mode filemode.FileMode
}

func (f *ledgerDiffFile) Hash() plumbing.Hash     { return f.hash }
func (f *ledgerDiffFile) Mode() filemode.FileMode { return f.mode }
func (f *ledgerDiffFile) Path() string            { return f.path }

// ledgerTextChunk is one line-oriented segment of a plaintext file diff.
type ledgerTextChunk struct {
	content string
	op      fdiff.Operation
}

func (c *ledgerTextChunk) Content() string       { return c.content }
func (c *ledgerTextChunk) Type() fdiff.Operation { return c.op }

// ledgerTextPatch is the plaintext diff of one file between two ledger commits.
type ledgerTextPatch struct {
	from, to fdiff.File
	chunks   []fdiff.Chunk
}

func (p *ledgerTextPatch) IsBinary() bool                  { return false }
func (p *ledgerTextPatch) Files() (fdiff.File, fdiff.File) { return p.from, p.to }
func (p *ledgerTextPatch) Chunks() []fdiff.Chunk           { return p.chunks }

// ledgerPatch is the plaintext diff of every file changed between two ledger commits.
type ledgerPatch []fdiff.FilePatch

func (p ledgerPatch) FilePatches() []fdiff.FilePatch { return p }
func (p ledgerPatch) Message() string                { return "" }

// diffBetween computes the diff of one file between two ledger commits over decrypted snapshots, in
// memory. Only the snapshot of filePath is compared: the commit range may also contain other files'
// snapshots committed by concurrent mutations, and those are not this file's change. Both hashes
// are required (ErrLedgerDiffHashesRequired) and a locked vault fails closed with
// ErrLedgerVaultRequired. The patch carries the caller's file path, not the keyed snapshot name.
func (s *GitLedgerService) diffBetween(ledgerDir, hashBefore, hashAfter, filePath string) (ledgerDiff, error) {
	if hashBefore == "" || hashAfter == "" {
		return ledgerDiff{}, fmt.Errorf("ledger: %w", constants.ErrLedgerDiffHashesRequired)
	}
	if filePath == "" {
		return ledgerDiff{}, fmt.Errorf("ledger: diff file path: %w", constants.ErrMissingRequiredField)
	}
	if !s.vaultUnlocked() {
		return ledgerDiff{}, fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	relPath, err := s.getGitRelativePath(filePath)
	if err != nil {
		return ledgerDiff{}, err
	}
	snapshotPath := relPath + ledgerSnapshotSuffix

	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return ledgerDiff{}, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	commitBefore, err := repo.CommitObject(plumbing.NewHash(hashBefore))
	if err != nil {
		return ledgerDiff{}, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	commitAfter, err := repo.CommitObject(plumbing.NewHash(hashAfter))
	if err != nil {
		return ledgerDiff{}, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	fromText, fromFile, err := s.snapshotAtCommit(commitBefore, snapshotPath)
	if err != nil {
		return ledgerDiff{}, err
	}
	toText, toFile, err := s.snapshotAtCommit(commitAfter, snapshotPath)
	if err != nil {
		return ledgerDiff{}, err
	}
	if (fromFile == nil && toFile == nil) || (fromFile != nil && toFile != nil && fromText == toText) {
		return ledgerDiff{}, nil
	}

	displayPath := s.normalizeToGitPath(filePath)
	fp := &ledgerTextPatch{}
	if fromFile != nil {
		fp.from = &ledgerDiffFile{path: displayPath, hash: fromFile.Hash, mode: fromFile.Mode}
	}
	if toFile != nil {
		fp.to = &ledgerDiffFile{path: displayPath, hash: toFile.Hash, mode: toFile.Mode}
	}
	for _, d := range gitdiff.Do(fromText, toText) {
		var op fdiff.Operation
		switch d.Type {
		case dmp.DiffEqual:
			op = fdiff.Equal
		case dmp.DiffDelete:
			op = fdiff.Delete
		case dmp.DiffInsert:
			op = fdiff.Add
		}
		fp.chunks = append(fp.chunks, &ledgerTextChunk{content: d.Text, op: op})
	}

	patch := ledgerPatch{fp}
	return ledgerDiff{
		stat:    formatLedgerDiffStat(patch),
		content: renderLedgerPatch(patch),
	}, nil
}

// snapshotAtCommit returns the decrypted content of one snapshot at a commit. A snapshot that does
// not exist at the commit returns ("", nil, nil); every other failure is an error.
func (s *GitLedgerService) snapshotAtCommit(commit *object.Commit, snapshotPath string) (string, *object.File, error) {
	file, err := commit.File(snapshotPath)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	text, err := s.decryptSnapshot(file, snapshotPath)
	if err != nil {
		return "", nil, err
	}
	return text, file, nil
}

// decryptSnapshot returns the plaintext of one encrypted ledger snapshot stored at snapshotPath.
func (s *GitLedgerService) decryptSnapshot(file *object.File, snapshotPath string) (string, error) {
	ciphertext, err := file.Contents()
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	plaintext, err := s.config.EncryptionVault.DecryptWithAAD([]byte(ciphertext), ledgerSnapshotAAD(snapshotPath))
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return string(plaintext), nil
}

// renderLedgerPatch renders a plaintext patch in git unified-diff form.
func renderLedgerPatch(patch ledgerPatch) string {
	if len(patch) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := fdiff.NewUnifiedEncoder(&buf, 3).Encode(patch); err != nil {
		return ""
	}
	return buf.String()
}

// formatLedgerDiffStat summarizes a plaintext patch as "N files changed, X insertions(+), Y deletions(-)".
func formatLedgerDiffStat(patch ledgerPatch) string {
	if len(patch) == 0 {
		return ""
	}
	totalAdd := 0
	totalDel := 0
	for _, fp := range patch {
		for _, c := range fp.Chunks() {
			switch c.Type() {
			case fdiff.Add:
				totalAdd += countLedgerLines(c.Content())
			case fdiff.Delete:
				totalDel += countLedgerLines(c.Content())
			}
		}
	}

	numFiles := len(patch)
	var parts []string
	if numFiles == 1 {
		parts = append(parts, "1 file changed")
	} else {
		parts = append(parts, fmt.Sprintf("%d files changed", numFiles))
	}

	if totalAdd > 0 {
		if totalAdd == 1 {
			parts = append(parts, "1 insertion(+)")
		} else {
			parts = append(parts, fmt.Sprintf("%d insertions(+)", totalAdd))
		}
	}

	if totalDel > 0 {
		if totalDel == 1 {
			parts = append(parts, "1 deletion(-)")
		} else {
			parts = append(parts, fmt.Sprintf("%d deletions(-)", totalDel))
		}
	}

	return strings.Join(parts, ", ")
}

// countLedgerLines counts the lines in a diff chunk; a trailing partial line counts as one line.
func countLedgerLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// gitShowFile retrieves a file's content at a specific commit.
func (s *GitLedgerService) gitShowFile(ledgerDir, commitHash, relPath string) (string, error) {
	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	file, err := commit.File(relPath)
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrPathNotFound)
	}

	content, err := file.Contents()
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	return content, nil
}

// countLines counts the number of lines in file content.
func countLines(content []byte) int {
	return strings.Count(string(content), "\n") + 1
}

// ── Utilities ─────────────────────────────────────────────────────────

// truncateHash safely truncates a git hash for logging.
func truncateHash(hash string) string {
	if len(hash) >= 12 {
		return hash[:12]
	}
	return hash
}
