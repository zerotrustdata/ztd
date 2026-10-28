// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Catalog entries signed before signing_public_key existed append this flag.
// The verifier reads the key from the segment log, so the displayed command
// omits the placeholder.
const legacyPinnedKeyFlag = ' --public-key <trusted-ed25519-key-hex>';

export function runnableVerifyCommand(command: string): string {
  return command.endsWith(legacyPinnedKeyFlag) ? command.slice(0, -legacyPinnedKeyFlag.length) : command;
}
