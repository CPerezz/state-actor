package autofill

import (
	mrand "math/rand"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/ethereum/state-actor/internal/templates"
)

// codePoolSeed is fixed (not --seed) so every client derives the same pool.
const codePoolSeed = 0x57a7ec0de

// poolCode returns code-pool entry j: a CodeSampler-sized slice of the
// embedded ERC20 runtime rotated by j (real bytecode compresses under LZ4;
// random bytes do not).
//
// ponytail: one real contract sliced at rotating offsets, not a corpus.
// Upgrade to a small corpus if a benchmark shows it over-compresses.
func poolCode(j int, s Sampler) ([]byte, common.Hash) {
	src := templates.ERC20RuntimeBytecode
	code := make([]byte, s.Draw(mrand.New(mrand.NewSource(codePoolSeed+int64(j)))))
	for n := copy(code, src[j%len(src):]); n < len(code); {
		n += copy(code[n:], src)
	}
	return code, crypto.Keccak256Hash(code)
}
