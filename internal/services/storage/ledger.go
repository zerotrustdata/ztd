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

// GitLedgerReader reads a runtime's git-backed file ledger. It never writes: opening it performs
// no bootstrap and no read creates a session repository. Read-only consumers (reporting,
// compliance) open the ledger through OpenGitLedgerReader.
type GitLedgerReader struct {
	config      *LedgerConfig
	logger      *slog.Logger
	fileSvc     fs.RuntimeFileService
	baseRelPath string
	// sessionInitMu guards session repository creation, so a reader sees no repository or a
	// complete one without waiting for a ledger write. Reads never take the write lock: git
	// objects are immutable once written and a ref moves atomically.
	sessionInitMu sync.Mutex
}

// GitLedgerService maintains a git-backed version control of all files modified by the operator.
// It is the only ledger writer: BeginMutation and CompleteMutation record every governed file
// mutation.
type GitLedgerService struct {
	*GitLedgerReader
	// mu serializes ledger writes. A write holds it from reading HEAD to moving the ref, never
	// across the host write.
	mu sync.Mutex
}

// LedgerResult contains the result of a file ledger operation.
type LedgerResult struct {
	FilePath string
	// SnapshotName is the keyed name of the file's snapshot (SnapshotName). The file mutation
	// row carries it so the file's commits can be found without walking the ledger.
	SnapshotName     string
	Operation        FileMutationOperation
	LedgerHashBefore string
	LedgerHashAfter  string
	DiffStat         string
	DiffContent      string // Full diff content (raw, unscrubbed)
	Success          bool
	Error            string

	// target is the governed FILE_EDIT target the ledger reads. The caller owns it and keeps it
	// open until CompleteMutation returns.
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

// ── Constructors ────────────────────────────────────────────────────────

// OpenGitLedgerReader opens the runtime ledger for reading only. EncryptionVault in config is
// required. It writes nothing; a ledger that was never bootstrapped fails on first read.
func OpenGitLedgerReader(config *LedgerConfig, logger *slog.Logger, fileSvc fs.RuntimeFileService) (*GitLedgerReader, error) {
	if config == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrLedgerConfigRequired)
	}
	if config.EncryptionVault == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	return &GitLedgerReader{
		config:      config,
		logger:      logger,
		fileSvc:     fileSvc,
		baseRelPath: filepath.Join(constants.DataDirname, constants.LedgerDirname),
	}, nil
}

// NewGitLedgerService creates the ledger writer of a runtime.
// EncryptionVault in config is required for encryption at rest.
// The base directory, files/ git repo, and sessions/ directory are eagerly
// initialized at construction time so the ledger is ready before any
// transactions occur.
func NewGitLedgerService(config *LedgerConfig, logger *slog.Logger, fileSvc fs.RuntimeFileService) (*GitLedgerService, error) {
	reader, err := OpenGitLedgerReader(config, logger, fileSvc)
	if err != nil {
		return nil, err
	}
	s := &GitLedgerService{GitLedgerReader: reader}
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

// ── Session management ──────────────────────────────────────────────────

// openSessionLedger returns the ledger path of an existing session. It is the read-path
// lookup: it never creates anything. A session id that has no ledger yet returns
// ErrLedgerSessionNotFound. The empty session id is the default ledger, which bootstrap
// always creates.
func (s *GitLedgerReader) openSessionLedger(operatorSessionID string) (string, error) {
	if operatorSessionID == "" {
		return s.fileSvc.Resolve(filepath.Join(s.baseRelPath, constants.FilesDirname)), nil
	}

	sessionRelPath := filepath.Join(s.baseRelPath, constants.SessionsDirname, operatorSessionID)

	s.sessionInitMu.Lock()
	defer s.sessionInitMu.Unlock()

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
// does not exist yet. Only BeginMutation calls it; every read uses openSessionLedger.
func (s *GitLedgerService) createSessionLedger(operatorSessionID string) (string, error) {
	if operatorSessionID == "" {
		return s.fileSvc.Resolve(filepath.Join(s.baseRelPath, constants.FilesDirname)), nil
	}

	sessionRelPath := filepath.Join(s.baseRelPath, constants.SessionsDirname, operatorSessionID)
	gitRelPath := filepath.Join(sessionRelPath, constants.GitDirname)

	s.sessionInitMu.Lock()
	defer s.sessionInitMu.Unlock()

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
		Author: ledgerSignature(),
	})
	if err != nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	return nil
}

// ── Path helpers ────────────────────────────────────────────────────────

// normalizeToGitPath converts a file path to a git-relative path with forward slashes.
// This is the single source of truth for path normalization across the ledger service.
func (s *GitLedgerReader) normalizeToGitPath(filePath string) string {
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

// SnapshotName returns the in-tree name of the snapshot of a host path:
// hex(HMAC-SHA256(path)), keyed from the vault. Paths are content, so neither the git tree nor
// any commit message may carry one; the keyed name lets history, diff and restore find a path's
// snapshot without a path-to-name index, and reveals nothing about the path without the vault.
// It fails closed with ErrLedgerVaultRequired while the vault is locked.
func (s *GitLedgerReader) SnapshotName(filePath string) (string, error) {
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

// ── Snapshot layout ─────────────────────────────────────────────────────

// ledgerSnapshotSuffix is appended to every snapshot name. Ledger content is stored only as
// encrypted snapshots; no plaintext copy ever exists in the ledger.
const ledgerSnapshotSuffix = ".enc"

// ledgerSnapshotShardDepth is the number of one-character directories, taken from the leading
// hex characters of the snapshot name, between the snapshots tree and a snapshot. Changing one
// snapshot then rewrites only the trees along its path, each holding at most 16 entries above
// the leaf, instead of one tree that lists every snapshot of the session.
const ledgerSnapshotShardDepth = 4

// ledgerSnapshotAADVersion versions the associated data bound into every snapshot.
const ledgerSnapshotAADVersion = "ztd-ledger-snapshot-v1"

// snapshotTreePath returns the git path of a snapshot, snapshots/a/b/c/d/<name>.enc. It is the
// only path a snapshot is written to or read from.
func snapshotTreePath(name string) string {
	parts := make([]string, 0, ledgerSnapshotShardDepth+2)
	parts = append(parts, constants.LedgerSnapshotsTreeDirname)
	for i := 0; i < ledgerSnapshotShardDepth && i < len(name); i++ {
		parts = append(parts, name[i:i+1])
	}
	return strings.Join(append(parts, name+ledgerSnapshotSuffix), "/")
}

// preLayoutSnapshotPath returns the flat path, files/<name>.enc, at which commits from before the
// sharded layout hold a snapshot. Its content is never read: only the tree entry is checked, to
// tell a pre-layout commit from a missing snapshot. It is also the logical snapshot name bound
// into every snapshot's AAD, which the sharded layout keeps.
func preLayoutSnapshotPath(name string) string {
	return constants.FilesDirname + "/" + name + ledgerSnapshotSuffix
}

// ledgerSnapshotAAD binds a snapshot ciphertext to its logical snapshot name
// (files/<keyed name>.enc), so a blob moved to another file's snapshot fails authentication
// instead of decrypting as that file's content.
func ledgerSnapshotAAD(name string) []byte {
	return []byte(ledgerSnapshotAADVersion + "|" + preLayoutSnapshotPath(name))
}

// snapshotAt returns the snapshot blob of name in tree, or nil when the tree holds no snapshot at
// the sharded path. A missing or unreadable object is an error, never an absent snapshot.
func snapshotAt(st storer.EncodedObjectStorer, tree *object.Tree, name string) (*object.File, error) {
	path := snapshotTreePath(name)
	entry, err := tree.FindEntry(path)
	if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	blob, err := object.GetBlob(st, entry.Hash)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return object.NewFile(path, entry.Mode, blob), nil
}

// requireSnapshotAt returns the snapshot blob of name in tree. A tree with no sharded snapshot
// fails closed: ErrLedgerSnapshotPreLayout when it holds a flat files/<name>.enc entry, because
// the commit predates the sharded layout, and ErrPathNotFound otherwise.
func requireSnapshotAt(st storer.EncodedObjectStorer, tree *object.Tree, name string) (*object.File, error) {
	file, err := snapshotAt(st, tree, name)
	if err != nil || file != nil {
		return file, err
	}
	_, err = tree.FindEntry(preLayoutSnapshotPath(name))
	switch {
	case err == nil:
		return nil, fmt.Errorf("ledger: %w", constants.ErrLedgerSnapshotPreLayout)
	case errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound):
		return nil, fmt.Errorf("ledger: %w", constants.ErrPathNotFound)
	default:
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
}

// vaultUnlocked reports whether the runtime vault can encrypt and decrypt ledger content.
func (s *GitLedgerReader) vaultUnlocked() bool {
	return s.config.EncryptionVault != nil && s.config.EncryptionVault.IsUnlocked()
}

// maxLedgerSnapshotSize is the largest host file the ledger snapshots.
const maxLedgerSnapshotSize = 100 * 1024 * 1024

// readTargetContent reads a governed target for a snapshot through its rooted handle, never a
// raw host path.
func readTargetContent(target *fs.FileEditTarget) ([]byte, error) {
	info, err := target.Stat()
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrStatFailed)
	}
	if info.Size() > maxLedgerSnapshotSize {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	content, err := target.ReadFile()
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrFileOpenFailed)
	}
	return content, nil
}

// ── Mutation ────────────────────────────────────────────────────────────

// Commit messages carry fixed text, operation kind, and identifiers only. A host path is
// content and is never written to a commit message.
const (
	ledgerMsgPreMutation   = "Pre-mutation backup"
	ledgerMsgPostMutation  = "Post-mutation"
	ledgerMsgPostCreation  = "Post-creation"
	ledgerMsgViaSessionFmt = "%s via OperatorSession %s"
)

// BeginMutation begins a governed file mutation in operatorSessionID's ledger. Call
// CompleteMutation after the host write succeeds. The operation follows from the target: a
// target that is not an existing file is a creation, and commits nothing. An existing file is a
// write: its content is committed as a pre-mutation backup only when it differs from the latest
// snapshot, so drift made outside the ledger is captured and an unchanged pre-image is not stored
// again. LedgerHashBefore is that backup commit, or HEAD when nothing was committed. A locked
// vault or a backup that cannot be committed returns an error; the caller refuses the mutation.
func (s *GitLedgerService) BeginMutation(operatorSessionID string, target *fs.FileEditTarget) (*LedgerResult, error) {
	if target == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	if !s.vaultUnlocked() {
		return nil, fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	filePath := target.Path()
	name, err := s.SnapshotName(filePath)
	if err != nil {
		return nil, err
	}

	result := &LedgerResult{
		FilePath:     filePath,
		SnapshotName: name,
		Operation:    FileMutationCreate,
		target:       target,
	}
	var preImage []byte
	if info, statErr := target.Stat(); statErr == nil && !info.IsDir() {
		result.Operation = FileMutationWrite
		if preImage, err = readTargetContent(target); err != nil {
			return nil, fmt.Errorf("ledger: begin write snapshot: %w", err)
		}
	}

	ledgerDir, err := s.createSessionLedger(operatorSessionID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	repo, head, err := openLedgerHead(ledgerDir)
	if err != nil {
		return nil, err
	}
	result.LedgerHashBefore = head.Hash().String()

	if result.Operation == FileMutationWrite {
		unchanged, err := s.matchesLatestSnapshot(repo, head, name, preImage)
		if err != nil {
			return nil, fmt.Errorf("ledger: begin write snapshot: %w", err)
		}
		if !unchanged {
			hashBefore, err := s.commitSnapshot(repo, head, name, preImage, ledgerMsgPreMutation)
			if err != nil {
				return nil, fmt.Errorf("ledger: pre-mutation commit: %w", err)
			}
			result.LedgerHashBefore = hashBefore
		}
	}

	result.Success = true
	return result, nil
}

// matchesLatestSnapshot reports whether the snapshot of name at HEAD holds plaintext. Ciphertexts
// always differ under a fresh nonce, so the comparison is by plaintext.
func (s *GitLedgerService) matchesLatestSnapshot(repo *git.Repository, head *plumbing.Reference, name string, plaintext []byte) (bool, error) {
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return false, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	tree, err := commit.Tree()
	if err != nil {
		return false, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	latest, err := snapshotAt(repo.Storer, tree, name)
	if err != nil || latest == nil {
		return false, err
	}
	content, err := s.decryptSnapshot(latest, name)
	if err != nil {
		return false, err
	}
	return content == string(plaintext), nil
}

// CompleteMutation records the result of a mutation begun by BeginMutation, after the host write.
// The post-mutation snapshot is always committed, even for identical content: each one is
// encrypted under a fresh nonce, so every mutation's after commit changes the snapshot, which
// file history relies on. Any snapshot, commit or diff failure is returned: the mutation happened
// but is not recorded, so the caller fails it.
func (s *GitLedgerService) CompleteMutation(result *LedgerResult, operatorSessionID string) error {
	if result == nil || result.target == nil {
		return fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	if err := s.completeMutation(result, operatorSessionID); err != nil {
		result.Error = err.Error()
		return err
	}
	s.logger.Info("File mutation recorded in ledger",
		"operation", string(result.Operation),
		"hash_before", truncateHash(result.LedgerHashBefore),
		"hash_after", truncateHash(result.LedgerHashAfter),
		"diff_size", len(result.DiffContent))
	return nil
}

func (s *GitLedgerService) completeMutation(result *LedgerResult, operatorSessionID string) error {
	if !s.vaultUnlocked() {
		return fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	ledgerDir, err := s.openSessionLedger(operatorSessionID)
	if err != nil {
		return err
	}
	content, err := readTargetContent(result.target)
	if err != nil {
		return err
	}

	message := ledgerMsgPostMutation
	if result.Operation == FileMutationCreate {
		message = ledgerMsgPostCreation
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	repo, head, err := openLedgerHead(ledgerDir)
	if err != nil {
		return err
	}
	hashAfter, err := s.commitSnapshot(repo, head, result.SnapshotName, content, fmt.Sprintf(ledgerMsgViaSessionFmt, message, operatorSessionID))
	if err != nil {
		return fmt.Errorf("ledger: post-mutation commit: %w", err)
	}
	result.LedgerHashAfter = hashAfter

	diff, err := s.diffCommits(repo, result.LedgerHashBefore, result.LedgerHashAfter, result.FilePath)
	if err != nil {
		return fmt.Errorf("ledger: post-mutation diff: %w", err)
	}
	result.DiffStat = diff.stat
	if result.Operation == FileMutationCreate {
		result.DiffStat = fmt.Sprintf("+%d lines, %d bytes (new file)", countLines(content), len(content))
	}
	result.DiffContent = diff.content
	return nil
}

// commitSnapshot encrypts plaintext as the snapshot of name and commits it on top of head.
func (s *GitLedgerService) commitSnapshot(repo *git.Repository, head *plumbing.Reference, name string, plaintext []byte, message string) (string, error) {
	encrypted, err := s.config.EncryptionVault.EncryptWithAAD(plaintext, ledgerSnapshotAAD(name))
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	hash, err := commitBlob(repo, head, snapshotTreePath(name), encrypted, message)
	if err != nil {
		return "", fmt.Errorf("ledger: %w: %w", constants.ErrInternal, err)
	}
	return hash.String(), nil
}

// RestoreFileFromCommit restores a file to its content at commitHash in sourceSessionID's ledger.
// A restore is a governed file mutation and is recorded like one, through operatorSessionID's
// ledger (the executing session): it refuses to touch the host file unless the source snapshot
// reads and the mutation begins, and any failure to record the restored file is returned. A
// commit from before the sharded layout returns ErrLedgerSnapshotPreLayout and leaves the host
// file untouched. The result carries the before/after hashes and diff stat for the file mutation
// record.
func (s *GitLedgerService) RestoreFileFromCommit(target *fs.FileEditTarget, commitHash, sourceSessionID, operatorSessionID string) (*LedgerResult, error) {
	if target == nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	content, err := s.GetFileAtCommit(target.Path(), commitHash, sourceSessionID)
	if err != nil {
		return nil, err
	}

	result, err := s.BeginMutation(operatorSessionID, target)
	if err != nil {
		return nil, err
	}
	if err := target.WriteFile([]byte(content), constants.PermFilePrivate); err != nil {
		return nil, fmt.Errorf("ledger: %w: %w", constants.ErrInternal, err)
	}
	if err := s.CompleteMutation(result, operatorSessionID); err != nil {
		return nil, fmt.Errorf("ledger: restored file written but not recorded: %w", err)
	}
	result.Operation = FileMutationRestore

	s.logger.Info("File restored from commit",
		"commit", truncateHash(commitHash),
		string(constants.SessionKeyPrefixOperator), operatorSessionID)

	return result, nil
}

// ── History & Retrieval ─────────────────────────────────────────────────

// GetStateMerkleRoot returns the current git commit hash as the state merkle root.
// This provides a BFT-verifiable snapshot of the ledger state at a point in time.
func (s *GitLedgerReader) GetStateMerkleRoot() (string, error) {
	ledgerDir := s.fileSvc.Resolve(filepath.Join(s.baseRelPath, constants.FilesDirname))
	_, head, err := openLedgerHead(ledgerDir)
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

// ListCommits lists commits from the ledger repo for a session (or the files repo if sessionID is empty).
// Returns up to limit commits ordered newest-first (repo.Log order).
func (s *GitLedgerReader) ListCommits(sessionID string, limit int) ([]LedgerCommit, error) {
	if limit <= 0 {
		limit = 500
	}

	ledgerDir, err := s.openSessionLedger(sessionID)
	if err != nil {
		return nil, fmt.Errorf("ledger: list commits: %w", err)
	}

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

// GetFileHistory returns the commits among commitHashes that change filePath's snapshot, in the
// given order, capped at limit. The candidates come from the file's mutation rows
// (ListFileMutationsBySnapshot): a snapshot changes only in the before or after commit of a
// mutation, so the cost follows the file's own history. Walking the commit chain instead reads the
// snapshot tree of every commit in the session. Only the sharded snapshot path is compared, so a
// commit from before the sharded layout is not a history entry. A candidate that cannot be read
// fails closed.
func (s *GitLedgerReader) GetFileHistory(ctx context.Context, filePath string, commitHashes []string, limit int, operatorSessionID string) ([]FileHistoryEntry, error) {
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

	name, err := s.SnapshotName(filePath)
	if err != nil {
		return nil, err
	}

	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}

	var entries []FileHistoryEntry
	for _, commitHash := range commitHashes {
		if len(entries) >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("ledger: file history: %w", err)
		}
		commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
		if err != nil {
			return nil, fmt.Errorf("ledger: file history: %w", err)
		}
		changed, err := commitChangesSnapshot(repo.Storer, commit, name)
		if err != nil {
			return nil, fmt.Errorf("ledger: file history: %w", err)
		}
		if changed {
			entries = append(entries, FileHistoryEntry{
				CommitHash: commit.Hash.String(), Timestamp: commit.Author.When,
				Message: strings.TrimSpace(commit.Message), FilePath: filePath,
			})
		}
	}

	s.logger.Debug("GetFileHistory result", "candidates", len(commitHashes), "entries", len(entries))
	return entries, nil
}

// commitChangesSnapshot reports whether a commit holds a restorable snapshot of name that differs
// from its parents' snapshot.
func commitChangesSnapshot(st storer.EncodedObjectStorer, commit *object.Commit, name string) (bool, error) {
	tree, err := commit.Tree()
	if err != nil {
		return false, err
	}
	snapshot, err := snapshotAt(st, tree, name)
	if err != nil || snapshot == nil {
		return false, err
	}
	for i := range commit.ParentHashes {
		parent, err := commit.Parent(i)
		if err != nil {
			return false, err
		}
		parentTree, err := parent.Tree()
		if err != nil {
			return false, err
		}
		previous, err := snapshotAt(st, parentTree, name)
		if err != nil {
			return false, err
		}
		if previous != nil && snapshot.Hash == previous.Hash {
			return false, nil
		}
	}
	return true, nil
}

// GetFileAtCommit returns the decrypted content of a file's snapshot at a commit. It fails closed:
// ErrLedgerSnapshotPreLayout for a commit from before the sharded layout, ErrPathNotFound when the
// commit holds no snapshot of the file, and ErrLedgerVaultRequired while the vault is locked.
func (s *GitLedgerReader) GetFileAtCommit(filePath, commitHash, operatorSessionID string) (string, error) {
	ledgerDir, err := s.openSessionLedger(operatorSessionID)
	if err != nil {
		return "", err
	}

	name, err := s.SnapshotName(filePath)
	if err != nil {
		return "", err
	}

	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	file, err := requireSnapshotAt(repo.Storer, tree, name)
	if err != nil {
		return "", err
	}
	return s.decryptSnapshot(file, name)
}

// GetDiffContent returns the plaintext diff of one file between two commits of one session. Only
// the named file's snapshot is compared, so a concurrent mutation of another file that landed
// between the two commits is never attributed to this one. Fail-closed: a missing hash, a locked
// vault, an after commit with no snapshot of the file, or an unreadable snapshot is an error,
// never an empty diff.
func (s *GitLedgerReader) GetDiffContent(hashBefore, hashAfter, filePath, operatorSessionID string) (string, error) {
	diff, err := s.diffForSession(hashBefore, hashAfter, filePath, operatorSessionID)
	if err != nil {
		return "", err
	}
	return diff.content, nil
}

// GetDiffStat returns the diff statistics of one file between two commits of one session. It fails
// closed in the same way as GetDiffContent.
func (s *GitLedgerReader) GetDiffStat(hashBefore, hashAfter, filePath, operatorSessionID string) (string, error) {
	diff, err := s.diffForSession(hashBefore, hashAfter, filePath, operatorSessionID)
	if err != nil {
		return "", err
	}
	return diff.stat, nil
}

// diffForSession resolves the existing session ledger and computes the diff, failing closed.
func (s *GitLedgerReader) diffForSession(hashBefore, hashAfter, filePath, operatorSessionID string) (ledgerDiff, error) {
	ledgerDir, err := s.openSessionLedger(operatorSessionID)
	if err != nil {
		return ledgerDiff{}, fmt.Errorf("ledger: session path: %w", err)
	}
	return s.diffBetween(ledgerDir, hashBefore, hashAfter, filePath)
}

// ── Internal git helpers ────────────────────────────────────────────────

// openLedgerHead opens a ledger repository and resolves its HEAD.
func openLedgerHead(ledgerDir string) (*git.Repository, *plumbing.Reference, error) {
	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return repo, head, nil
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

// diffBetween computes the diff of one file between two commits of the ledger at ledgerDir.
func (s *GitLedgerReader) diffBetween(ledgerDir, hashBefore, hashAfter, filePath string) (ledgerDiff, error) {
	repo, err := git.PlainOpen(ledgerDir)
	if err != nil {
		return ledgerDiff{}, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return s.diffCommits(repo, hashBefore, hashAfter, filePath)
}

// diffCommits computes the diff of one file between two ledger commits over decrypted snapshots, in
// memory. Only the snapshot of filePath is compared: the commit range may also contain other files'
// snapshots committed by concurrent mutations, and those are not this file's change. Both hashes
// are required (ErrLedgerDiffHashesRequired) and a locked vault fails closed with
// ErrLedgerVaultRequired. The after commit must hold the snapshot (requireSnapshotAt); a before
// commit without one at the sharded path diffs as a new file, so the first mutation of a file
// after the layout change still diffs against a pre-layout HEAD. The patch carries the caller's
// file path, not the keyed snapshot name.
func (s *GitLedgerReader) diffCommits(repo *git.Repository, hashBefore, hashAfter, filePath string) (ledgerDiff, error) {
	if hashBefore == "" || hashAfter == "" {
		return ledgerDiff{}, fmt.Errorf("ledger: %w", constants.ErrLedgerDiffHashesRequired)
	}
	if filePath == "" {
		return ledgerDiff{}, fmt.Errorf("ledger: diff file path: %w", constants.ErrMissingRequiredField)
	}
	if !s.vaultUnlocked() {
		return ledgerDiff{}, fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	name, err := s.SnapshotName(filePath)
	if err != nil {
		return ledgerDiff{}, err
	}

	treeBefore, err := commitTree(repo, hashBefore)
	if err != nil {
		return ledgerDiff{}, err
	}
	treeAfter, err := commitTree(repo, hashAfter)
	if err != nil {
		return ledgerDiff{}, err
	}

	toFile, err := requireSnapshotAt(repo.Storer, treeAfter, name)
	if err != nil {
		return ledgerDiff{}, err
	}
	toText, err := s.decryptSnapshot(toFile, name)
	if err != nil {
		return ledgerDiff{}, err
	}
	fromFile, err := snapshotAt(repo.Storer, treeBefore, name)
	if err != nil {
		return ledgerDiff{}, err
	}
	fromText := ""
	if fromFile != nil {
		if fromText, err = s.decryptSnapshot(fromFile, name); err != nil {
			return ledgerDiff{}, err
		}
		if fromText == toText {
			return ledgerDiff{}, nil
		}
	}

	displayPath := s.normalizeToGitPath(filePath)
	fp := &ledgerTextPatch{to: &ledgerDiffFile{path: displayPath, hash: toFile.Hash, mode: toFile.Mode}}
	if fromFile != nil {
		fp.from = &ledgerDiffFile{path: displayPath, hash: fromFile.Hash, mode: fromFile.Mode}
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

// commitTree returns the root tree of a ledger commit.
func commitTree(repo *git.Repository, commitHash string) (*object.Tree, error) {
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	return tree, nil
}

// decryptSnapshot returns the plaintext of the encrypted snapshot of name.
func (s *GitLedgerReader) decryptSnapshot(file *object.File, name string) (string, error) {
	if !s.vaultUnlocked() {
		return "", fmt.Errorf("ledger: %w", constants.ErrLedgerVaultRequired)
	}
	ciphertext, err := file.Contents()
	if err != nil {
		return "", fmt.Errorf("ledger: %w", constants.ErrInternal)
	}
	plaintext, err := s.config.EncryptionVault.DecryptWithAAD([]byte(ciphertext), ledgerSnapshotAAD(name))
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
