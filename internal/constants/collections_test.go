// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectionsRegistry is the shape of protocol/constants/collections.json.
type collectionsRegistry struct {
	Collections map[string]struct {
		GoConst  string `json:"_go_const"`
		Value    string `json:"value"`
		Governed bool   `json:"_governed"`
	} `json:"collections"`
}

// TestCollectionsRegistryMatchesGoConstants asserts collections.json lists
// exactly the CollectionName constants declared in collections.go, by Go name
// and value.
func TestCollectionsRegistryMatchesGoConstants(t *testing.T) {
	data, err := os.ReadFile("../../protocol/constants/collections.json")
	require.NoError(t, err)
	var registry collectionsRegistry
	require.NoError(t, json.Unmarshal(data, &registry))
	fromJSON := map[string]string{}
	for key, entry := range registry.Collections {
		assert.Equalf(t, key, entry.Value, "collections.json key %s differs from its value", key)
		fromJSON[entry.GoConst] = entry.Value
	}

	file, err := parser.ParseFile(token.NewFileSet(), "collections.go", nil, 0)
	require.NoError(t, err)
	fromGo := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if typ, ok := vs.Type.(*ast.Ident); !ok || typ.Name != "CollectionName" {
				continue
			}
			for i, name := range vs.Names {
				lit := vs.Values[i].(*ast.BasicLit)
				value, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				fromGo[name.Name] = value
			}
		}
	}
	require.NotEmpty(t, fromGo)
	assert.Equal(t, fromGo, fromJSON)
}

// TestCollectionIsGovernedDocumentMatchesRegistry keeps the Go governed
// document set in step with the `_governed` flag in collections.json, which
// the L4 Warden enforces for DOCUMENT_UPDATE and DOCUMENT_DELETE.
func TestCollectionIsGovernedDocumentMatchesRegistry(t *testing.T) {
	data, err := os.ReadFile("../../protocol/constants/collections.json")
	require.NoError(t, err)

	var registry collectionsRegistry
	require.NoError(t, json.Unmarshal(data, &registry))
	require.NotEmpty(t, registry.Collections)

	governed := 0
	for key, entry := range registry.Collections {
		if entry.Governed {
			governed++
		}
		assert.Equal(t, entry.Governed, CollectionName(entry.Value).IsGovernedDocument(),
			"collections.json %s (_governed=%v) disagrees with CollectionName.IsGovernedDocument", key, entry.Governed)
	}
	assert.Positive(t, governed, "collections.json marks no collection _governed")
}

// TestCollectionIsGovernedDocumentExcludesPlatformAuthority names the
// collections whose exclusion is the point of the governed document set.
func TestCollectionIsGovernedDocumentExcludesPlatformAuthority(t *testing.T) {
	for _, c := range []CollectionName{
		CollectionUsers,
		CollectionTrustedSigners,
		CollectionAppPolicies,
		CollectionRevokedCertificates,
		CollectionPlatformEnrollments,
		CollectionOperators,
		CollectionOperatorSessions,
		CollectionCLISessions,
		CollectionWebSessions,
		CollectionSettings,
		CollectionConsensus,
		CollectionEnrollmentTokens,
	} {
		assert.False(t, c.IsGovernedDocument(), "%s must not be writable through DOCUMENT_UPDATE or DOCUMENT_DELETE", c)
	}
}
