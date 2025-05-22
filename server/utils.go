package server

import (
	"math/big"

	"go.sia.tech/core/types"
)

var maxTarget = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// This doesn't appear to work :shrug:
func difficulty(childTarget types.BlockID) uint64 {
	i := new(big.Int).SetBytes(childTarget[:])
	i.Div(maxTarget, i)
	return i.Uint64()
}
