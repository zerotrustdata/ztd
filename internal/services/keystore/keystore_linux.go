// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"errors"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// platformKeyring returns the Linux master key store: an existing
// ssh-agent-sealed key, else the Secret Service, else ssh-agent when the
// Secret Service is unreachable or has no persistent default keyring
// (SSH-only sessions, servers, WSL). Other Secret Service failures fail
// closed because the key may live there.
func platformKeyring(fileSvc fs.RuntimeFileService) (Keyring, error) {
	return selectLinuxKeyring(fileSvc, dialSecretService, dialSSHAgent)
}

func selectLinuxKeyring(fileSvc fs.RuntimeFileService, dialSecrets secretServiceDialer, dialAgent sshAgentDialer) (Keyring, error) {
	sshAgent := newSSHAgentKeyring(fileSvc, dialAgent)
	sealed, err := sshAgent.sealed()
	if err != nil {
		return nil, err
	}
	if sealed {
		return sshAgent, nil
	}
	secretService, ssErr := newSecretServiceKeyring(dialSecrets)
	if ssErr == nil {
		return secretService, nil
	}
	if !errors.Is(ssErr, constants.ErrKeyStoreNoDefaultCollection) && !errors.Is(ssErr, constants.ErrKeyStoreSecretServiceDown) {
		return nil, ssErr
	}
	if err := sshAgent.usable(); err != nil {
		return nil, fmt.Errorf("%w; %w", ssErr, err)
	}
	return sshAgent, nil
}
