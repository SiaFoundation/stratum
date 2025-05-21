package stratum

import (
	"encoding/hex"

	"go.sia.tech/core/types"
)

// ExampleV2CoinbaseTxn demonstrates how to create a coinbase v2 transaction
// suitable for Stratum mining.
func ExampleV2CoinbaseTxn() {
	const label = "mining pool test"

	// send cb1 and cb2 to the miner
	cb1, cb2 := V2CoinbaseTxn(label)

	// after receiving the nonce back from the miner, the
	// transaction can be constructed by concatenating cb1 + extranonce1 + extranonce2 + cb2
	// where extranonce1 and extranonce2 are the 4-byte values
	extranonce1, extranonce2 := []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x05, 0x06, 0x07, 0x08}
	coinbaseTxnStr := cb1 + hex.EncodeToString(extranonce1) + hex.EncodeToString(extranonce2) + cb2
	buf, err := hex.DecodeString(coinbaseTxnStr)
	if err != nil {
		panic("failed to decode coinbase: " + err.Error())
	}
	dec := types.NewBufDecoder(buf)

	var txn types.V2Transaction
	txn.DecodeFrom(dec)
	if err := dec.Err(); err != nil {
		panic(err)
	}
}
