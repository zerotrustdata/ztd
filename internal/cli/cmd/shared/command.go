// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package shared

import (
	"context"

	"github.com/spf13/cobra"
)

// CommandContext returns the command context, or context.Background when the
// command has none. Public-feed and evaluation commands share this helper.
func CommandContext(cmd *cobra.Command) context.Context {
	if cmd.Context() != nil {
		return cmd.Context()
	}
	return context.Background()
}

// annotationNoLaunchProfile marks a command subtree that runs with no Gateway
// and therefore must not read the Gateway launch profile or PID file from the
// working directory before its own flag checks run.
const annotationNoLaunchProfile = "ztd/no-launch-profile"

// DeclareNoLaunchProfile marks cmd and every subcommand under it as running
// with no Gateway. The root pre-run consults NeedsLaunchProfile before it
// reads any runtime file.
func DeclareNoLaunchProfile(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string, 1)
	}
	cmd.Annotations[annotationNoLaunchProfile] = "true"
	return cmd
}

// NeedsLaunchProfile reports whether cmd, or the nearest command in its
// ancestry that declares otherwise, reads the Gateway launch profile.
func NeedsLaunchProfile(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if _, declared := c.Annotations[annotationNoLaunchProfile]; declared {
			return false
		}
	}
	return true
}
