package stratum

import (
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/poold/internal/blake2b"
	"lukechampine.com/frand"
)

// taken from core
func blockMerkleRoot(minerPayouts []types.SiacoinOutput, transactions []types.Transaction) types.Hash256 {
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
	return acc.Root()
}

func TestBlockMerkleBranches(t *testing.T) {
	minerPayouts := make([]types.SiacoinOutput, frand.Intn(100))
	txns := make([]types.Transaction, frand.Intn(100))
	for i := range minerPayouts {
		minerPayouts[i].Address = frand.Entropy256()
		minerPayouts[i].Value = types.NewCurrency64(frand.Uint64n(100))
	}
	for i := range txns {
		txns[i].ArbitraryData = [][]byte{frand.Bytes(32)}
	}

	coinbaseTxn := types.Transaction{
		ArbitraryData: [][]byte{{0x04, 0x05, 0x06}},
	}
	h := types.NewHasher()
	h.E.WriteUint8(0x00)
	coinbaseTxn.EncodeTo(h.E)
	root := h.Sum()

	for _, branch := range blockMerkleBranches(minerPayouts, txns) {
		root = blake2b.SumPair(branch, root)
	}

	expectedRoot := blockMerkleRoot(minerPayouts, append(txns, coinbaseTxn))
	if root != expectedRoot {
		t.Fatalf("expected %x, got %x", expectedRoot, root)
	}
}
