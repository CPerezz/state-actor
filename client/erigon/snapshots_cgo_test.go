//go:build cgo_erigon

package erigon

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/internal/erigon/snap"
)

func TestWriteSnapshotsEmitsAllValueDomains(t *testing.T) {
	dir := t.TempDir()

	alloc := map[common.Address]*allocAccount{
		common.HexToAddress("0x000000000000000000000000000000000000ba51"): {
			Balance: big.NewInt(0),
			Code:    []byte{0x60, 0x80, 0x60, 0x40},
		},
		common.HexToAddress("0x000000000000000000000000000000000000b0f00"): {
			Balance: new(big.Int).Mul(big.NewInt(15), big.NewInt(1e18)),
			Nonce:   7,
			Code:    bytesEf01("0xef010022222222222222222222222222222222222222"),
		},
		common.HexToAddress("0x0000000000000000000000000000000000c0ffee"): {
			Balance: big.NewInt(0),
			Nonce:   7,
			Code:    []byte{0x60, 0x80, 0x60, 0x40, 0x52},
			Storage: map[common.Hash]common.Hash{
				common.BigToHash(big.NewInt(0)): common.BigToHash(big.NewInt(100)),
				common.BigToHash(big.NewInt(1)): common.BigToHash(big.NewInt(200)),
			},
		},
	}

	if err := writeSnapshots(context.Background(), dir, 42, alloc); err != nil {
		t.Fatalf("writeSnapshots: %v", err)
	}

	domain := snap.DomainDir(dir)
	r := snap.StepRange{From: 0, To: 256}
	mustExist := []string{
		snap.BuildDataFilename(domain, "v1.0", snap.DomainAccounts, r),
		snap.BuildBTreeFilename(domain, "v1.0", snap.DomainAccounts, r),
		snap.BuildExistenceFilename(domain, "v1.0", snap.DomainAccounts, r),
		snap.BuildDataFilename(domain, "v1.0", snap.DomainStorage, r),
		snap.BuildBTreeFilename(domain, "v1.0", snap.DomainStorage, r),
		snap.BuildExistenceFilename(domain, "v1.0", snap.DomainStorage, r),
		snap.BuildDataFilename(domain, "v1.0", snap.DomainCode, r),
		snap.BuildBTreeFilename(domain, "v1.0", snap.DomainCode, r),
		snap.BuildExistenceFilename(domain, "v1.0", snap.DomainCode, r),
		filepath.Join(snap.SnapshotsDir(dir), "salt-state.txt"),
		filepath.Join(snap.SnapshotsDir(dir), "erigondb.toml"),
	}
	for _, p := range mustExist {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing file %s: %v", p, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("file %s is empty", p)
		}
	}

	// Determinism: same seed + same alloc → byte-identical kv data.
	dir2 := t.TempDir()
	if err := writeSnapshots(context.Background(), dir2, 42, alloc); err != nil {
		t.Fatalf("writeSnapshots (2nd run): %v", err)
	}
	for _, ext := range []string{"v1.0-accounts.0-256.kv", "v1.0-storage.0-256.kv", "v1.0-code.0-256.kv"} {
		a, err := os.ReadFile(filepath.Join(snap.DomainDir(dir), ext))
		if err != nil {
			t.Fatalf("read %s: %v", ext, err)
		}
		b, err := os.ReadFile(filepath.Join(snap.DomainDir(dir2), ext))
		if err != nil {
			t.Fatalf("read %s (2nd): %v", ext, err)
		}
		if len(a) != len(b) {
			t.Errorf("%s length mismatch: %d vs %d", ext, len(a), len(b))
		}
	}
}

func bytesEf01(hex string) []byte {
	if len(hex) >= 2 && hex[0] == '0' && hex[1] == 'x' {
		hex = hex[2:]
	}
	out := make([]byte, len(hex)/2)
	for i := 0; i < len(out); i++ {
		hi := hexNibble(hex[2*i])
		lo := hexNibble(hex[2*i+1])
		out[i] = byte(hi<<4 | lo)
	}
	return out
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c - 'a' + 10)
	case c >= 'A' && c <= 'F':
		return int(c - 'A' + 10)
	}
	return 0
}
