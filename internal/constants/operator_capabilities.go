// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// operatorCapabilityOrder is the canonical order for identity and display.
var operatorCapabilityOrder = []OperatorCapability{OperatorCapabilityData, OperatorCapabilityInference, OperatorCapabilityProvenance, OperatorCapabilityObserver}

// OperatorCapabilities is the complete set of capabilities enabled in one runtime.
// Capabilities compose independently of the deployment shape, which is OperatorType.
type OperatorCapabilities []OperatorCapability

func (c OperatorCapabilities) Has(capability OperatorCapability) bool {
	return slices.Contains(c, capability)
}

func (c OperatorCapabilities) Validate() error {
	for _, capability := range c {
		if !slices.Contains(operatorCapabilityOrder, capability) {
			return fmt.Errorf("%w: operator capability %q", ErrOperatorCapabilityInvalid, capability)
		}
	}
	return nil
}

// Canonical returns a deduplicated set in stable order for identity and display.
func (c OperatorCapabilities) Canonical() OperatorCapabilities {
	result := OperatorCapabilities{}
	for _, capability := range operatorCapabilityOrder {
		if c.Has(capability) {
			result = append(result, capability)
		}
	}
	return result
}

// WithEmbeddedBaseline returns the canonical set including Data and
// Inference. The embedded Operator is the Gateway's own default Data and
// Inference operator, so its capabilities always include both.
func (c OperatorCapabilities) WithEmbeddedBaseline() OperatorCapabilities {
	return append(append(OperatorCapabilities{}, c...), OperatorCapabilityData, OperatorCapabilityInference).Canonical()
}

func (c OperatorCapabilities) String() string {
	parts := make([]string, 0, len(c))
	for _, capability := range c.Canonical() {
		parts = append(parts, string(capability))
	}
	return strings.Join(parts, ",")
}

// Set implements pflag.Value and rejects unknown capabilities at the CLI boundary.
func (c *OperatorCapabilities) Set(value string) error {
	capabilities := OperatorCapabilities{}
	for _, part := range strings.Split(value, ",") {
		capabilities = append(capabilities, OperatorCapability(strings.TrimSpace(part)))
	}
	if err := capabilities.Validate(); err != nil {
		return err
	}
	*c = append(*c, capabilities...).Canonical()
	return nil
}
func (c OperatorCapabilities) Type() string { return "operator-capabilities" }

func (c *OperatorCapabilities) UnmarshalJSON(data []byte) error {
	var capabilities []OperatorCapability
	if err := json.Unmarshal(data, &capabilities); err != nil {
		return err
	}
	if err := OperatorCapabilities(capabilities).Validate(); err != nil {
		return err
	}
	*c = OperatorCapabilities(capabilities).Canonical()
	return nil
}
