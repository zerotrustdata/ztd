// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// ExportActuatorPublicKey writes the Actuator's public key as JSON in the PKI
// directory, where the conformance oracle pins it to verify signed event receipts.
func ExportActuatorPublicKey(fileSvc fs.RuntimeFileService, pubKey ed25519.PublicKey, keyID string, logger *slog.Logger) error {
	if err := fileSvc.MkdirAll(context.Background(), constants.PkiDirname, constants.PermDirPrivate); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrDirCreateFailed, err)
	}

	jsonRelPath := filepath.Join(constants.PkiDirname, constants.ActuatorPubJSONFilename)
	jsonData := models.ActuatorPublicKeyExport{
		KeyID:     keyID,
		PublicKey: hex.EncodeToString(pubKey),
		Algorithm: "ed25519",
	}
	jsonBytes, err := json.MarshalIndent(jsonData, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrJSONMarshalFailed, err)
	}
	if err := fileSvc.WriteFile(context.Background(), jsonRelPath, jsonBytes, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrCertSaveFailed, err)
	}
	if logger != nil {
		logger.Info("Actuator public key exported", "path", fileSvc.Resolve(jsonRelPath), "format", "JSON")
	}

	return nil
}
