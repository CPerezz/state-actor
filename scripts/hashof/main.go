package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
)

// hashof prints keccak256(addr) for each hex address on the command line.
// Used to identify which address has a given hashed_address.
//
// Usage: go run ./scripts/hashof 0x000F3df6D732807Ef1319fB7B8bB8522d0Beac02 ...
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hashof <0xaddr> [<0xaddr>...]")
		os.Exit(2)
	}
	for _, arg := range os.Args[1:] {
		s := arg
		if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
			s = s[2:]
		}
		b, err := hex.DecodeString(s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad hex %q: %v\n", arg, err)
			continue
		}
		sum := crypto.Keccak256(b)
		fmt.Printf("%s -> %s\n", arg, "0x"+hex.EncodeToString(sum))
	}
}
