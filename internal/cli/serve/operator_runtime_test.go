// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestRefuseModelStoreRuntime(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	modelsDir := filepath.Join(tmp, "models")
	nestedRuntime := filepath.Join(modelsDir, constants.RuntimeDirname)
	deepNestedRuntime := filepath.Join(modelsDir, "sub", "worker", constants.RuntimeDirname)
	outsideRuntime := filepath.Join(tmp, "worker", constants.RuntimeDirname)
	siblingRuntime := filepath.Join(tmp, "models-other", constants.RuntimeDirname)

	tests := []struct {
		name             string
		runtimeDir       string
		modelStorageRoot string
		wantErr          bool
	}{
		{
			name:             "empty model storage root is allowed",
			runtimeDir:       outsideRuntime,
			modelStorageRoot: "",
			wantErr:          false,
		},
		{
			name:             "whitespace model storage root is allowed",
			runtimeDir:       outsideRuntime,
			modelStorageRoot: "   ",
			wantErr:          false,
		},
		{
			name:             "empty runtime dir is allowed",
			runtimeDir:       "",
			modelStorageRoot: modelsDir,
			wantErr:          false,
		},
		{
			name:             "runtime dir completely outside model store",
			runtimeDir:       outsideRuntime,
			modelStorageRoot: modelsDir,
			wantErr:          false,
		},
		{
			name:             "sibling directory with prefix collision",
			runtimeDir:       siblingRuntime,
			modelStorageRoot: modelsDir,
			wantErr:          false,
		},
		{
			name:             "runtime dir directly inside model store",
			runtimeDir:       nestedRuntime,
			modelStorageRoot: modelsDir,
			wantErr:          true,
		},
		{
			name:             "runtime dir in subfolder of model store",
			runtimeDir:       deepNestedRuntime,
			modelStorageRoot: modelsDir,
			wantErr:          true,
		},
		{
			name:             "runtime dir identical to model store",
			runtimeDir:       modelsDir,
			modelStorageRoot: modelsDir,
			wantErr:          true,
		},
		{
			name:             "trailing slash in model storage root",
			runtimeDir:       nestedRuntime,
			modelStorageRoot: modelsDir + string(filepath.Separator),
			wantErr:          true,
		},
		{
			name:             "trailing slash in runtime dir",
			runtimeDir:       nestedRuntime + string(filepath.Separator),
			modelStorageRoot: modelsDir,
			wantErr:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := refuseModelStoreRuntime(tt.runtimeDir, tt.modelStorageRoot)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, constants.ErrOperatorModelStoreRuntime)
				assert.Contains(t, err.Error(), "--working-dir")
			} else {
				assert.NoError(t, err)
			}
		})
	}

	if runtime.GOOS == "windows" {
		t.Run("windows case-insensitive containment", func(t *testing.T) {
			t.Parallel()
			root := `C:\ai\ollama\models`
			inside := `c:\AI\ollama\Models\.ztd`
			err := refuseModelStoreRuntime(inside, root)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrOperatorModelStoreRuntime)

			diffDrive := `D:\ai\ollama\models\.ztd`
			err = refuseModelStoreRuntime(diffDrive, root)
			assert.NoError(t, err)
		})
	}
}
