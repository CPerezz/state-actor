package geth

import (
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
)

// Compile-time assertion that *Writer satisfies the generator.Writer interface.
// If this line fails to compile, geth.Writer is missing a method or has a
// signature mismatch with the interface.
var _ generator.Writer = (*Writer)(nil)

func TestWriterBasic(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "geth-writer-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	w, err := NewWriter(tmpDir, 1000, 4)
	if err != nil {
		t.Fatalf("Failed to create geth writer: %v", err)
	}
	defer w.Close()

	addr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	acc := &types.StateAccount{
		Nonce:    42,
		Balance:  uint256.NewInt(1000000000000000000), // 1 ETH
		Root:     types.EmptyRootHash,
		CodeHash: types.EmptyCodeHash.Bytes(),
	}

	if err := w.WriteAccount(addr, crypto.Keccak256Hash(addr[:]), acc, 0); err != nil {
		t.Fatalf("Failed to write account: %v", err)
	}

	slot := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	value := common.HexToHash("0x000000000000000000000000000000000000000000000000000000000000002a")

	if err := w.WriteStorage(addr, crypto.Keccak256Hash(addr[:]), slot, crypto.Keccak256Hash(slot[:]), value); err != nil {
		t.Fatalf("Failed to write storage: %v", err)
	}

	if err := w.Flush(); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	stats := w.Stats()
	if stats.AccountBytes == 0 {
		t.Error("Expected non-zero account bytes")
	}
	if stats.StorageBytes == 0 {
		t.Error("Expected non-zero storage bytes")
	}

	t.Logf("geth writer stats: accounts=%d, storage=%d, code=%d",
		stats.AccountBytes, stats.StorageBytes, stats.CodeBytes)
}
