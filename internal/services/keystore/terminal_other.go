// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !linux && !darwin && !windows

package keystore

import "io"

// ReaderIsTerminal is false on platforms without a console check, so a new
// master key is not printed and is not stored unless the caller accepts an
// unrecoverable runtime.
func ReaderIsTerminal(io.Reader) bool { return false }
