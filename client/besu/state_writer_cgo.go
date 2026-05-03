//go:build cgo_besu

package besu

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"math/big"
	mrand "math/rand"
	"os"
	"sort"

	"github.com/cockroachdb/pebble"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/besu"
	besurlp "github.com/nerolation/state-actor/internal/besu/rlp"
	besutrie "github.com/nerolation/state-actor/internal/besu/trie"
)

// phase1FlushBytes is the temp-Pebble batch flush threshold during entity
// streaming. Mirrors nethermind's entitygen_cgo.go:80 — 64 MiB keeps Phase 1
// memory bounded while amortizing per-batch syscall overhead.
const phase1FlushBytes = 64 * 1024 * 1024

// writeStateAndCollectRoot drives the two-phase streaming pipeline:
//
//	Phase 1: generate entities (single-goroutine RNG, math/rand.Rand is
//	         not thread-safe). For each, compute addrHash and serialize
//	         (addrHash → entityBlob) into a temp Pebble DB. Pebble's LSM
//	         auto-sorts by key so Phase 2 can iterate in addrHash-sorted
//	         order.
//
//	Phase 2: iterate the temp Pebble. For each entity:
//	         - If contract: build per-account storage trie via
//	           Builder.BeginStorage; write flat slots + code; embed
//	           storageRoot in the account RLP.
//	         - Write flat ACCOUNT_INFO_STATE.
//	         - Feed addrHash + accountRLP into the account trie builder.
//
// Returns the state root and a Stats summary. NodeSink.SaveWorldState is
// invoked here at the end of Phase 2; the caller (run_cgo.go) does NOT
// need to call it again.
//
// Memory bound: O(max storage slots per single contract). The full account
// set never lives in RAM simultaneously — Phase 1 streams entities through
// Pebble, Phase 2 streams them back out one at a time.
//
// v1 deferred: cross-account parallelism in Phase 2 (per amendment from
// Tier 4 review). v1 is sequential per account.
func writeStateAndCollectRoot(
	ctx context.Context,
	cfg generator.Config,
	db *besuDB,
	sink *nodeSink,
) (common.Hash, []byte, *generator.Stats, error) {
	stats := &generator.Stats{}

	// --- Phase 1: stream entities to temp Pebble. ---

	tmpDir, err := os.MkdirTemp("", "besu-acct-trie-*")
	if err != nil {
		return common.Hash{}, nil, nil, fmt.Errorf("besu: mkdtemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	pdb, err := pebble.Open(tmpDir, &pebble.Options{})
	if err != nil {
		return common.Hash{}, nil, nil, fmt.Errorf("besu: open temp pebble: %w", err)
	}

	rng := mrand.New(mrand.NewSource(int64(cfg.Seed)))
	pendingBytes := 0
	batch := pdb.NewBatch()
	flush := func() error {
		if err := batch.Commit(pebble.Sync); err != nil {
			return err
		}
		batch = pdb.NewBatch()
		pendingBytes = 0
		return nil
	}

	// Phase 1 target-size cap — mirror generator/generator.go:1050-1069.
	// Track cumulative raw bytes (32B addrHash key + entity blob) and stop
	// emission once cfg.TargetSize is reached. This is intentionally an
	// over-estimate of what the final Bonsai DB will hold (raw entity bytes
	// don't include trie node overhead) but on the safer side — we tend to
	// land slightly under target. Without this cap, --target-size=50GB
	// would require user to also pass --accounts/--contracts large enough
	// to hit it; this makes target-size the governing constraint.
	totalRawBytes := uint64(0)
	targetReached := false
	checkTarget := func(blobLen int) bool {
		totalRawBytes += uint64(32 + blobLen)
		if cfg.TargetSize > 0 && totalRawBytes >= cfg.TargetSize {
			if cfg.Verbose {
				log.Printf("besu Phase 1: raw bytes %d MiB >= target %d MiB — stopping entity emission early",
					totalRawBytes>>20, cfg.TargetSize>>20)
			}
			targetReached = true
			return true
		}
		return false
	}

	// Emit count: EOA and contract counts from cfg.
	for i := 0; i < cfg.NumAccounts && !targetReached; i++ {
		if err := ctx.Err(); err != nil {
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}
		addr, nonce, balance := genEOA(rng)
		addrHash := crypto.Keccak256Hash(addr[:])
		blob := encodeEntityEOA(nonce, balance)
		if err := batch.Set(addrHash[:], blob, nil); err != nil {
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}
		pendingBytes += 32 + len(blob)
		if pendingBytes >= phase1FlushBytes {
			if err := flush(); err != nil {
				pdb.Close()
				return common.Hash{}, nil, nil, err
			}
		}
		if checkTarget(len(blob)) {
			break
		}
	}

	// --- Phase 1b: inject any explicitly-requested addresses (e.g. Anvil's
	// default account). Mirrors generator.Generator's InjectAddresses
	// handling at generator/generator.go:925-949: each address gets a fresh
	// EOA with 999_999_999 ETH balance, nonce=0, no code/storage. We append
	// them BEFORE Phase 1 flush so they hit the temp Pebble alongside the
	// synthetic EOAs and naturally sort into the right position.
	injectBalance := new(uint256.Int).Mul(uint256.NewInt(999_999_999), uint256.NewInt(1_000_000_000_000_000_000))
	seenInjected := make(map[common.Address]struct{}, len(cfg.InjectAddresses))
	for _, addr := range cfg.InjectAddresses {
		if err := ctx.Err(); err != nil {
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}
		if _, dup := seenInjected[addr]; dup {
			continue
		}
		seenInjected[addr] = struct{}{}
		addrHash := crypto.Keccak256Hash(addr[:])
		// Inject as an EOA with the canonical large balance.
		blob := encodeEntityEOA(0, injectBalance)
		if err := batch.Set(addrHash[:], blob, nil); err != nil {
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}
		pendingBytes += 32 + len(blob)
		if pendingBytes >= phase1FlushBytes {
			if err := flush(); err != nil {
				pdb.Close()
				return common.Hash{}, nil, nil, err
			}
		}
	}

	for i := 0; i < cfg.NumContracts && !targetReached; i++ {
		if err := ctx.Err(); err != nil {
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}
		addr, nonce, balance, code, slots := genContract(rng, cfg)
		addrHash := crypto.Keccak256Hash(addr[:])
		blob := encodeEntityContract(nonce, balance, code, slots)
		if err := batch.Set(addrHash[:], blob, nil); err != nil {
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}
		pendingBytes += 32 + len(blob)
		if pendingBytes >= phase1FlushBytes {
			if err := flush(); err != nil {
				pdb.Close()
				return common.Hash{}, nil, nil, err
			}
		}
		if checkTarget(len(blob)) {
			break
		}
	}
	if err := flush(); err != nil {
		pdb.Close()
		return common.Hash{}, nil, nil, err
	}
	// Compact merges all SSTs into one sorted run for fastest forward iteration.
	if err := pdb.Compact(nil, []byte{0xff, 0xff, 0xff, 0xff}, false); err != nil {
		pdb.Close()
		return common.Hash{}, nil, nil, err
	}

	// --- Phase 2: iterate sorted, drive Builder + flat-state writes. ---

	builder := besutrie.New(sink)
	iter, err := pdb.NewIter(nil)
	if err != nil {
		pdb.Close()
		return common.Hash{}, nil, nil, fmt.Errorf("besu: temp iter: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		if err := ctx.Err(); err != nil {
			iter.Close()
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}
		var addrHash common.Hash
		copy(addrHash[:], iter.Key())
		entity := decodeEntity(iter.Value())

		// Storage trie + flat slots + code.
		storageRoot := besu.EmptyTrieNodeHash
		codeHash := besu.EmptyCodeHash
		if entity.kind == entityContract {
			if len(entity.slots) > 0 {
				sb := builder.BeginStorage(addrHash)
				// Sort slots by slotHash (storage trie also requires sorted insert).
				type kv struct {
					slotHash common.Hash
					value    common.Hash
				}
				kvs := make([]kv, 0, len(entity.slots))
				for slotKey, slotVal := range entity.slots {
					kvs = append(kvs, kv{
						slotHash: crypto.Keccak256Hash(slotKey[:]),
						value:    slotVal,
					})
				}
				sort.Slice(kvs, func(i, j int) bool {
					return kvs[i].slotHash.Big().Cmp(kvs[j].slotHash.Big()) < 0
				})
				for _, e := range kvs {
					valueRLP := besurlp.EncodeStorageValue(e.value)
					if err := sb.AddSlot(e.slotHash, valueRLP); err != nil {
						iter.Close()
						pdb.Close()
						return common.Hash{}, nil, nil, err
					}
					if err := sink.PutFlatStorage(addrHash, e.slotHash, valueRLP); err != nil {
						iter.Close()
						pdb.Close()
						return common.Hash{}, nil, nil, err
					}
					stats.StorageSlotsCreated++
					stats.StorageBytes += uint64(64 + len(valueRLP))
				}
				root, err := sb.Commit()
				if err != nil {
					iter.Close()
					pdb.Close()
					return common.Hash{}, nil, nil, err
				}
				storageRoot = root
			}
			if len(entity.code) > 0 {
				codeHash = crypto.Keccak256Hash(entity.code)
				if err := sink.PutCode(codeHash, entity.code); err != nil {
					iter.Close()
					pdb.Close()
					return common.Hash{}, nil, nil, err
				}
				stats.CodeBytes += uint64(len(entity.code))
			}
		}

		accountRLP, err := besurlp.EncodeAccount(entity.nonce, entity.balance, storageRoot, codeHash)
		if err != nil {
			iter.Close()
			pdb.Close()
			return common.Hash{}, nil, nil, fmt.Errorf("besu: encode account: %w", err)
		}
		if err := sink.PutFlatAccount(addrHash, accountRLP); err != nil {
			iter.Close()
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}
		if err := builder.AddAccount(addrHash, accountRLP); err != nil {
			iter.Close()
			pdb.Close()
			return common.Hash{}, nil, nil, err
		}

		if entity.kind == entityEOA {
			stats.AccountsCreated++
		} else {
			stats.ContractsCreated++
		}
		stats.AccountBytes += uint64(32 + len(accountRLP))
	}
	if err := iter.Close(); err != nil {
		pdb.Close()
		return common.Hash{}, nil, nil, err
	}
	if err := pdb.Close(); err != nil {
		return common.Hash{}, nil, nil, err
	}

	// Commit the account trie. NodeSink emits remaining trie nodes.
	rootHash, rootRLP, err := builder.Commit()
	if err != nil {
		return common.Hash{}, nil, nil, fmt.Errorf("besu: trie commit: %w", err)
	}

	stats.TotalBytes = stats.AccountBytes + stats.StorageBytes + stats.CodeBytes
	return rootHash, rootRLP, stats, nil
}

// --- Entity types and encoding ---

type entityKind byte

const (
	entityEOA      entityKind = 1
	entityContract entityKind = 2
)

type entity struct {
	kind    entityKind
	nonce   uint64
	balance *uint256.Int
	code    []byte
	slots   map[common.Hash]common.Hash
}

// genEOA produces a synthetic EOA via the rng.
func genEOA(rng *mrand.Rand) (common.Address, uint64, *uint256.Int) {
	var addr common.Address
	rng.Read(addr[:])
	nonce := rng.Uint64() % 1024
	balance := uint256.NewInt(rng.Uint64()).Mul(
		uint256.NewInt(rng.Uint64()),
		uint256.NewInt(1_000_000_000),
	)
	return addr, nonce, balance
}

// genContract produces a synthetic contract with code + storage slots.
//
// Mask 0xEF code prefix to 0x60 (PUSH1) per EIP-3541. Mirror of nethermind
// entitygen_cgo.go behavior.
func genContract(rng *mrand.Rand, cfg generator.Config) (common.Address, uint64, *uint256.Int, []byte, map[common.Hash]common.Hash) {
	var addr common.Address
	rng.Read(addr[:])
	nonce := rng.Uint64() % 1024
	balance := uint256.NewInt(rng.Uint64()).Mul(
		uint256.NewInt(rng.Uint64()),
		uint256.NewInt(1_000_000_000),
	)

	// Random bytecode, length [32, 256].
	codeLen := 32 + rng.Intn(225)
	code := make([]byte, codeLen)
	rng.Read(code)
	// EIP-3541: forbid 0xEF prefix. Mask to PUSH1 (0x60) — same fix nethermind uses.
	if len(code) > 0 && code[0] == 0xEF {
		code[0] = 0x60
	}

	// Slot count between MinSlots..MaxSlots inclusive.
	minSlots := cfg.MinSlots
	if minSlots < 1 {
		minSlots = 1
	}
	maxSlots := cfg.MaxSlots
	if maxSlots < minSlots {
		maxSlots = minSlots
	}
	span := maxSlots - minSlots + 1
	count := minSlots + rng.Intn(span)

	slots := make(map[common.Hash]common.Hash, count)
	for i := 0; i < count; i++ {
		var k, v common.Hash
		rng.Read(k[:])
		rng.Read(v[:])
		slots[k] = v
	}
	return addr, nonce, balance, code, slots
}

// --- Entity blob serialization for temp Pebble ---
//
// Format (single-byte kind tag + fields):
//
//   EOA:
//     [0x01] [nonce u64 BE] [balance_len u8] [balance bytes...]
//
//   Contract:
//     [0x02] [nonce u64 BE] [balance_len u8] [balance bytes...]
//        [code_len u32 BE] [code bytes...]
//        [slot_count u32 BE] [slot_count × ([slot_key 32B] [slot_value 32B])]

func encodeEntityEOA(nonce uint64, balance *uint256.Int) []byte {
	balBytes := balance.ToBig().Bytes() // minimal big-endian
	out := make([]byte, 1+8+1+len(balBytes))
	out[0] = byte(entityEOA)
	binary.BigEndian.PutUint64(out[1:9], nonce)
	out[9] = byte(len(balBytes))
	copy(out[10:], balBytes)
	return out
}

func encodeEntityContract(nonce uint64, balance *uint256.Int, code []byte, slots map[common.Hash]common.Hash) []byte {
	balBytes := balance.ToBig().Bytes()
	size := 1 + 8 + 1 + len(balBytes) + 4 + len(code) + 4 + len(slots)*64
	out := make([]byte, 0, size)
	out = append(out, byte(entityContract))
	var nonceBuf [8]byte
	binary.BigEndian.PutUint64(nonceBuf[:], nonce)
	out = append(out, nonceBuf[:]...)
	out = append(out, byte(len(balBytes)))
	out = append(out, balBytes...)
	var codeLenBuf [4]byte
	binary.BigEndian.PutUint32(codeLenBuf[:], uint32(len(code)))
	out = append(out, codeLenBuf[:]...)
	out = append(out, code...)
	var slotCountBuf [4]byte
	binary.BigEndian.PutUint32(slotCountBuf[:], uint32(len(slots)))
	out = append(out, slotCountBuf[:]...)
	for k, v := range slots {
		out = append(out, k[:]...)
		out = append(out, v[:]...)
	}
	return out
}

func decodeEntity(blob []byte) entity {
	if len(blob) < 1 {
		panic("besu: empty entity blob")
	}
	e := entity{kind: entityKind(blob[0])}
	pos := 1
	e.nonce = binary.BigEndian.Uint64(blob[pos : pos+8])
	pos += 8
	balLen := int(blob[pos])
	pos++
	balBytes := blob[pos : pos+balLen]
	pos += balLen
	e.balance = new(uint256.Int)
	e.balance.SetBytes(balBytes)

	if e.kind == entityContract {
		codeLen := int(binary.BigEndian.Uint32(blob[pos : pos+4]))
		pos += 4
		e.code = make([]byte, codeLen)
		copy(e.code, blob[pos:pos+codeLen])
		pos += codeLen
		slotCount := int(binary.BigEndian.Uint32(blob[pos : pos+4]))
		pos += 4
		e.slots = make(map[common.Hash]common.Hash, slotCount)
		for i := 0; i < slotCount; i++ {
			var k, v common.Hash
			copy(k[:], blob[pos:pos+32])
			pos += 32
			copy(v[:], blob[pos:pos+32])
			pos += 32
			e.slots[k] = v
		}
	}
	return e
}

// suppress unused warning for big import — the field is used via uint256.ToBig.
var _ = (*big.Int)(nil)
