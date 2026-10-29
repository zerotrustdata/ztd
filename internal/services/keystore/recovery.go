// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"io"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

const (
	// RecoveryRoleGateway is the runtime a gateway start creates.
	RecoveryRoleGateway = "gateway"
	// RecoveryRoleOperator is the runtime an operator start creates.
	RecoveryRoleOperator = "operator"
	// RecoveryRoleRuntime is a master key created outside gateway or operator start.
	RecoveryRoleRuntime = "runtime"

	recoveryChecksumInfo    = "ztd-recovery-checksum-v1\x00"
	recoveryFingerprintInfo = "ztd-recovery-fingerprint-v1\x00"
	recoveryKeyBytes        = 32
	recoveryChecksumBytes   = 5
	recoveryEncodedLen      = 60 // base32(32+5) with no padding
)

var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// RecoveryFingerprint is the public label printed with a recovery key so a
// paper copy can be matched later. It is not the key.
func RecoveryFingerprint(key []byte) string {
	mac := make([]byte, len(recoveryFingerprintInfo)+len(key))
	copy(mac, recoveryFingerprintInfo)
	copy(mac[len(recoveryFingerprintInfo):], key)
	sum := sha256.Sum256(mac)
	vault.SecureZero(mac)
	encoded := hex.EncodeToString(sum[:8])
	vault.SecureZero(sum[:])
	return encoded
}

// ConfirmRecoveryKey is the fail-closed gate before a new master key is stored.
// interactive shows key once and requires the operator to type it back.
// acceptUnrecoverable stores the key without showing it. Any other caller
// gets ErrKeyStoreRecoveryRequired and the key is not stored. key is not
// retained, logged, or written anywhere except the terminal when interactive
// is true.
func ConfirmRecoveryKey(out io.Writer, in io.Reader, role, keyringName string, key []byte, interactive, acceptUnrecoverable bool) error {
	if role == "" {
		role = RecoveryRoleRuntime
	}
	if acceptUnrecoverable {
		_, _ = io.WriteString(out, "[ztd] --accept-unrecoverable: no recovery key was saved for this "+role+".\n")
		_, _ = io.WriteString(out, "[ztd] If this OS key store is lost, this "+role+" cannot be restored.\n")
		return nil
	}
	if !interactive {
		_, _ = io.WriteString(out, "[ztd] Refusing to create this "+role+". There is no terminal to save the recovery key.\n")
		_, _ = io.WriteString(out, "[ztd] Run this command from a terminal and retype the key, or pass --accept-unrecoverable to keep the only copy in the OS key store.\n")
		_, _ = io.WriteString(out, "[ztd] Losing that copy means this "+role+" cannot be restored.\n")
		return constants.ErrKeyStoreRecoveryRequired
	}
	grouped, err := encodeRecoveryKey(key)
	if err != nil {
		return err
	}
	defer vault.SecureZero(grouped)
	label := keyringName
	if label == "" {
		label = "OS key store"
	}
	var display []byte
	display = append(display, "[ztd] New master key for this "...)
	display = append(display, role...)
	display = append(display, " ("...)
	display = append(display, label...)
	display = append(display, ").\n\n[ztd] Recovery key, shown once:\n\n    "...)
	display = append(display, grouped...)
	display = append(display, "\n\n[ztd] Fingerprint: "...)
	display = append(display, RecoveryFingerprint(key)...)
	display = append(display, "\n\n[ztd] Write this down and store it offline, outside this machine.\n"...)
	display = append(display, "[ztd] Anyone who has this key can open this "...)
	display = append(display, role...)
	display = append(display, ".\n[ztd] ztd will not show it again, write it to disk, or log it.\n"...)
	display = append(display, "[ztd] Your terminal scrollback has a copy. Clear that after the offline copy is stored.\n"...)
	display = append(display, "[ztd] If you lose this key and the OS key store loses it too, this "...)
	display = append(display, role...)
	display = append(display, " cannot be restored. There is no backdoor.\n"...)
	switch keyringName {
	case "keychain", "secret-service":
		display = append(display, "[ztd] This login's OS key store holds one master key. Every gateway and operator on this login uses it.\n"...)
	case "ssh-agent", "dpapi":
		display = append(display, "[ztd] This key opens only this runtime tree.\n"...)
	}
	display = append(display, "\n[ztd] Type the recovery key to confirm you saved it:\n"...)
	_, _ = out.Write(display)
	vault.SecureZero(display)

	line, err := ReadRecoveryLine(in)
	if err != nil {
		if errors.Is(err, constants.ErrKeyStoreRecoveryDeclined) {
			_, _ = io.WriteString(out, "[ztd] No confirmation. Nothing was stored.\n")
		}
		return err
	}
	defer vault.SecureZero(line)
	typed, err := ParseRecoveryKey(line)
	if err != nil {
		_, _ = io.WriteString(out, "[ztd] That does not match. Nothing was stored. The key above is void. Run the command again for a new key.\n")
		return constants.ErrKeyStoreRecoveryMismatch
	}
	defer vault.SecureZero(typed)
	if subtle.ConstantTimeCompare(typed, key) != 1 {
		_, _ = io.WriteString(out, "[ztd] That does not match. Nothing was stored. The key above is void. Run the command again for a new key.\n")
		return constants.ErrKeyStoreRecoveryMismatch
	}
	_, _ = io.WriteString(out, "[ztd] Recovery key confirmed. It will not be shown again.\n")
	return nil
}

// ReadRecoveryLine reads one confirmation line. The caller must SecureZero it.
// A line longer than the reader buffer is a mismatch, not a stored key.
func ReadRecoveryLine(in io.Reader) ([]byte, error) {
	line, err := bufio.NewReaderSize(in, 512).ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, constants.ErrKeyStoreRecoveryMismatch
	}
	if len(line) == 0 && err != nil {
		return nil, constants.ErrKeyStoreRecoveryDeclined
	}
	return append([]byte(nil), line...), nil
}

// ParseRecoveryKey decodes a recovery key, ignoring spaces and dashes.
// The caller must SecureZero the result. A bad checksum or length is a mismatch.
func ParseRecoveryKey(line []byte) ([]byte, error) {
	norm := normalizeRecovery(line)
	defer vault.SecureZero(norm)
	if len(norm) != recoveryEncodedLen {
		return nil, constants.ErrKeyStoreRecoveryMismatch
	}
	raw := make([]byte, recoveryEncoding.DecodedLen(len(norm)))
	n, err := recoveryEncoding.Decode(raw, norm)
	if err != nil || n != recoveryKeyBytes+recoveryChecksumBytes {
		vault.SecureZero(raw)
		return nil, constants.ErrKeyStoreRecoveryMismatch
	}
	raw = raw[:n]
	defer vault.SecureZero(raw)
	key := make([]byte, recoveryKeyBytes)
	copy(key, raw[:recoveryKeyBytes])
	want := recoveryChecksum(key)
	if subtle.ConstantTimeCompare(raw[recoveryKeyBytes:], want[:]) != 1 {
		vault.SecureZero(key)
		return nil, constants.ErrKeyStoreRecoveryMismatch
	}
	return key, nil
}

func encodeRecoveryKey(key []byte) ([]byte, error) {
	if len(key) != recoveryKeyBytes {
		return nil, constants.ErrKeyStoreInvalidKeyLength
	}
	payload := make([]byte, recoveryKeyBytes+recoveryChecksumBytes)
	copy(payload, key)
	sum := recoveryChecksum(key)
	copy(payload[recoveryKeyBytes:], sum[:])
	enc := make([]byte, recoveryEncoding.EncodedLen(len(payload)))
	recoveryEncoding.Encode(enc, payload)
	vault.SecureZero(payload)
	grouped := make([]byte, 0, len(enc)+len(enc)/4)
	for i := 0; i < len(enc); i++ {
		if i > 0 && i%4 == 0 {
			grouped = append(grouped, '-')
		}
		grouped = append(grouped, enc[i])
	}
	vault.SecureZero(enc)
	return grouped, nil
}

func recoveryChecksum(key []byte) [recoveryChecksumBytes]byte {
	mac := make([]byte, len(recoveryChecksumInfo)+len(key))
	copy(mac, recoveryChecksumInfo)
	copy(mac[len(recoveryChecksumInfo):], key)
	sum := sha256.Sum256(mac)
	vault.SecureZero(mac)
	var out [recoveryChecksumBytes]byte
	copy(out[:], sum[:recoveryChecksumBytes])
	vault.SecureZero(sum[:])
	return out
}

func normalizeRecovery(line []byte) []byte {
	out := make([]byte, 0, len(line))
	for _, c := range line {
		switch c {
		case '-', ' ', '\t', '\r', '\n':
			continue
		default:
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
			}
			out = append(out, c)
		}
	}
	return out
}
