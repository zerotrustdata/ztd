// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package persona grades each step of an agent AgentTrace deterministically, by generic AgentRole.
//
// Grades are deterministic only: no model, no judge, no network. The tier that a step is graded under comes from the
// step itself (resolved_tier), never from the scenario. The secondary judge never changes a grade.
//
// Mapping from the evaluation plan's persona names to AgentRole, for maintainers only:
// Triage is ROUTER, Tribunal seats are CANDIDATE_GENERATOR and CANDIDATE_VOTER, Marshal is RISK_ANALYST, Auditor is
// REVIEWER, Sage and Dash are RESPONDER (not graded here), Codex is MEMORY_WRITER, Scribe is TITLE_WRITER.
package persona
