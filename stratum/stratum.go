package stratum

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/api"
	"go.uber.org/zap"
	"lukechampine.com/frand"
)

type Server struct {
	rpcTimeout time.Duration

	client *api.Client
	log    *zap.Logger
	closed chan struct{}
}

func NewServer(client *api.Client, log *zap.Logger) *Server {
	return &Server{
		rpcTimeout: time.Minute,
		client:     client,
		log:        log,
		closed:     make(chan struct{}, 1),
	}
}

func (s *Server) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func paramOrDefault[T any](params []any, index int, def T) T {
	if len(params) <= index {
		return def
	}
	if v, ok := params[index].(T); ok {
		return v
	}
	return def
}

//
// The following function is covered by the included license
// The function was copied from https://github.com/btcsuite/btcd
//
// ISC License

// Copyright (c) 2013-2017 The btcsuite developers
// Copyright (c) 2015-2016 The Decred developers

// Permission to use, copy, modify, and distribute this software for any
// purpose with or without fee is hereby granted, provided that the above
// copyright notice and this permission notice appear in all copies.

// THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
// WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
// MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
// ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
// WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
// ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
// OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

// BigToCompact converts a whole number N to a compact representation using
// an unsigned 32-bit number.  The compact representation only provides 23 bits
// of precision, so values larger than (2^23 - 1) only encode the most
// significant digits of the number.  See CompactToBig for details.
func bigToCompact(n *big.Int) uint32 {
	// No need to do any work if it's zero.
	if n.Sign() == 0 {
		return 0
	}

	// Since the base for the exponent is 256, the exponent can be treated
	// as the number of bytes.  So, shift the number right or left
	// accordingly.  This is equivalent to:
	// mantissa = mantissa / 256^(exponent-3)
	var mantissa uint32
	exponent := uint(len(n.Bytes()))
	if exponent <= 3 {
		mantissa = uint32(n.Bits()[0])
		mantissa <<= 8 * (3 - exponent)
	} else {
		// Use a copy to avoid modifying the caller's original number.
		tn := new(big.Int).Set(n)
		mantissa = uint32(tn.Rsh(tn, 8*(exponent-3)).Bits()[0])
	}

	// When the mantissa already has the sign bit set, the number is too
	// large to fit into the available 23-bits, so divide the number by 256
	// and increment the exponent accordingly.
	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		exponent++
	}

	// Pack the exponent, sign bit, and mantissa into an unsigned 32-bit
	// int and return it.
	compact := uint32(exponent<<24) | mantissa
	if n.Sign() < 0 {
		compact |= 0x00800000
	}
	return compact
}

func (s *Server) getBlockForBroadcast(addr types.Address, nonce []byte) (types.Block, error) {
	cs, err := s.client.ConsensusTipState()
	if err != nil {
		return types.Block{}, fmt.Errorf("failed to get consensus tip state: %w", err)
	}
	_, poolTxns, _, err := s.client.TxpoolTransactions()
	if err != nil {
		return types.Block{}, fmt.Errorf("failed to get txpool transactions: %w", err)
	}

	coinbaseTxn := types.Transaction{
		ArbitraryData: [][]byte{
			[]byte("Nate Pool Yolo"),
			frand.Bytes(8),
		},
	}
	b := types.Block{
		ParentID:  cs.Index.ID,
		Timestamp: time.Now(),
		Nonce:     binary.BigEndian.Uint64(nonce),
		MinerPayouts: []types.SiacoinOutput{
			{Address: addr, Value: cs.BlockReward()},
		},
		Transactions: append([]types.Transaction{coinbaseTxn}, poolTxns...),
	}
	if b.ID().CmpWork(cs.ChildTarget) < 0 {
		return types.Block{}, errors.New("block does not meet target")
	}
	return b, nil
}

func (s *Server) Listen(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		log := s.log.With(zap.String("remote", conn.RemoteAddr().String()))

		go func() {
			defer conn.Close()

			select {
			case <-s.closed:
				return
			default:
			}

			var lastID int
			nextID := func() int {
				lastID++
				return lastID
			}

			difficultySubscriptionID := hex.EncodeToString(frand.Bytes(4))
			notifySubscriptionID := hex.EncodeToString(frand.Bytes(4))

			//dec := json.NewDecoder(conn)
			readRequest := func() (req StratumRequest, err error) {
				buf := bytes.NewBuffer(nil)
				dec := json.NewDecoder(io.TeeReader(conn, buf))
				err = dec.Decode(&req)
				return
			}

			writeResponse := func(req StratumRequest, result any, error []any) error {
				resp := StratumResponse{
					ID:     req.ID,
					Result: result,
					Error:  error,
				}
				buf, err := json.Marshal(resp)
				if err != nil {
					return fmt.Errorf("failed to encode resp")
				}
				buf = append(buf, '\n')
				_, err = conn.Write(buf)
				if err != nil {
					return err
				}
				log.Debug("write response", zap.String("response", string(buf)))
				return nil
			}

			writeRequest := func(method string, params []any) error {
				req := StratumRequest{
					ID:     nextID(),
					Method: method,
					Params: params,
				}
				buf, err := json.Marshal(req)
				if err != nil {
					return fmt.Errorf("failed to encode req")
				}
				buf = append(buf, '\n')
				_, err = conn.Write(buf)
				if err != nil {
					return err
				}
				log.Debug("write request", zap.String("request", string(buf)))
				return nil
			}

			sendNotify := func(clean bool) error {
				cs, err := s.client.ConsensusTipState()
				if err != nil {
					return fmt.Errorf("failed to get consensus tip state: %w", err)
				}

				coinbaseTxn := types.Transaction{
					ArbitraryData: [][]byte{
						[]byte("Nate Pool Yolo"),
						frand.Bytes(4),
					},
				}
				txnBuf := bytes.NewBuffer(nil)
				enc := types.NewEncoder(txnBuf)
				coinbaseTxn.EncodeTo(enc)
				if err := enc.Flush(); err != nil {
					return fmt.Errorf("failed to encode coinbase txn: %w", err)
				}

				timeBuf := make([]byte, 8)
				binary.LittleEndian.PutUint64(timeBuf, uint64(cs.PrevTimestamps[0].UnixMilli()))

				err = writeRequest("mining.notify", []any{
					fmt.Sprintf("%d", nextID()),
					cs.Index.ID,
					hex.EncodeToString(txnBuf.Bytes()),
					"0000000000000000",
					[]string{"526f07beaa9cd09686dd26f1c835b9c790e2ebb4253eb96b1aec6bf8c2e4f758", "35d1eacb84df9add18358f6713d82e3df22b5583c0e8c057da156873dca98a1e"},
					"",
					fmt.Sprintf("%08x", bigToCompact(new(big.Int).SetBytes(cs.ChildTarget[:]))),
					hex.EncodeToString(timeBuf),
					clean,
				})
				if err != nil {
					return fmt.Errorf("failed to send notify: %w", err)
				}
				return nil
			}

			errCh := make(chan error, 1)
			requests := make(chan StratumRequest, 1)

			go func() {
				for {
					select {
					case <-s.closed:
						return
					default:
					}

					req, err := readRequest()
					if err != nil {
						errCh <- fmt.Errorf("failed to decode request: %w", err)
						return
					}
					requests <- req
				}
			}()

			for {
				select {
				case <-s.closed:
					return
				case err := <-errCh:
					log.Warn("request error", zap.Error(err))
					return
				case <-time.After(15 * time.Second):
					if err := sendNotify(false); err != nil {
						log.Warn("failed to send notify", zap.Error(err))
						continue
					}
				case req := <-requests:
					log.Debug("request received", zap.Any("request", req))
					log := log.With(zap.String("method", req.Method), zap.Any("id", req.ID))

					switch req.Method {
					case "mining.subscribe":
						difficultySubscriptionID = hex.EncodeToString(frand.Bytes(8))
						notifySubscriptionID = hex.EncodeToString(frand.Bytes(8))
						// clientName := paramOrDefault(req.Params, 0, "")
						err = writeResponse(req, []any{
							[]any{
								[]any{"mining.set_difficulty", difficultySubscriptionID},
								[]any{"mining.notify", notifySubscriptionID},
							},
							hex.EncodeToString(frand.Bytes(4)),
							4,
						}, nil)
						if err != nil {
							log.Warn("failed to encode response", zap.Error(err))
							return
						}
					case "mining.authorize":
						workerName := paramOrDefault(req.Params, 0, "")
						workerName, walletAddress, ok := strings.Cut(workerName, ".")
						if !ok {
							walletAddress = workerName
						}

						var addr types.Address
						if err := addr.UnmarshalText([]byte(walletAddress)); err != nil {
							log.Debug("failed to parse wallet address", zap.String("address", walletAddress), zap.Error(err))
							writeResponse(req, false, []any{"client name must be a valid Sia address"})
							continue
						}
						writeResponse(req, true, nil)
						writeRequest("mining.set_difficulty", []any{
							float64(2000.0),
						})
						if err := sendNotify(true); err != nil {
							log.Warn("failed to send notify", zap.Error(err))
						}
					case "mining.submit":
						workerName := paramOrDefault(req.Params, 0, "")
						workerName, walletAddress, ok := strings.Cut(workerName, ".")
						if !ok {
							walletAddress = workerName
						}

						var payoutAddr types.Address
						if err := payoutAddr.UnmarshalText([]byte(walletAddress)); err != nil {
							log.Debug("failed to parse wallet address", zap.String("address", walletAddress), zap.Error(err))
							writeResponse(req, false, []any{"client name must be a valid Sia address"})
							continue
						}

						jobID := paramOrDefault(req.Params, 1, "")
						// extraNonce2 := paramOrDefault(req.Params, 2, "")
						// nTime := paramOrDefault(req.Params, 3, "")
						nonceStr := paramOrDefault(req.Params, 4, "")

						nonce, err := hex.DecodeString(nonceStr)
						if err != nil {
							log.Debug("failed to decode nonce", zap.String("nonce", nonceStr), zap.Error(err))
							writeResponse(req, false, []any{"invalid nonce"})
							continue
						}

						log := log.With(zap.String("jobID", jobID))

						b, err := s.getBlockForBroadcast(payoutAddr, nonce)
						if err != nil {
							log.Debug("failed to get block for broadcast", zap.Error(err))
							writeResponse(req, false, []any{"invalid nonce"})
							continue
						}

						if err := s.client.SyncerBroadcastBlock(b); err != nil {
							log.Debug("failed to broadcast block", zap.Error(err))
							writeResponse(req, false, []any{"invalid nonce"})
							continue
						}
					}
				}
			}
		}()
	}
}
