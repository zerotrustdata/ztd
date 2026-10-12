// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { deployCommands } from '../features/operators/DeployPanel';
import { credentialPathId } from '../features/account/AccountView';
import { parseFragment } from './fragment';

describe('parseFragment', () => {
  it('reads each deep-link intent', () => {
    expect(parseFragment('#enroll=1&token=abc')).toEqual({ enrollmentToken: 'abc' });
    expect(parseFragment('#recovery=r1')).toEqual({ recoveryToken: 'r1' });
    expect(parseFragment('#approve=tx')).toEqual({ approveTxHash: 'tx' });
    expect(parseFragment('#platform-enrollment=req')).toEqual({ platformEnrollmentId: 'req' });
    expect(parseFragment('#token=abc')).toEqual({});
    expect(parseFragment('')).toEqual({});
  });
});

describe('credentialPathId', () => {
  it('converts standard base64 to the unpadded base64url revoke form', () => {
    expect(credentialPathId('+/8A/w==')).toBe('-_8A_w');
  });
});

describe('deployCommands', () => {
  it('targets the plain-HTTP bootstrap port from the HTTPS console', () => {
    const c = deployCommands({ protocol: 'https:', hostname: 'gw.local', port: '8443', host: 'gw.local:8443' });
    expect(c.quickLinux).toBe('curl -fsSL http://gw.local:8080/ztd-deploy.sh | bash');
    expect(c.binary({ label: 'L', filename: 'ztd-linux-amd64', os: 'linux' })).toBe(
      'curl -fsSL http://gw.local:8080/.well-known/ztd/bin/ztd-linux-amd64 -o ztd && chmod +x ztd && ./ztd operator start -e gw.local',
    );
  });

  it('passes a non-default HTTPS port to the Operator', () => {
    const c = deployCommands({ protocol: 'https:', hostname: 'gw', port: '9443', host: 'gw:9443' });
    expect(c.binary({ label: 'W', filename: 'ztd-windows-amd64.exe', os: 'windows' })).toContain('operator start -e gw -p 9443');
  });
});
