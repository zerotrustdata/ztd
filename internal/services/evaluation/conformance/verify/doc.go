// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package verify recomputes every verdict of a persisted governance conformance
// run from the run record alone. It opens no network, no store and no Gateway:
// the only trust it extends is to the actuator anchors pinned in the record and
// to the catalogue and doctrine corpus compiled into the binary. Any change to a
// persisted byte turns the suite verdict into INVALID_EVIDENCE.
package verify
