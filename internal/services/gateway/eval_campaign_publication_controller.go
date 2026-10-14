// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
)

// EvalCampaignPublicationController exposes gateway-owned campaign publication
// idempotency state to the owner CLI over mTLS. DELETE discards a run's
// publication: it withdraws the run's dataset from anonymous mirror reads,
// prunes its proof artifacts, and clears its idempotency state.
type EvalCampaignPublicationController struct {
	logger    *slog.Logger
	responder *response.Writer
	service   *EvalCampaignPublicationService
	spectator func() *PublicSpectatorRuntime
}

type EvalCampaignPublicationControllerDeps struct {
	Logger    *slog.Logger
	Responder *response.Writer
	Service   *EvalCampaignPublicationService
	Spectator func() *PublicSpectatorRuntime
}

func newEvalCampaignPublicationController(d EvalCampaignPublicationControllerDeps) *EvalCampaignPublicationController {
	return &EvalCampaignPublicationController{
		logger:    d.Logger,
		responder: d.Responder,
		service:   d.Service,
		spectator: d.Spectator,
	}
}

const evalCampaignPublicationStateSuffix = "/publication-state"

func (c *EvalCampaignPublicationController) handlePublicationState(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, evalCampaignPublicationStateSuffix) {
		c.responder.Error(w, http.StatusNotFound, constants.ErrNotFound.Error())
		return
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, constants.APIPaths.EvalCampaignPublicationStateByRun), evalCampaignPublicationStateSuffix)
	if runID == "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrMissingRequiredField.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		state, err := c.service.Get(r.Context(), runID)
		if err != nil {
			c.responder.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.responder.JSON(w, http.StatusOK, state)
	case http.MethodDelete:
		if err := c.discardPublication(r.Context(), runID); err != nil {
			c.responder.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.responder.JSON(w, http.StatusOK, map[string]string{"run_id": runID, "status": "discarded"})
	case http.MethodPut:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, constants.PublicFeedBatchMaxBytes))
		if err != nil {
			c.responder.Error(w, http.StatusBadRequest, fmt.Errorf("%w: %v", constants.ErrInvalidJSONBody, err).Error())
			return
		}
		state := models.EvalCampaignPublicationState{}
		if err := json.Unmarshal(body, &state); err != nil {
			c.responder.Error(w, http.StatusBadRequest, fmt.Errorf("%w: %v", constants.ErrInvalidJSONBody, err).Error())
			return
		}
		if state.RunID == "" {
			state.RunID = runID
		}
		if state.RunID != runID {
			c.responder.Error(w, http.StatusBadRequest, constants.ErrEvidenceScopeMismatch.Error())
			return
		}
		if err := c.service.Put(r.Context(), state); err != nil {
			c.responder.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.responder.JSON(w, http.StatusOK, state)
	default:
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
	}
}

// discardPublication withdraws one run's dataset from anonymous mirror reads,
// removes its proof artifacts from the proof catalog and pushes the catalog,
// then clears its publication idempotency. The signed batch chain is not
// rewritten. Each step is idempotent, so a failed discard can be retried.
func (c *EvalCampaignPublicationController) discardPublication(ctx context.Context, runID string) error {
	var runtime *PublicSpectatorRuntime
	if c.spectator != nil {
		runtime = c.spectator()
	}
	if mirror := runtime.Mirror(); mirror != nil {
		if err := mirror.WithdrawCampaignDataset(ctx, runID); err != nil {
			return err
		}
	}
	if publisher := runtime.Publisher(); publisher != nil && runtime.Running() {
		removed, err := publisher.PruneProofCatalogForRun(ctx, runID)
		if err != nil {
			return err
		}
		if removed > 0 {
			if _, err := publisher.FlushProofPackage(ctx); err != nil {
				return err
			}
		}
	}
	return c.service.Delete(ctx, runID)
}
