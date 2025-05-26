package stratum

import (
	"bytes"
	"encoding/hex"
	"testing"

	"go.sia.tech/core/blake2b"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/testutil"
	"lukechampine.com/frand"
)

func TestCoinbaseTxn(t *testing.T) {
	const label = "mining pool test"
	extranonce1, extranonce2 := frand.Bytes(4), frand.Bytes(4)

	cb1, cb2 := CoinbaseTxn(label)

	coinbaseTxnStr := cb1 + hex.EncodeToString(extranonce1) + hex.EncodeToString(extranonce2) + cb2

	buf, err := hex.DecodeString(coinbaseTxnStr)
	if err != nil {
		t.Fatalf("failed to decode coinbase: %v", err)
	}

	dec := types.NewBufDecoder(buf)

	var txn types.Transaction
	txn.DecodeFrom(dec)
	if err := dec.Err(); err != nil {
		t.Fatalf("failed to decode coinbase txn: %v", err)
	}

	expectedArbData := append(append(append(nonSiaPrefix[:], []byte(label)...), extranonce1...), extranonce2...)
	if !bytes.Equal(txn.ArbitraryData[0], expectedArbData) {
		t.Fatalf("expected arbitrary data to be %x, got %x", expectedArbData, txn.ArbitraryData[0])
	}
}

func TestV2CoinbaseTxn(t *testing.T) {
	const label = "mining pool test"
	extranonce1, extranonce2 := frand.Bytes(4), frand.Bytes(4)

	cb1, cb2 := V2CoinbaseTxn(label)

	coinbaseTxnStr := cb1 + hex.EncodeToString(extranonce1) + hex.EncodeToString(extranonce2) + cb2
	buf, err := hex.DecodeString(coinbaseTxnStr)
	if err != nil {
		t.Fatalf("failed to decode coinbase: %v", err)
	}

	var txn types.V2Transaction
	dec := types.NewBufDecoder(buf)
	txn.DecodeFrom(dec)
	if err := dec.Err(); err != nil {
		t.Fatalf("failed to decode coinbase txn: %v", err)
	}
	expectedArbData := append(append(append(nonSiaPrefix[:], []byte(label)...), extranonce1...), extranonce2...)
	if !bytes.Equal(txn.ArbitraryData, expectedArbData) {
		t.Fatalf("expected arbitrary data to be %x, got %x", expectedArbData, txn.ArbitraryData)
	}
}

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
	v2txns := make([]types.V2Transaction, frand.Intn(100))
	for i := range minerPayouts {
		minerPayouts[i].Address = frand.Entropy256()
		minerPayouts[i].Value = types.NewCurrency64(frand.Uint64n(100))
	}
	for i := range txns {
		txns[i].ArbitraryData = [][]byte{frand.Bytes(32)}
	}
	for i := range v2txns {
		v2txns[i].ArbitraryData = frand.Bytes(32)
	}

	t.Run("v1", func(t *testing.T) {
		coinbaseTxn := types.Transaction{
			ArbitraryData: [][]byte{{0x04, 0x05, 0x06}},
		}
		h := types.NewHasher()
		h.E.WriteUint8(0x00)
		coinbaseTxn.EncodeTo(h.E)
		root := h.Sum()

		// hardfork not active
		n, _ := testutil.Network()
		n.HardforkV2.AllowHeight = 1
		n.HardforkV2.RequireHeight = 1
		cs := consensus.State{
			Network: n,
		}

		for _, branch := range BlockMerkleBranches(cs, minerPayouts, txns, v2txns) {
			branchBuf, _ := hex.DecodeString(branch)
			root = blake2b.SumPair(([32]byte)(branchBuf), root)
		}

		expectedRoot := blockMerkleRoot(minerPayouts, append(txns, coinbaseTxn))
		if root != expectedRoot {
			t.Fatalf("expected %x, got %x", expectedRoot, root)
		}
	})

	t.Run("v2", func(t *testing.T) {
		coinbaseTxn := types.V2Transaction{
			ArbitraryData: []byte{0x04, 0x05, 0x06},
		}
		h := types.NewHasher()
		h.E.WriteUint8(0x00)
		coinbaseTxn.EncodeTo(h.E)
		root := h.Sum()

		// hardfork active
		n, _ := testutil.Network()
		n.HardforkV2.AllowHeight = 0
		n.HardforkV2.RequireHeight = 0
		cs := consensus.State{
			Network: n,
		}

		for _, branch := range BlockMerkleBranches(cs, minerPayouts, txns, v2txns) {
			branchBuf, _ := hex.DecodeString(branch)
			root = blake2b.SumPair(([32]byte)(branchBuf), root)
		}

		expectedRoot := cs.Commitment(minerPayouts[0].Address, txns, append(v2txns, coinbaseTxn))
		if root != expectedRoot {
			t.Fatalf("expected %x, got %x", expectedRoot, root)
		}
	})
}
