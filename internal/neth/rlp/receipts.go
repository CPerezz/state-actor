package rlp

// EncodeReceipts returns the RLP encoding of a receipt list.
//
// For state-actor's genesis-only writer, the only call site emits an empty
// receipts list at the genesis row of the receipts DB — Nethermind expects
// an entry to exist at `(0||hash)40bytes` even when the block has no
// transactions, with the value being the RLP empty list 0xc0.
//
// Non-empty receipts are out of scope: state-actor never writes blocks
// past genesis, and full Nethermind receipt RLP (which includes the
// storage-format BlockHash/Number/Index/Sender/Recipient/ContractAddress
// header per `ReceiptStorageDecoder.cs`) is non-trivial. We panic loudly
// if a non-empty list reaches here so a future feature creep doesn't
// silently produce wrong bytes.
func EncodeReceipts(receipts [][]byte) []byte {
	if len(receipts) == 0 {
		return []byte{0xc0}
	}
	panic("EncodeReceipts: non-empty receipts not implemented; genesis-only writer")
}
