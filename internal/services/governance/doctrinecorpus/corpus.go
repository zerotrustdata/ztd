// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package doctrinecorpus owns the adversarial L1 doctrine corpus. The corpus is
// embedded in the binary and read by the L1 unit tests, the conformance catalogue,
// the coverage report and the persona graders.
package doctrinecorpus

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

//go:embed corpus.json
var corpusJSON []byte

// Category is a corpus entry category.
type Category string

const (
	CategoryDestructiveDelete   Category = "destructive_delete"
	CategoryDiskDestroy         Category = "disk_destroy"
	CategoryExfiltration        Category = "exfiltration"
	CategoryPrivilegeEscalation Category = "privilege_escalation"
	CategoryPersistence         Category = "persistence"
	CategoryBenign              Category = "benign"
)

// Expect is what L1 must do with an entry.
type Expect string

const (
	ExpectBlock Expect = "block"
	ExpectAllow Expect = "allow"
)

// attackCategories is the fixed order of attack categories returned by Categories.
var attackCategories = []Category{
	CategoryDestructiveDelete,
	CategoryDiskDestroy,
	CategoryExfiltration,
	CategoryPrivilegeEscalation,
	CategoryPersistence,
}

type Entry struct {
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Command  string   `json:"command"`
	Expect   Expect   `json:"expect"`
}

type Corpus struct {
	Version string  `json:"version"`
	Subject string  `json:"subject"`
	Policy  string  `json:"policy"`
	Entries []Entry `json:"entries"`
}

// Load decodes and validates the embedded corpus.
func Load() (*Corpus, error) {
	dec := json.NewDecoder(bytes.NewReader(corpusJSON))
	dec.DisallowUnknownFields()
	var corpus Corpus
	if err := dec.Decode(&corpus); err != nil {
		return nil, fmt.Errorf("doctrinecorpus: decode: %w: %w", constants.ErrInvalidJSONBody, err)
	}
	if err := validate(&corpus); err != nil {
		return nil, fmt.Errorf("doctrinecorpus: load: %w", err)
	}
	return &corpus, nil
}

// SHA256 is the hex sha256 of the embedded corpus bytes.
func SHA256() string {
	sum := sha256.Sum256(corpusJSON)
	return hex.EncodeToString(sum[:])
}

// Attacks returns the entries that expect block, in file order.
func (c *Corpus) Attacks() []Entry {
	var attacks []Entry
	for _, e := range c.Entries {
		if e.Expect == ExpectBlock {
			attacks = append(attacks, e)
		}
	}
	return attacks
}

// Categories returns the attack categories in a fixed order (the constants above, benign excluded).
func Categories() []Category {
	return append([]Category(nil), attackCategories...)
}

func isKnownCategory(c Category) bool {
	if c == CategoryBenign {
		return true
	}
	for _, known := range attackCategories {
		if c == known {
			return true
		}
	}
	return false
}

func validate(c *Corpus) error {
	if c.Policy == "" {
		return fmt.Errorf("%w: corpus must document its policy", constants.ErrValidationFailed)
	}
	if len(c.Entries) == 0 {
		return fmt.Errorf("%w: corpus must not be empty", constants.ErrValidationFailed)
	}
	seen := make(map[string]struct{}, len(c.Entries))
	for _, e := range c.Entries {
		if e.ID == "" {
			return fmt.Errorf("%w: entry id is empty", constants.ErrValidationFailed)
		}
		if _, dup := seen[e.ID]; dup {
			return fmt.Errorf("%w: duplicate corpus id %q", constants.ErrValidationFailed, e.ID)
		}
		seen[e.ID] = struct{}{}
		if e.Command == "" {
			return fmt.Errorf("%w: entry %q command is empty", constants.ErrValidationFailed, e.ID)
		}
		if !isKnownCategory(e.Category) {
			return fmt.Errorf("%w: entry %q has unknown category %q", constants.ErrValidationFailed, e.ID, e.Category)
		}
		if e.Expect != ExpectBlock && e.Expect != ExpectAllow {
			return fmt.Errorf("%w: entry %q has unknown expect %q", constants.ErrValidationFailed, e.ID, e.Expect)
		}
		if (e.Category == CategoryBenign) != (e.Expect == ExpectAllow) {
			return fmt.Errorf("%w: entry %q category %q must expect %s iff benign",
				constants.ErrValidationFailed, e.ID, e.Category, ExpectAllow)
		}
	}
	return nil
}
