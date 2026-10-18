// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"google.golang.org/protobuf/proto"

	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

func appendUniqueReference(references []*compliancev1.ComplianceEvidenceReference, candidate *compliancev1.ComplianceEvidenceReference) []*compliancev1.ComplianceEvidenceReference {
	if candidate == nil {
		return references
	}
	for _, reference := range references {
		if proto.Equal(reference, candidate) {
			return references
		}
	}
	return append(references, proto.Clone(candidate).(*compliancev1.ComplianceEvidenceReference))
}
