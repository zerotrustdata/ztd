// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package authcmd

import (
	"context"
	"errors"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// MockClientFactory returns an API client factory that always yields client.
func MockClientFactory(client APIClient) APIClientFactory {
	return func(fs.RuntimeFileService, *config.Config) (APIClient, error) {
		return client, nil
	}
}

// FailingClientFactory returns an API client factory that always returns err.
func FailingClientFactory(err error) APIClientFactory {
	return func(fs.RuntimeFileService, *config.Config) (APIClient, error) {
		return nil, err
	}
}

// PanickingClientFactory returns an API client factory that reports an error if called.
func PanickingClientFactory() APIClientFactory {
	return func(fs.RuntimeFileService, *config.Config) (APIClient, error) {
		return nil, errors.New("clientFactory should not be called when fileSvcFactory fails")
	}
}

// PanickingEnrollerFactory returns an enroller factory whose enroller reports an error if called.
func PanickingEnrollerFactory() EnrollerFactory {
	return func(_ auth.OutputFunc, _ fs.RuntimeFileService, _ *config.Config) (Enroller, error) {
		return panickingEnroller{}, nil
	}
}

type panickingEnroller struct{}

func (panickingEnroller) Enroll(context.Context, auth.EnrollmentOptions) (*auth.EnrollmentResult, error) {
	return nil, errors.New("enrollerFactory should not be called on this code path")
}
