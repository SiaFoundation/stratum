package server

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/stratum"
	"go.sia.tech/walletd/v2/api"
	"go.uber.org/zap"
	"lukechampine.com/frand"
)

type Server struct {
	rpcTimeout time.Duration

	client *api.Client
	log    *zap.Logger
	closed chan struct{}
}

func New(client *api.Client, log *zap.Logger) *Server {
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

func (s *Server) getBlockForBroadcast(addr types.Address, nonce, extranonce1, extranonce2 []byte, timestamp time.Time) (types.Block, error) {
	cs, err := s.client.ConsensusTipState()
	if err != nil {
		return types.Block{}, fmt.Errorf("failed to get consensus tip state: %w", err)
	}

	spec := types.NewSpecifier("NonSia")
	coinbaseTxn := types.Transaction{
		ArbitraryData: [][]byte{
			append(spec[:], append(extranonce1, extranonce2...)...),
		},
	}
	b := types.Block{
		ParentID:  cs.Index.ID,
		Timestamp: timestamp,
		Nonce:     binary.LittleEndian.Uint64(nonce),
		MinerPayouts: []types.SiacoinOutput{
			{Address: addr, Value: cs.BlockReward()},
		},
		Transactions: []types.Transaction{coinbaseTxn},
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
					return fmt.Errorf("failed to encode req: %w", err)
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

				prefixNonSia := types.NewSpecifier("NonSia")
				arbData := append(prefixNonSia[:], make([]byte, 8)...)
				trim := len(arbData) + 8 // arbitrary data + 8 byte signature length prefix
				coinbaseTxn := types.Transaction{
					ArbitraryData: [][]byte{
						arbData, // placeholder data
					},
				}
				txnBuf := bytes.NewBuffer(nil)
				enc := types.NewEncoder(txnBuf)
				coinbaseTxn.EncodeTo(enc)
				if err := enc.Flush(); err != nil {
					return fmt.Errorf("failed to encode coinbase txn: %w", err)
				}
				// the miner will do coinbase1 + extranonce1 + extranonce2 + coinbase2
				// coinbase1 is the transaction up to the arbitrary data length prefix
				// extranonce1 is the 4 random bytes we sent in mining.subscribe
				// extranonce2 is the 4 random bytes the miner sends in mining.submit
				// coinbase2 is the 0 length prefix for signatures
				coinbaseBuf := txnBuf.Bytes()
				coinbase1 := coinbaseBuf[:len(coinbaseBuf)-trim]
				coinbase2 := coinbaseBuf[len(coinbaseBuf)-8:]

				timeBuf := make([]byte, 8)
				binary.LittleEndian.PutUint64(timeBuf, uint64(types.CurrentTimestamp().Unix()))
				log.Debug("sent time", zap.String("time", fmt.Sprint(timeBuf)))

				err = writeRequest("mining.notify", []any{
					fmt.Sprintf("%d", nextID()),   // jobID
					cs.Index.ID,                   // parent ID
					hex.EncodeToString(coinbase1), // encoded transaction minus arbitrary data + signatures
					hex.EncodeToString(coinbase2), // encoded transaction signature length prefix
					stratum.BlockMerkleBranches(cs, []types.SiacoinOutput{
						{Address: types.VoidAddress, Value: cs.BlockReward()},
					}, []types.Transaction{}, nil), // merkle tree of the rest of the block contents
					"", // version???
					fmt.Sprintf("%08x", bigToCompact(new(big.Int).SetBytes(cs.ChildTarget[:]))), // optional
					hex.EncodeToString(timeBuf), // block time
					clean,                       // clean jobs
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

			// sent in mining.subscribe and used by the miner to build the coinbase transaction
			// should be regenerated every time a block is mined
			extranonce1 := make([]byte, 4) // frand.Bytes(4)

			for {
				select {
				case <-s.closed:
					return
				case err := <-errCh:
					log.Warn("request error", zap.Error(err))
					return
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
							hex.EncodeToString(extranonce1),
							4,
						}, nil)
						if err != nil {
							log.Warn("failed to encode response", zap.Error(err))
							return
						}
					case "mining.authorize":
						cs, err := s.client.ConsensusTipState()
						if err != nil {
							log.Debug("failed to get tip")
							return
						}
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

						diff := difficulty(cs.ChildTarget)
						log.Debug("difficulty", zap.Uint64("difficulty", diff))

						writeResponse(req, true, nil)
						writeRequest("mining.set_difficulty", []any{
							// TODO: Setting this to the block target or trying to
							// calculate the difficulty does not appear to work. I
							// either get no shares or "not enough work" when submitting
							// shares.
							//
							// I have no idea what to set this to and it's entirely
							// possible I'm not calculating the difficulty correctly.
							//diff,
							10000,
						})
						if err := sendNotify(true); err != nil {
							log.Warn("failed to send notify", zap.Error(err))
						}
					case "mining.submit":
						cs, err := s.client.ConsensusTipState()
						if err != nil {
							panic(err)
						}
						jobID := paramOrDefault(req.Params, 1, "")
						extranonce2, err := hex.DecodeString(paramOrDefault(req.Params, 2, ""))
						if err != nil {
							log.Debug("failed to decode extranonce2", zap.String("extranonce2", paramOrDefault(req.Params, 2, "")), zap.Error(err))
							writeResponse(req, false, []any{"invalid extranonce2"})
							continue
						} else if len(extranonce2) != 4 {
							log.Debug("invalid extranonce2 length", zap.String("extranonce2", paramOrDefault(req.Params, 2, "")))
							writeResponse(req, false, []any{"invalid extranonce2"})
							continue
						}

						nTimeStr := paramOrDefault(req.Params, 3, "")
						nonceStr := paramOrDefault(req.Params, 4, "")
						log.Debug("params", zap.String("nTime", nTimeStr), zap.String("nonce", nonceStr))

						nonce, err := hex.DecodeString(nonceStr)
						if err != nil {
							log.Debug("failed to decode nonce", zap.String("nonce", nonceStr), zap.Error(err))
							writeResponse(req, false, []any{"invalid nonce"})
							continue
						}
						timeBuf, err := hex.DecodeString(nTimeStr)
						if err != nil {
							log.Debug("failed to decode nTime", zap.String("nTimeStr", nTimeStr), zap.Error(err))
							writeResponse(req, false, []any{"invalid nTime"})
							continue
						}

						log.Debug("received time", zap.String("time", fmt.Sprint(timeBuf)))
						timestamp := time.Unix(int64(binary.LittleEndian.Uint64(timeBuf)), 0)

						log := log.With(zap.String("jobID", jobID), zap.String("nonce", nonceStr), zap.Uint64("nonceU64", binary.LittleEndian.Uint64(nonce)),
							zap.Time("nTime", timestamp))

						b, err := s.getBlockForBroadcast(types.VoidAddress, nonce, extranonce1, extranonce2, timestamp)
						if err != nil {
							log.Debug("failed to get block for broadcast", zap.Error(err))
							writeResponse(req, false, []any{"invalid nonce"})
							continue
						}

						log.Debug("built block", zap.Stringer("id", b.ID()), zap.Stringer("target", cs.ChildTarget),
							zap.String("nTimeStr", nTimeStr), zap.String("nonceStr", nonceStr))
						//extranonce1 = frand.Bytes(4)

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
