package stratum

import (
	"math/big"

	"go.sia.tech/core/types"
	"go.sia.tech/poold/internal/blake2b"
)

var maxTarget = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// This doesn't appear to work :shrug:
func difficulty(childTarget types.BlockID) uint64 {
	i := new(big.Int).SetBytes(childTarget[:])
	i.Div(maxTarget, i)
	return i.Uint64()
}

func blockMerkleBranches(minerPayouts []types.SiacoinOutput, transactions []types.Transaction) []types.Hash256 {
	h := types.NewHasher()
	acc := new(blake2b.Accumulator)
	for _, mp := range minerPayouts {
		h.Reset()
		h.E.WriteUint8(0x00)
		types.V1SiacoinOutput(mp).EncodeTo(h.E)
		acc.AddLeaf(h.Sum())
	}
	for _, txn := range transactions {
		h.Reset()
		h.E.WriteUint8(0x00)
		txn.EncodeTo(h.E)
		acc.AddLeaf(h.Sum())
	}
	roots := acc.Roots()
	out := make([]types.Hash256, 0, len(roots))
	for _, root := range roots {
		out = append(out, root)
	}
	return out
}
