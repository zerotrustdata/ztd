// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcapability

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func ValidateServedModelTag(modelTag string) error {
	if modelTag == "" || strings.HasPrefix(modelTag, "-") {
		return fmt.Errorf("%w: %q", constants.ErrInferenceModelTagInvalid, modelTag)
	}
	for _, character := range modelTag {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("._:/-", character) {
			continue
		}
		return fmt.Errorf("%w: %q", constants.ErrInferenceModelTagInvalid, modelTag)
	}
	return nil
}

// ValidateWitnessCommand rejects generic command execution on read-only witness
// operators. Their narrowly scoped observation handlers remain available.
func ValidateWitnessCommand(capabilities constants.OperatorCapabilities, command string) error {
	if runsGenericWork(capabilities) {
		return nil
	}
	return fmt.Errorf("%w: command %q", constants.ErrWitnessCommandNotCapable, strings.TrimSpace(command))
}
