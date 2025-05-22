package server

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"go.sia.tech/core/consensus"
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

func (s *Server) getBlockForBroadcast(cs consensus.State, addr types.Address, nonce uint64, timestamp time.Time, arbTxn types.Transaction) (types.Block, error) {
	b := types.Block{
		ParentID:  cs.Index.ID,
		Timestamp: timestamp,
		Nonce:     nonce,
		MinerPayouts: []types.SiacoinOutput{
			{Address: addr, Value: cs.BlockReward()},
		},
		Transactions: []types.Transaction{arbTxn},
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

			writeResponse := func(req StratumRequest, result any, errStr string) error {
				resp := StratumResponse{
					ID:     req.ID,
					Result: result,
				}
				if errStr != "" {
					resp.Error = &errStr
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

			// global session state
			var (
				extranonce1          = hex.EncodeToString(frand.Bytes(4))
				coinbase1, coinbase2 = stratum.CoinbaseTxn("Sia Solo Stratum")
			)

			sendNotify := func(clean bool) error {
				cs, err := s.client.ConsensusTipState()
				if err != nil {
					return fmt.Errorf("failed to get consensus tip state: %w", err)
				}

				nTime := types.CurrentTimestamp()
				timeBuf := make([]byte, 8)
				binary.LittleEndian.PutUint64(timeBuf, uint64(nTime.Unix()))
				log.Debug("sent time", zap.Time("nTime", nTime))

				err = writeRequest("mining.notify", []any{
					fmt.Sprintf("%d", nextID()), // jobID
					cs.Index.ID,                 // parent ID
					coinbase1,
					coinbase2,
					stratum.BlockMerkleBranches(cs, []types.SiacoinOutput{
						{Address: types.VoidAddress, Value: cs.BlockReward()},
					}, []types.Transaction{}, nil), // merkle tree of the rest of the block contents
					"", // version???
					"",
					hex.EncodeToString(timeBuf), // block time
					clean,                       // clean jobs
				})
				if err != nil {
					return fmt.Errorf("failed to send notify: %w", err)
				}
				return nil
			}

			requests := make(chan StratumRequest, 1)

			go func() {
				rr := bufio.NewScanner(conn)
				for rr.Scan() {
					select {
					case <-s.closed:
						return
					default:
					}

					line := rr.Bytes()
					var req StratumRequest
					if err := json.Unmarshal(line, &req); err != nil {
						log.Warn("failed to decode request", zap.String("line", string(line)), zap.Error(err))
						continue
					}
					log.Debug("request received", zap.String("request", string(line)))
					requests <- req
				}
			}()

			for {
				select {
				case <-s.closed:
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
							extranonce1,
							4,
						}, "")
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
							writeResponse(req, false, "invalid address")
							continue
						}

						writeResponse(req, true, "")
						writeRequest("mining.set_difficulty", []any{
							// TODO: Setting this to the block target or trying to
							// calculate the difficulty does not appear to work. I
							// either get no shares or "not enough work" when submitting
							// shares.
							//
							// I have no idea what to set this to and it's entirely
							// possible I'm not calculating the difficulty correctly.
							//diff,
							10,
						})
						if err := sendNotify(false); err != nil {
							log.Warn("failed to send notify", zap.Error(err))
						}
					case "mining.submit":
						cs, err := s.client.ConsensusTipState()
						if err != nil {
							panic(err)
						}
						jobID := paramOrDefault(req.Params, 1, "")
						extranonce2 := paramOrDefault(req.Params, 2, "")

						nTimeStr := paramOrDefault(req.Params, 3, "")
						nonceStr := paramOrDefault(req.Params, 4, "")

						nonceBuf, err := hex.DecodeString(nonceStr)
						if err != nil {
							log.Debug("failed to decode nonce", zap.String("nonce", nonceStr), zap.Error(err))
							writeResponse(req, false, "invalid nonce")
							continue
						}
						nonce := binary.LittleEndian.Uint64(nonceBuf)

						timeBuf, err := hex.DecodeString(nTimeStr)
						if err != nil {
							log.Debug("failed to decode nTime", zap.String("nTimeStr", nTimeStr), zap.Error(err))
							writeResponse(req, false, "invalid nTime")
							continue
						}
						timestamp := time.Unix(int64(binary.LittleEndian.Uint64(timeBuf)), 0)
						log := log.With(zap.String("jobID", jobID), zap.Uint64("nonce", nonce), zap.Time("nTime", timestamp))

						coinbaseTxn, err := stratum.ParseCoinbaseTxn(coinbase1, extranonce1, extranonce2, coinbase2)
						if err != nil {
							log.Debug("failed to parse coinbase txn", zap.String("coinbase1", coinbase1), zap.String("coinbase2", coinbase2),
								zap.String("extranonce1", extranonce1), zap.String("extranonce2", extranonce2), zap.Error(err))
							writeResponse(req, false, "invalid coinbase")
							continue
						}

						b, err := s.getBlockForBroadcast(cs, types.VoidAddress, nonce, timestamp, coinbaseTxn)
						if err != nil {
							log.Debug("failed to get block for broadcast", zap.Error(err))
							writeResponse(req, false, "invalid nonce")
							continue
						}

						if b.ID().CmpWork(cs.ChildTarget) < 0 {
							// ignore invalid shares. It'll find one eventually
							log.Debug("not enough work", zap.Stringer("id", b.ID()), zap.Stringer("target", cs.ChildTarget))
							continue
						}

						log.Debug("built block", zap.Stringer("id", b.ID()), zap.Stringer("target", cs.ChildTarget),
							zap.String("nTimeStr", nTimeStr), zap.String("nonceStr", nonceStr))
						extranonce1 = hex.EncodeToString(frand.Bytes(4)) // refresh extranonce1

						if err := writeResponse(req, true, ""); err != nil {
							log.Warn("failed to send success response", zap.Error(err))
							return
						}

						go func() {
							if err := s.client.SyncerBroadcastBlock(b); err != nil {
								log.Debug("failed to broadcast block", zap.Error(err))
							}
						}()

					}
				}
			}
		}()
	}
}
