// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package compliance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// OverlayStatus represents the maturity level of a COSAiS overlay entry.
type OverlayStatus string

const (
	OverlayStatusDraft      OverlayStatus = "draft"
	OverlayStatusFinalized  OverlayStatus = "finalized"
	OverlayStatusDeprecated OverlayStatus = "deprecated"
)

// Overlay represents a single COSAiS control overlay entry.
type Overlay struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description,omitempty"`
	UseCase     string        `json:"use_case"`
	ControlRefs []string      `json:"control_refs"`
	Status      OverlayStatus `json:"status"`
}

// OverlayCatalog is the typed collection of COSAiS overlay entries loaded
// from one or more JSON files in a configurable directory.
type OverlayCatalog struct {
	Version  string    `json:"version"`
	Source   string    `json:"source"`
	Overlays []Overlay `json:"overlays"`
}

// Validate checks that the catalog has required fields and no duplicate overlay IDs.
func (c *OverlayCatalog) Validate() error {
	if c.Version == "" {
		return fmt.Errorf("%w: overlay catalog version is empty", constants.ErrOverlayCatalogInvalid)
	}
	if c.Source == "" {
		return fmt.Errorf("%w: overlay catalog source is empty", constants.ErrOverlayCatalogInvalid)
	}
	if len(c.Overlays) == 0 {
		return fmt.Errorf("%w: overlay catalog has no overlays", constants.ErrOverlayCatalogInvalid)
	}

	seen := make(map[string]bool, len(c.Overlays))
	for i := range c.Overlays {
		ov := &c.Overlays[i]
		if ov.ID == "" {
			return fmt.Errorf("%w: overlay at index %d has empty ID", constants.ErrOverlayCatalogInvalid, i)
		}
		if ov.Title == "" {
			return fmt.Errorf("%w: overlay %s has empty title", constants.ErrOverlayCatalogInvalid, ov.ID)
		}
		if ov.UseCase == "" {
			return fmt.Errorf("%w: overlay %s has empty use_case", constants.ErrOverlayCatalogInvalid, ov.ID)
		}
		if ov.Status == "" {
			return fmt.Errorf("%w: overlay %s has empty status", constants.ErrOverlayCatalogInvalid, ov.ID)
		}
		if seen[ov.ID] {
			return fmt.Errorf("%w: duplicate overlay ID: %s", constants.ErrOverlayCatalogInvalid, ov.ID)
		}
		seen[ov.ID] = true
	}

	return nil
}

// FindOverlay returns the overlay with the given ID, or nil if not found.
func (c *OverlayCatalog) FindOverlay(id string) *Overlay {
	for i := range c.Overlays {
		if c.Overlays[i].ID == id {
			return &c.Overlays[i]
		}
	}
	return nil
}

// HasOverlay returns true if the catalog contains an overlay with the given ID.
func (c *OverlayCatalog) HasOverlay(id string) bool {
	return c.FindOverlay(id) != nil
}

// LoadOverlaysFromDir loads all COSAiS overlay JSON files from the given
// directory and merges them into a single OverlayCatalog. Files are read
// in lexical order. If the directory is empty, an empty catalog is returned
// without error. Each file must contain a valid OverlayCatalog JSON.
// Duplicate overlay IDs across files return an error.
func LoadOverlaysFromDir(dir string) (*OverlayCatalog, error) {
	if dir == "" {
		return &OverlayCatalog{}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", constants.ErrOverlayReadFailed, dir, err)
	}

	merged := &OverlayCatalog{}
	seenIDs := make(map[string]bool)
	loadedCount := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), constants.FileExtJSON) {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", constants.ErrOverlayReadFailed, entry.Name(), err)
		}

		var cat OverlayCatalog
		if err := json.Unmarshal(data, &cat); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", constants.ErrOverlayParseFailed, entry.Name(), err)
		}

		if err := cat.Validate(); err != nil {
			return nil, fmt.Errorf("compliance: validate overlay file %s: %w", entry.Name(), err)
		}

		if merged.Version == "" {
			merged.Version = cat.Version
			merged.Source = cat.Source
		}

		for _, ov := range cat.Overlays {
			if seenIDs[ov.ID] {
				return nil, fmt.Errorf("compliance: duplicate overlay ID %s across files in %s", ov.ID, dir)
			}
			seenIDs[ov.ID] = true
			merged.Overlays = append(merged.Overlays, ov)
			loadedCount++
		}
	}

	if loadedCount == 0 {
		return &OverlayCatalog{}, nil
	}

	return merged, nil
}

// ValidateOverlayRefs checks that every overlay ID referenced by the KSI
// catalog exists in the overlay catalog. Returns a slice of dangling
// overlay IDs (KSIs referencing overlays not in the catalog). If the slice
// is empty, all references resolve.
func ValidateOverlayRefs(ksiCatalog *KSICatalog, overlayCatalog *OverlayCatalog) []string {
	var dangling []string
	for _, ksi := range ksiCatalog.KSIs {
		for _, ref := range ksi.OverlayRefs {
			if !overlayCatalog.HasOverlay(ref) {
				dangling = append(dangling, fmt.Sprintf("%s -> %s", ksi.ID, ref))
			}
		}
	}
	return dangling
}
