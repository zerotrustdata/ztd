// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
)

func TestZtdoService_ExecutionTarget(t *testing.T) {
	svc := &ZtdoService{config: &config.Config{OperatorID: "operator-1"}}
	require.True(t, svc.ExecutesFor("operator-1"))
	require.False(t, svc.ExecutesFor("operator-2"))
	require.False(t, svc.ExecutesFor(""))
	require.False(t, (&ZtdoService{}).ExecutesFor("operator-1"))
	require.False(t, (*ZtdoService)(nil).ExecutesFor("operator-1"))
	svc.config.OperatorID = ""
	require.False(t, svc.ExecutesFor(""), "an unenrolled runtime owns no execution target")
}

func TestZtdoService_RuntimeIdentity(t *testing.T) {
	ctx := context.Background()
	svc := &ZtdoService{config: &config.Config{OperatorID: "operator-1", OperatorSessionId: "session-1"}}
	identity, err := svc.RuntimeIdentity(ctx)
	require.NoError(t, err)
	require.Equal(t, govtypes.RuntimeIdentity{OperatorID: "operator-1", SessionID: "session-1"}, identity)

	svc.config.OperatorSessionId = ""
	_, err = svc.RuntimeIdentity(ctx)
	require.ErrorIs(t, err, constants.ErrGatewayOperatorSessionIDRequired, "a runtime with no session owns no audit identity")

	svc.config.OperatorID = ""
	_, err = svc.RuntimeIdentity(ctx)
	require.ErrorIs(t, err, constants.ErrOperatorIDMissing)

	_, err = (*ZtdoService)(nil).RuntimeIdentity(ctx)
	require.ErrorIs(t, err, constants.ErrOperatorIDMissing)
}
