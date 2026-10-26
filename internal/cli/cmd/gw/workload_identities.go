// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Only explicitly recorded workloads and fixed launcher defaults are inspected.
// This is advisory: gateway cleanup never changes workload directories.
func reportWorkloadIdentities(cmd *cobra.Command) {
	type workload struct {
		Capability constants.OperatorCapability `json:"capability"`
		Directory  string                       `json:"directory"`
	}
	var workloads []workload
	// This launcher registry is project-local (.local.dev/), outside runtime state.
	if data, err := os.ReadFile(constants.LocalDevFullWorkloadsRegistryPath); err == nil {
		if err := json.Unmarshal(data, &workloads); err != nil {
			cmd.PrintErrln("Could not read local workload registry; workload identities may require reset after gateway cleanup.")
		}
	}
	home, _ := os.UserHomeDir()
	for _, capability := range []constants.OperatorCapability{constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver, constants.OperatorCapabilityInference, constants.OperatorCapabilityData} {
		workloads = append(workloads, workload{capability, filepath.Join(home, constants.OllamaZTDWorkloadsDirPath, string(capability))})
	}
	seen := map[string]bool{}
	printed := false
	for _, workload := range workloads {
		if !filepath.IsAbs(workload.Directory) || seen[workload.Directory] {
			continue
		}
		if (constants.OperatorCapabilities{workload.Capability}).Validate() != nil {
			continue
		}
		seen[workload.Directory] = true
		if _, err := os.Stat(filepath.Join(workload.Directory, constants.RuntimeDirname, constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)); err != nil {
			continue
		}
		if !printed {
			cmd.Println("Local workload identities will require reset and fresh enrollment after the gateway starts over:")
			printed = true
		}
		cmd.Printf("  %s: %s\n", workload.Capability, workload.Directory)
	}
	if printed {
		cmd.Println("Recover each workload, then re-enroll it (working data is preserved). Run:")
		cmd.Println("ztd operator reset-identity --working-dir <dir>")
	}
}
