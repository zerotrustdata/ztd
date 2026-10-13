// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !linux && !darwin

package config

import "os"

func openEnvFile(path string) (*os.File, error)      { return os.Open(path) }
func validateEnvFileMetadata(info os.FileInfo) error { return nil }
