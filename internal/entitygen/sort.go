package entitygen

import (
	"bytes"
	"sort"

	"github.com/ethereum/go-ethereum/common"
)

// MapToSortedSlots converts an unordered storage map (typical genesis-alloc
// shape) to a slice sorted by Key — the order required by StackTrie consumers.
func MapToSortedSlots(m map[common.Hash]common.Hash) []StorageSlot {
	slots := make([]StorageSlot, 0, len(m))
	for k, v := range m {
		slots = append(slots, StorageSlot{Key: k, Value: v})
	}
	sort.Slice(slots, func(i, j int) bool {
		return bytes.Compare(slots[i].Key[:], slots[j].Key[:]) < 0
	})
	return slots
}
