package stratum

import (
	"bytes"
	"encoding/hex"

	"go.sia.tech/core/blake2b"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

var nonSiaPrefix = types.NewSpecifier("NonSia")

// CoinbaseTxn creates the coinbase1 and coinbase2 parameters
// with the given label suitable for Stratum mining. The label
// can be used to identify the pool that found the block.
func CoinbaseTxn(label string) (cb1, cb2 string) {
	arbdata := append(append(nonSiaPrefix[:], []byte(label)...), make([]byte, 8)...) // 8 additional bytes for extranonce1 + extranonce2
	coinbaseTxn := types.Transaction{
		ArbitraryData: [][]byte{
			arbdata,
		},
	}
	buf := bytes.NewBuffer(nil)
	enc := types.NewEncoder(buf)
	coinbaseTxn.EncodeTo(enc)
	enc.Flush()
	coinbaseBuf := buf.Bytes()
	cb2Buf := coinbaseBuf[len(coinbaseBuf)-8:]             // signatures length prefix
	cb1Buf := coinbaseBuf[:len(coinbaseBuf)-8-len(cb2Buf)] // trim nonce placeholders and signatures length prefix
	return hex.EncodeToString(cb1Buf), hex.EncodeToString(cb2Buf)
}

// V2CoinbaseTxn creates the coinbase1 and coinbase2 parameters
// with the given label suitable for Stratum mining. The label
// can be used to identify the pool that found the block.
func V2CoinbaseTxn(label string) (cb1, cb2 string) {
	arbdata := append(append(nonSiaPrefix[:], []byte(label)...), make([]byte, 8)...) // 8 additional bytes for extranonce1 + extranonce2
	coinbaseTxn := types.V2Transaction{
		ArbitraryData: arbdata,
	}
	buf := bytes.NewBuffer(nil)
	enc := types.NewEncoder(buf)
	coinbaseTxn.EncodeTo(enc)
	enc.Flush()
	coinbaseBuf := buf.Bytes()
	cb1Buf := coinbaseBuf[:len(coinbaseBuf)-8] // trim extranonce placeholders
	return hex.EncodeToString(cb1Buf), ""      // coinbase2 is empty in v2 transactions because unset fields are excluded from the encoding
}

// BlockMerkleBranches returns the merkle branches for the given
// minerPayouts and transactions. The branches are returned as
// hex-encoded strings suitable for Stratum mining.
func BlockMerkleBranches(cs consensus.State, minerPayouts []types.SiacoinOutput, txns []types.Transaction, v2txns []types.V2Transaction) []string {
	const (
		leafHashPrefix = 0x00
	)
	acc := new(blake2b.Accumulator)
	h := types.NewHasher()
	if cs.Index.Height < cs.Network.HardforkV2.AllowHeight {
		// v1
		for _, mp := range minerPayouts {
			h.Reset()
			h.E.WriteUint8(leafHashPrefix)
			types.V1SiacoinOutput(mp).EncodeTo(h.E)
			acc.AddLeaf(([32]byte)(h.Sum()))
		}
		for _, txn := range txns {
			acc.AddLeaf(txn.MerkleLeafHash())
		}
	} else {
		// state is first hashed separately
		cs.EncodeTo(h.E)
		stateHash := h.Sum()

		// first leaf is the current chain state
		h.Reset()
		h.E.WriteUint8(0)
		h.E.Write([]byte("sia/commitment|"))
		h.E.WriteUint8(2)
		stateHash.EncodeTo(h.E)
		minerPayouts[0].Address.EncodeTo(h.E)
		h.E.Flush()
		acc.AddLeaf(h.Sum())
		for _, txn := range txns {
			acc.AddLeaf(txn.MerkleLeafHash())
		}
		for _, txn := range v2txns {
			acc.AddLeaf(txn.MerkleLeafHash())
		}
	}

	roots := make([]string, 0, len(acc.Trees))
	for height, root := range acc.Trees {
		if acc.NumLeaves&(1<<height) != 0 {
			roots = append(roots, hex.EncodeToString(root[:]))
		}
	}
	return roots
}
