package templates

import (
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/spec"
)

func init() {
	Register(&eoaTemplate{})
}

// eoaTemplate handles `kind: eoa`. EOAs in this schema are richer than
// "address + balance": they may carry EIP-7702 delegation code (23-byte
// 0xef0100<addr> marker) AND custom storage (the "EOA storage bloat via
// delegation" use case from Story 2). The unified PreAllocEntity makes
// these three knobs symmetric with `kind: contract` — only the validator
// rules differ (EOAs reject template + parameters).
type eoaTemplate struct{}

func (eoaTemplate) Name() string { return "eoa" }

// UserVisible reports false: `eoa` is dispatched from `kind: eoa`, never
// from a user-supplied `template:` value.
func (eoaTemplate) UserVisible() bool { return false }

func (eoaTemplate) ValidateParameters(params map[string]any) error {
	// EOAs do not accept template parameters. The spec validator already
	// rejects this, so this branch is only reached if Expand is called
	// programmatically without going through Validate.
	if len(params) > 0 {
		return errEOAParameters
	}
	return nil
}

func (eoaTemplate) Expand(ctx Context, e spec.Entity) ([]PreAllocEntity, error) {
	balance := uint256.NewInt(0)
	if e.Balance != nil {
		balance = e.Balance.V
	}

	acc := &types.StateAccount{
		Nonce:    e.Nonce,
		Balance:  balance,
		Root:     types.EmptyRootHash,
		CodeHash: types.EmptyCodeHash[:],
	}

	if len(e.Code) > 0 {
		// Most often a 7702 0xef0100<addr> delegation marker (23 bytes),
		// but the spec accepts arbitrary code on EOAs — the EVM treats
		// only the 0xef01 prefix specially.
		acc.CodeHash = crypto.Keccak256Hash(e.Code).Bytes()
	}

	pe := PreAllocEntity{
		Address: ctx.ResolvedAddress,
		Account: acc,
		Code:    e.Code, // nil for plain EOAs; bytes for 7702 delegators
	}

	if e.ApproximateSizeBytes > 0 {
		slotCount := ctx.Sizer.SlotsForBytes(ctx.ClientName, e.ApproximateSizeBytes)
		if slotCount > 0 {
			pe.Storage = SynthesizeSlots(ctx.Seed, ctx.ResolvedAddress, "eoa", slotCount)
		}
	}

	return []PreAllocEntity{pe}, nil
}

// errEOAParameters is returned by ValidateParameters for EOAs with non-empty
// params. The string matches spec.Validate's error wording so users see one
// consistent message regardless of which layer caught the rule.
var errEOAParameters = newEOAParamErr()

func newEOAParamErr() error {
	return errString("eoa template does not accept parameters")
}

// errString is a tiny error type used to avoid pulling in `errors.New` from
// every template file. Equivalent to errors.New(s).
type errString string

func (e errString) Error() string { return string(e) }
