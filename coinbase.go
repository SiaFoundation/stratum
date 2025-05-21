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
	return hex.EncodeToString(cb1Buf), ""
}

// BlockMerkleBranches returns the merkle branches for the given
// minerPayouts and transactions. The branches are returned as
// hex-encoded strings suitable for Stratum mining.
func BlockMerkleBranches(cs consensus.State, minerPayouts []types.SiacoinOutput, txns []types.Transaction, v2txns []types.V2Transaction) []string {
	const (
		leafHashPrefix = 0x00
	)
	acc := new(blake2b.Accumulator)
	h := blake2b.New256()
	enc := types.NewEncoder(h)
	if cs.Index.Height < cs.Network.HardforkV2.AllowHeight {
		// v1
		for _, mp := range minerPayouts {
			h.Reset()
			enc.WriteUint8(leafHashPrefix)
			types.V1SiacoinOutput(mp).EncodeTo(enc)
			enc.Flush()
			acc.AddLeaf(([32]byte)(h.Sum(nil)))
		}
		for _, txn := range txns {
			h.Reset()
			enc.WriteUint8(leafHashPrefix)
			txn.EncodeTo(enc)
			enc.Flush()
			acc.AddLeaf(([32]byte)(h.Sum(nil)))
		}
	} else {
		// state is first hashed separately
		cs.EncodeTo(enc)
		enc.Flush()
		stateHash := (types.Hash256)(h.Sum(nil))

		// first leaf is the current chain state
		h.Reset()
		enc.WriteUint8(0)
		enc.Write([]byte("sia/commitment|"))
		enc.WriteUint8(2)
		stateHash.EncodeTo(enc)
		minerPayouts[0].Address.EncodeTo(enc)
		enc.Flush()
		acc.AddLeaf(([32]byte)(h.Sum(nil)))
		for _, txn := range txns {
			h.Reset()
			acc.AddLeaf(txn.FullHash())
		}
		for _, txn := range v2txns {
			h.Reset()
			acc.AddLeaf(txn.FullHash())
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
