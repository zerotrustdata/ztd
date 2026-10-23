// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func runFormationsCmd(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	command := evalCmdWithConfig(testDeps(root))
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(append(append([]string{"formations"}, args...), "--project-root", root))
	err := command.Execute()
	return out.String(), err
}

func overlayFormation(t *testing.T, root, id string) evaluation.Formation {
	t.Helper()
	overlay, err := evaluation.LoadFormationCatalogOverlayFile(filepath.Join(root, evaluation.DefaultFormationCatalogOverlayRelPath))
	require.NoError(t, err)
	for _, formation := range overlay.Formations {
		if formation.ID == id {
			return formation
		}
	}
	require.Failf(t, "formation not in overlay", "id %q", id)
	return evaluation.Formation{}
}

func TestFormationsAdd_ResolvesRolesFromServedTags(t *testing.T) {
	t.Parallel()

	root := testutil.TempDir(t)
	seedModelScopes(t, root)

	out, err := runFormationsCmd(t, root, "add", "tags-only",
		"--max-vram", "40gb",
		"--primary-tag", "qwen3:35b",
		"--assistant-tag", "granite4.2:8b",
		"--lite-tag", "gemma3:1b")
	require.NoError(t, err)
	assert.Contains(t, out, "Added formation tags-only: qwen3:35b / granite4.2:8b / gemma3:1b")

	formation := overlayFormation(t, root, "tags-only")
	assert.Equal(t, "tags-only", formation.DisplayName)
	assert.Equal(t, uint64(40*1024), formation.MaxVRAMMiB)
	assert.Equal(t, "qwen3", formation.Primary.Family)
	assert.Equal(t, "qwen3", formation.Primary.Provider)
	assert.Equal(t, "Q4_K_M", formation.Primary.Quantization)
	assert.Equal(t, digestThree, formation.Primary.ModelDigest, "catalog-only tag resolves from the catalog")
	assert.Equal(t, digestDrift, formation.Assistant.ModelDigest, "registry entry wins over the catalog")
	assert.Equal(t, evaluation.EstimateFormationModelVRAMMiB(8_000_000_000, "Q4_K_M"), formation.Assistant.EstimatedModelVRAMMiB)
	assert.Equal(t, evaluation.EstimateFormationKVCacheMiB(1_000_000_000), formation.Lite.EstimatedKVCacheMiB)
}

func TestFormationsAdd_RejectsBudgetOverflowWithoutWritingOverlay(t *testing.T) {
	t.Parallel()

	root := testutil.TempDir(t)
	seedModelScopes(t, root)

	_, err := runFormationsCmd(t, root, "add", "too-big",
		"--max-vram", "8",
		"--primary-tag", "qwen3:35b",
		"--assistant-tag", "granite4.2:8b",
		"--lite-tag", "gemma3:1b")
	require.ErrorIs(t, err, constants.ErrFormationVRAMBudgetExceeded)
	assert.Contains(t, err.Error(), "primary qwen3:35b")
	_, statErr := os.Stat(filepath.Join(root, evaluation.DefaultFormationCatalogOverlayRelPath))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestFormationsAdd_RejectsUnparseableVRAMBudget(t *testing.T) {
	t.Parallel()

	root := testutil.TempDir(t)
	seedModelScopes(t, root)

	_, err := runFormationsCmd(t, root, "add", "bad-budget",
		"--max-vram", "8gbx",
		"--primary-tag", "qwen3:35b",
		"--assistant-tag", "granite4.2:8b",
		"--lite-tag", "gemma3:1b")
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestFormationsAdd_UnresolvedTagNamesMissingFieldsUntilSupplied(t *testing.T) {
	t.Parallel()

	root := testutil.TempDir(t)
	seedModelScopes(t, root)
	args := []string{"add", "custom-lite",
		"--primary-tag", "granite4.2:8b",
		"--assistant-tag", "gemma3:1b",
		"--lite-tag", "custom:700m"}

	_, err := runFormationsCmd(t, root, args...)
	require.ErrorIs(t, err, constants.ErrFormationInvalid)
	assert.Contains(t, err.Error(), "missing family, provider")
	assert.Contains(t, err.Error(), "not in the model registry or catalog: custom:700m")

	_, err = runFormationsCmd(t, root, append(args,
		"--lite-family", "custom",
		"--lite-quant", "Q4_K_M",
		"--lite-parameters", "700m")...)
	require.NoError(t, err)
	formation := overlayFormation(t, root, "custom-lite")
	assert.Equal(t, "custom", formation.Lite.Provider)
	assert.Equal(t, evaluation.EstimateFormationModelVRAMMiB(700_000_000, "Q4_K_M"), formation.Lite.EstimatedModelVRAMMiB)
}

func TestFormationsAdd_RequiresEveryRoleTag(t *testing.T) {
	t.Parallel()

	root := testutil.TempDir(t)
	seedModelScopes(t, root)

	_, err := runFormationsCmd(t, root, "add", "no-lite",
		"--primary-tag", "granite4.2:8b",
		"--assistant-tag", "gemma3:1b")
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	assert.Contains(t, err.Error(), "--lite-tag is required")
}
