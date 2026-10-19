// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package consensustest builds a real K-of-N Ed25519 L2 consensus set for
// tests: independently keyed members registered as trusted signers, a
// consensus policy over them, and a bound HTTPS protobuf deliberation
// application that signs `<tx_hash>|<decision>` once per signing member. It
// imports no Gateway package so Gateway-internal and fixture tests share it.
package consensustest

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	govsvc "github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// SignerRegistrar registers a consensus member's public key as a trusted signer.
type SignerRegistrar interface {
	AddTrustedSigner(ctx context.Context, signer models.TrustedSigner) error
}

// PolicyRegistrar stores consensus policies and the bound consensus application.
type PolicyRegistrar interface {
	AddConsensus(ctx context.Context, policy models.ConsensusPolicy) error
}

// Member is one consensus member holding an Ed25519 key.
type Member struct {
	AppID      string
	PrivateKey ed25519.PrivateKey
}

// Setup is the result of seeding a consensus set into a Gateway's stores.
type Setup struct {
	ConsensusID string
	Members     []Member
	Deliberator *Deliberator
	Server      *httptest.Server
}

// Deliberator signs an envelope with every signing member. It implements
// mcp.L2ConsensusDeliberator.
type Deliberator struct {
	ConsensusID string
	Members     []Member
	Doctrine    *govsvc.L1Doctrine
}

// Deliberate implements mcp.L2ConsensusDeliberator.
func (d *Deliberator) Deliberate(_ context.Context, envelopeBytes []byte) ([]byte, error) {
	var env commonv1.GovernanceEnvelope
	isJSON := false
	if err := proto.Unmarshal(envelopeBytes, &env); err != nil {
		if jsonErr := protojson.Unmarshal(envelopeBytes, &env); jsonErr != nil {
			return nil, fmt.Errorf("test consensus deliberator: unmarshal envelope: %w", err)
		}
		isJSON = true
	}

	var cmdData string
	if env.IntentData != nil && len(env.IntentData.AsMap()) > 0 {
		jsonBytes, err := json.Marshal(env.IntentData.AsMap())
		if err == nil {
			cmdData = string(jsonBytes)
		}
	}
	if cmdData == "" {
		cmdData = string(env.Payload)
	}

	isSafe := true
	if d.Doctrine != nil && cmdData != "" {
		signals := d.Doctrine.AnalyzeCommand(cmdData)
		for _, sig := range signals {
			if sig.BlockRecommended {
				isSafe = false
				break
			}
		}
	}

	var votes []*commonv1.L2Vote
	for _, member := range d.Members {
		if member.PrivateKey == nil {
			continue
		}
		sig := ed25519.Sign(member.PrivateKey, govsvc.L2VotePayload(env.Id, isSafe))
		votes = append(votes, &commonv1.L2Vote{
			SignerKeyId:        member.AppID,
			ConsensusSignature: hex.EncodeToString(sig),
			Decision:           isSafe,
		})
	}
	if len(votes) == 0 {
		return nil, constants.ErrConsensusNoSigningMembers
	}

	if env.Governance == nil {
		env.Governance = &commonv1.GovernanceMetadata{}
	}
	if env.Governance.L2 == nil {
		env.Governance.L2 = &commonv1.L2Metadata{}
	}
	env.Governance.L2.ConsensusSetId = d.ConsensusID
	env.Governance.L2.Votes = votes

	if isJSON {
		return protojson.Marshal(&env)
	}
	return proto.Marshal(&env)
}

// SetupConsensus seeds nMembers trusted signers and a consensus policy with
// the given quorum, then binds a TLS deliberation application whose first
// nServiceMembers members sign. It returns the Setup carrying the
// Deliberator.
func SetupConsensus(t *testing.T, policies PolicyRegistrar, signers SignerRegistrar, consensusID string, nMembers, quorum, nServiceMembers int) *Setup {
	t.Helper()

	memberAppIDs := make([]string, nMembers)
	signingMembers := make([]Member, 0, nServiceMembers)

	for i := 0; i < nMembers; i++ {
		appID := fmt.Sprintf("%s-member-%d", consensusID, i)
		memberAppIDs[i] = appID

		pub, priv, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)

		err = signers.AddTrustedSigner(t.Context(), models.TrustedSigner{
			ID:        appID,
			PublicKey: hex.EncodeToString(pub),
			AddedAt:   time.Now().UTC(),
			Enabled:   true,
		})
		require.NoError(t, err)

		if i < nServiceMembers {
			signingMembers = append(signingMembers, Member{AppID: appID, PrivateKey: priv})
		}
	}

	err := policies.AddConsensus(t.Context(), models.ConsensusPolicy{
		ID:              consensusID,
		MemberAppIDs:    memberAppIDs,
		Quorum:          quorum,
		RequireDistinct: true,
		Enabled:         true,
	})
	require.NoError(t, err)

	deliberator := &Deliberator{
		ConsensusID: consensusID,
		Members:     signingMembers,
		Doctrine:    govsvc.NewL1Doctrine(),
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-protobuf" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		wire, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		signed, err := deliberator.Deliberate(r.Context(), wire)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(signed)
	}))
	t.Cleanup(server.Close)
	err = policies.AddConsensus(t.Context(), models.ConsensusPolicy{
		ID: string(constants.DocIDConsensusBinding), MemberAppIDs: []string{memberAppIDs[0]},
		Quorum: 1, RequireDistinct: true, EndpointURL: server.URL, Enabled: true,
	})
	require.NoError(t, err)

	return &Setup{
		ConsensusID: consensusID,
		Members:     signingMembers,
		Deliberator: deliberator,
		Server:      server,
	}
}
