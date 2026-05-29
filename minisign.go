package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// verifyMinisign verifies a minisign signature (`minisigText`) against `data`,
// using the public key encoded in `pubkeyB64`. Supports both raw Ed25519 (`Ed`)
// and BLAKE2b-512-prehashed Ed25519 (`ED`) — Zig nightly tarballs use the
// prehashed form.
//
// Only the file signature (line 2) is verified; the trusted-comment signature
// (line 4) is not checked.
func verifyMinisign(data, minisigText []byte, pubkeyB64 string) error {
	// Pubkey layout: 2-byte algo "Ed" + 8-byte key_id + 32-byte raw key.
	pkRaw, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil {
		return fmt.Errorf("pubkey base64: %w", err)
	}
	if len(pkRaw) != 42 {
		return fmt.Errorf("pubkey length: got %d, want 42", len(pkRaw))
	}
	if string(pkRaw[0:2]) != "Ed" {
		return fmt.Errorf("unsupported pubkey algo: %q", pkRaw[0:2])
	}
	pkKeyID := pkRaw[2:10]
	pkBytes := ed25519.PublicKey(pkRaw[10:42])

	// Signature is on line 2 (after the untrusted-comment line).
	lines := strings.Split(string(minisigText), "\n")
	if len(lines) < 2 {
		return errors.New("malformed minisig: too few lines")
	}
	sigRaw, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		return fmt.Errorf("sig base64: %w", err)
	}
	if len(sigRaw) != 74 {
		return fmt.Errorf("sig length: got %d, want 74", len(sigRaw))
	}
	sigAlgo := string(sigRaw[0:2])
	sigKeyID := sigRaw[2:10]
	sigBytes := sigRaw[10:74]

	for i := range pkKeyID {
		if pkKeyID[i] != sigKeyID[i] {
			return errors.New("key ID mismatch between pubkey and signature")
		}
	}

	switch sigAlgo {
	case "ED":
		digest := blake2b.Sum512(data)
		if !ed25519.Verify(pkBytes, digest[:], sigBytes) {
			return errors.New("ed25519 prehashed signature invalid")
		}
		return nil
	case "Ed":
		if !ed25519.Verify(pkBytes, data, sigBytes) {
			return errors.New("ed25519 signature invalid")
		}
		return nil
	default:
		return fmt.Errorf("unsupported signature algo: %q", sigAlgo)
	}
}
