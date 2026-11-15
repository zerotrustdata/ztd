// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

// ScrubTokenPrefix opens every reversible scrub token ({{UEI_<owner>_<N>}}).
// The egress scrubber replaces a sensitive value a governed read returns with
// such a token, so a model sees the token, never the value.
const ScrubTokenPrefix = "{{UEI_"
