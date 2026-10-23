// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"

	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/execution"
)

// SearchMatch is one matching line returned by search_file and
// search_directory. Content is scrubbed of credential patterns.
type SearchMatch struct {
	Path       string `json:"path"`
	LineNumber int    `json:"line_number"`
	Content    string `json:"content"`
}

type fsGrepServiceCtxKey struct{}

// withFsGrepService returns a context carrying the executing runtime's
// FsGrepService so the search tools resolve paths against its WorkDir.
func withFsGrepService(ctx context.Context, svc *execution.FsGrepService) context.Context {
	return context.WithValue(ctx, fsGrepServiceCtxKey{}, svc)
}

// fsGrepServiceFromContext returns the FsGrepService injected by the
// NativeToolHandler, or nil if none is configured. The search tools fail
// closed when this returns nil.
func fsGrepServiceFromContext(ctx context.Context) *execution.FsGrepService {
	svc, _ := ctx.Value(fsGrepServiceCtxKey{}).(*execution.FsGrepService)
	return svc
}

// scrubbedSearchMatches converts grep matches into tool matches with every
// line scrubbed.
func scrubbedSearchMatches(matches []models.FsGrepMatch) []SearchMatch {
	out := make([]SearchMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, SearchMatch{
			Path:       m.Path,
			LineNumber: m.LineNumber,
			Content:    scrubLine(m.Content),
		})
	}
	return out
}
