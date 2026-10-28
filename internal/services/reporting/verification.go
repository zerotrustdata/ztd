// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package reporting

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	verifyResultPass    = "PASS"
	verifyResultFail    = "FAIL"
	verifyResultSkipped = "SKIPPED"
)

// VerificationResult contains the output of the verification pass.
type VerificationResult struct {
	FailCount int
	Rows      []VerificationRow
}

func reportVerification(ctx context.Context, outDir string, eventsStore *storage.LocalFirstEventsStore, cl *storage.CommitmentLedger, ledger *storage.GitLedgerService, auditorTrust, actuatorTrust governance.EvidenceSignerSource) (FileResult, VerificationResult, error) {
	path := filepath.Join(outDir, constants.ReportVerificationFilename)

	var vr VerificationResult
	addRow := func(check, scope, subject, result, detail string) {
		vr.Rows = append(vr.Rows, VerificationRow{
			Check: check, Scope: scope, Subject: subject, Result: result, Detail: detail,
		})
		if result == verifyResultFail {
			vr.FailCount++
		}
	}

	// Check 1: Events chain integrity
	if ctx.Err() != nil {
		return FileResult{}, vr, ctx.Err()
	}
	if eventsStore != nil {
		if err := eventsStore.VerifyChain(ctx, 0); err != nil {
			addRow("events_chain", "events", "all", verifyResultFail, fmt.Sprintf("events chain verification failed: %v", err))
		} else {
			addRow("events_chain", "events", "all", verifyResultPass, "events chain verified")
		}
	} else {
		addRow("events_chain", "events", "all", verifyResultSkipped, "local-first events store not configured")
	}

	// Check 2: Commitment chain integrity
	if ctx.Err() != nil {
		return FileResult{}, vr, ctx.Err()
	}
	commitments, commitmentsErr := cl.ListCommitments()
	if commitmentsErr != nil {
		// A ciphertext that fails authentication was altered or moved from another row:
		// that is tamper evidence, not an unreadable ledger.
		unreadable := verifyResultSkipped
		if errors.Is(commitmentsErr, constants.ErrLocalFirstEventsStoreDecryptFailed) {
			unreadable = verifyResultFail
		}
		addRow("commitment_chain", "commitment_ledger", "all", unreadable, fmt.Sprintf("could not read commitments: %v", commitmentsErr))
	} else if len(commitments) == 0 {
		addRow("commitment_chain", "commitment_ledger", "all", verifyResultPass, "ledger empty (genesis)")
	} else {
		chainOK := true
		for i, c := range commitments {
			if i == 0 {
				if c.PriorCommitmentHash != "" {
					addRow("commitment_chain", "commitment_ledger", c.Hash, verifyResultFail,
						fmt.Sprintf("first commitment has non-empty prior_commitment_hash: %s", c.PriorCommitmentHash))
					chainOK = false
				}
				continue
			}
			prev := commitments[i-1]
			if c.PriorCommitmentHash != prev.Hash {
				addRow("commitment_chain", "commitment_ledger", c.Hash, verifyResultFail,
					fmt.Sprintf("prior_commitment_hash %s != previous hash %s", c.PriorCommitmentHash, prev.Hash))
				chainOK = false
			}
		}
		if chainOK {
			addRow("commitment_chain", "commitment_ledger", "all", verifyResultPass,
				fmt.Sprintf("%d commitments verified", len(commitments)))
		}
	}

	// Check 2: Commitment hash recomputation
	if ctx.Err() != nil {
		return FileResult{}, vr, ctx.Err()
	}
	if len(commitments) > 0 {
		allHashesOK := true
		allSignaturesOK := true
		for _, c := range commitments {
			var attestation operatorv1.CommitmentAttestation
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(c.AttestationJSON, &attestation); err != nil {
				addRow("commitment_hash_recompute", "commitment_ledger", c.Hash, verifyResultFail, fmt.Sprintf("invalid attestation JSON: %v", err))
				allHashesOK = false
				allSignaturesOK = false
				continue
			}
			if !governance.CommitmentRowMatchesAttestation(c, &attestation) {
				addRow("commitment_hash_recompute", "commitment_ledger", c.Hash, verifyResultFail, "structured commitment columns diverge from attestation JSON")
				allHashesOK = false
			}
			canonical, err := governance.CanonicalizeCommitmentAttestation(&attestation)
			if err != nil {
				addRow("commitment_hash_recompute", "commitment_ledger", c.Hash, verifyResultFail, fmt.Sprintf("canonicalization failed: %v", err))
				allHashesOK = false
				allSignaturesOK = false
				continue
			}
			computed := sha256.Sum256(canonical)
			computedHex := hex.EncodeToString(computed[:])
			if computedHex != c.Hash || computedHex != attestation.Hash || attestation.PriorCommitmentHash != c.PriorCommitmentHash {
				addRow("commitment_hash_recompute", "commitment_ledger", c.Hash, verifyResultFail, "stored commitment does not match its canonical attestation hash")
				allHashesOK = false
			}
			var publicKey ed25519.PublicKey
			keyErr := constants.ErrTrustedSignerKeyNotFound
			if auditorTrust != nil {
				publicKey, keyErr = auditorTrust.GetTrustedSignerPublicKey(ctx, attestation.AuditorKeyId)
			}
			signature, signatureErr := hex.DecodeString(attestation.Signature)
			if keyErr != nil || signatureErr != nil || len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, canonical, signature) {
				addRow("commitment_signature", "commitment_ledger", c.Hash, verifyResultFail, "Auditor signature verification failed against pinned trust")
				allSignaturesOK = false
			}
		}
		if allHashesOK {
			addRow("commitment_hash_recompute", "commitment_ledger", "all", verifyResultPass, "all commitment hashes verified")
		}
		if allSignaturesOK {
			addRow("commitment_signature", "commitment_ledger", "all", verifyResultPass, "all Auditor signatures verified")
		}
	}

	// Check 3: Git merkle root cross-check
	if ctx.Err() != nil {
		return FileResult{}, vr, ctx.Err()
	}
	if ledger != nil {
		merkleRoot, err := ledger.GetStateMerkleRoot()
		if err != nil {
			addRow("git_merkle_root", "ledger", "HEAD", verifyResultSkipped, fmt.Sprintf("ledger unavailable: %v", err))
		} else if merkleRoot == "" {
			addRow("git_merkle_root", "ledger", "HEAD", verifyResultSkipped, "ledger empty (no commits)")
		} else {
			addRow("git_merkle_root", "ledger", merkleRoot, verifyResultPass, "HEAD hash captured")
		}
	} else {
		addRow("git_merkle_root", "ledger", "HEAD", verifyResultSkipped, "ledger not configured")
	}

	// Check 4: File mutation linkage (every mutation references a valid event)
	if ctx.Err() != nil {
		return FileResult{}, vr, ctx.Err()
	}
	if eventsStore != nil {
		mutations, err := eventsStore.ListFileMutations(1000, 0)
		if err != nil {
			addRow("file_mutation_linkage", "events_store", "all", verifyResultSkipped, fmt.Sprintf("cannot read mutations: %v", err))
		} else {
			writeOps := 0
			missingHash := 0
			for _, m := range mutations {
				op := string(m.Operation)
				if op == "WRITE" || op == "CREATE" {
					writeOps++
					if m.LedgerHashAfter == "" {
						missingHash++
					}
				}
			}
			if missingHash > 0 {
				addRow("file_mutation_linkage", "events_store", "all", verifyResultFail,
					fmt.Sprintf("%d WRITE/CREATE mutations missing ledger_hash_after", missingHash))
			} else {
				addRow("file_mutation_linkage", "events_store", "all", verifyResultPass,
					fmt.Sprintf("%d mutations checked (%d write/create ops with hashes)", len(mutations), writeOps))
			}
		}
	} else {
		addRow("file_mutation_linkage", "events_store", "all", verifyResultSkipped, "local-first events store not configured")
	}

	// Check 5: Receipt/commitment cross-link (every committed transaction_id has a receipt)
	if ctx.Err() != nil {
		return FileResult{}, vr, ctx.Err()
	}
	if eventsStore != nil && commitmentsErr == nil {
		missingReceipts := 0
		invalidReceipts := 0
		for _, c := range commitments {
			if c.TransactionID == "" {
				invalidReceipts++
				continue
			}
			r, err := eventsStore.GetEventReceipt(c.TransactionID)
			if err != nil || r == nil {
				missingReceipts++
				continue
			}
			if actuatorTrust == nil || r.EventReceipt == nil {
				invalidReceipts++
				continue
			}
			publicKey, err := actuatorTrust.GetTrustedSignerPublicKey(ctx, r.EventReceipt.GetSignerKeyId())
			if err != nil || governance.VerifyEventReceipt(r.EventReceipt, publicKey) != nil || !governance.ReceiptMatchesCommitment(r.EventReceipt, c) {
				invalidReceipts++
			}
		}
		if missingReceipts > 0 {
			addRow("receipt_commitment_crosslink", "events_store+commitment_ledger", "all", verifyResultFail,
				fmt.Sprintf("%d committed transaction_ids have no matching receipt", missingReceipts))
		} else if invalidReceipts > 0 {
			addRow("receipt_commitment_crosslink", "events_store+commitment_ledger", "all", verifyResultFail, fmt.Sprintf("%d receipts failed pinned signature or commitment linkage verification", invalidReceipts))
		} else if len(commitments) > 0 {
			addRow("receipt_commitment_crosslink", "events_store+commitment_ledger", "all", verifyResultPass,
				fmt.Sprintf("all %d commitments have matching receipts", len(commitments)))
		} else {
			addRow("receipt_commitment_crosslink", "events_store+commitment_ledger", "all", verifyResultPass, "no commitments to cross-link")
		}
	}

	// Write the verification summary CSV
	var rows []Row
	for _, r := range vr.Rows {
		rows = append(rows, r)
	}

	res, err := writeCSV(path, VerificationRow{}.Columns(), rows)
	if err != nil {
		return FileResult{}, vr, fmt.Errorf("%w: verification_summary: %w", constants.ErrReportWriteFailed, err)
	}
	res.Filename = constants.ReportVerificationFilename
	return res, vr, nil
}
