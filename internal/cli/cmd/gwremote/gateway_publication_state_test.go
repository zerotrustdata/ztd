// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

type capturingPublicationStateClient struct {
	deletePath string
	deleteBody models.EvalCampaignPublicationDiscardResponse
}

func (c *capturingPublicationStateClient) Get(string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected get")
}

func (c *capturingPublicationStateClient) Post(string, interface{}) ([]byte, error) {
	return nil, fmt.Errorf("unexpected post")
}

func (c *capturingPublicationStateClient) Put(string, interface{}) ([]byte, error) {
	return nil, fmt.Errorf("unexpected put")
}

func (c *capturingPublicationStateClient) Delete(path string) ([]byte, error) {
	c.deletePath = path
	return json.Marshal(c.deleteBody)
}

func TestNewCampaignPublicationStateStore_RequiresHealthyGateway(t *testing.T) {
	WithGatewayHealthCheck(t, false)
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

	_, err := NewCampaignPublicationStateStore(fileSvc, cfg)
	assert.ErrorIs(t, err, constants.ErrGatewayUnhealthy)
}

func TestRemoteGatewayCampaignPublicationStateStore_DeleteDiscardsRunPublication(t *testing.T) {
	runID := "eval-init-nemotron-3-nano-4b-1792241875"
	tests := []struct {
		name    string
		body    models.EvalCampaignPublicationDiscardResponse
		wantErr error
	}{
		{name: "discarded", body: models.EvalCampaignPublicationDiscardResponse{RunID: runID, Discarded: true}},
		{name: "not discarded", body: models.EvalCampaignPublicationDiscardResponse{RunID: runID}, wantErr: constants.ErrEvidenceScopeMismatch},
		{name: "other run", body: models.EvalCampaignPublicationDiscardResponse{RunID: "other-run", Discarded: true}, wantErr: constants.ErrEvidenceScopeMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &capturingPublicationStateClient{deleteBody: test.body}
			store := &remoteGatewayCampaignPublicationStateStore{client: client}

			err := store.Delete(context.Background(), runID)
			assert.Equal(t, constants.EvalCampaignPublicationStatePath(runID), client.deletePath)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
