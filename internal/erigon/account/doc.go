// Package account is a pure-Go port of Erigon's Account.EncodeForStorage
// codec — the binary format Erigon uses for value bytes in the Accounts
// domain (`.kv` snapshot files + `TblAccountVals` in MDBX).
//
// # Spec source
//
// Mirrors `execution/types/accounts/account.go` from Erigon v3.4.2 (see
// `internal/erigon/constants.go PinnedErigonCommit`). The lines cited in
// this package's comments refer to
// `/Users/random_anon/dev/clients/erigon/execution/types/accounts/account.go`.
//
// # Wire format
//
// EncodeForStorage produces a variable-length binary record:
//
//	byte 0:        fieldSet bitmask
//	               bit 0: nonce present
//	               bit 1: balance present
//	               bit 2: incarnation present
//	               bit 3: codeHash present (== EmptyCodeHash means absent)
//	per present field:
//	  byte:        N (length in bytes of the field value)
//	  bytes 1..N:  field value, big-endian
//
// Encoding is variable-length to keep account records small on disk:
// a fresh EOA with nonce=0, balance=0, no code is just `[0x00]` (1 byte).
// A 1-wei EOA is `[0x02, 0x01, 0x01]` (3 bytes — fieldSet=2 for balance,
// 1-byte length, 0x01 value).
//
// # Differences from go-ethereum's StateAccount
//
// NOT an RLP encoding. NOT the same bytes geth/reth use for MPT
// leaves. The MPT-leaf form (used by state-actor's HashBuilder when
// computing the cross-client state root) is the standard
// `rlp.EncodeToBytes(StateAccount{Nonce, Balance, Root, CodeHash})`. Erigon
// stores domain values in this fieldset format and recomputes the MPT
// root separately via its HexPatriciaHashed commitment tree.
//
// The orchestrator (`client/erigon/run_cgo.go`, Phase C of the plan) will
// emit TWO sorted streams of accounts: one with EncodeForStorage values
// for the snapshot writer, one with RLP for the MPT root computation.
// See § Critical Verifier Corrections > Correction 4 in
// `/Users/random_anon/.claude/plans/so-i-have-a-declarative-owl.md`.
package account
