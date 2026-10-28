// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package ssh

import "strings"

// QuoteArg quotes one literal argument for a POSIX shell, including embedded
// single quotes. It is also used when displaying commands for a user to paste.
func QuoteArg(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}
