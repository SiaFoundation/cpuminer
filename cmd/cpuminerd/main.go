package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/api"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"lukechampine.com/frand"
)

func mineBlock(ctx context.Context, c *api.Client, minerAddr types.Address, threads int, log *zap.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cs, err := c.ConsensusTipState()
	if err != nil {
		return fmt.Errorf("failed to get consensus tip state: %w", err)
	}
	_, txns, v2txns, err := c.TxpoolTransactions()
	if err != nil {
		return fmt.Errorf("failed to get txpool transactions: %w", err)
	}

	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}

			index, err := c.ConsensusTip()
			if err != nil {
				log.Error("failed to get consensus tip state", zap.Error(err))
				continue
			} else if index != cs.Index {
				log.Debug("consensus tip changed, restarting", zap.Stringer("newTip", index))
				cancel()
				return
			}

			_, newTxns, newV2txns, err := c.TxpoolTransactions()
			if err != nil {
				log.Error("failed to get txpool transactions", zap.Error(err))
				continue
			} else if len(newTxns) != len(txns) {
				log.Debug("txpool transactions changed, restarting", zap.Int("newTxns", len(newTxns)), zap.Int("oldTxns", len(txns)))
				cancel()
				return
			} else if len(newV2txns) != len(v2txns) {
				log.Debug("v2 txpool transactions changed, restarting", zap.Int("newV2txns", len(newV2txns)), zap.Int("oldV2txns", len(v2txns)))
				cancel()
				return
			}
		}
	}()

	nonSiaPrefix := types.NewSpecifier("NonSia")
	b := types.Block{
		ParentID:     cs.Index.ID,
		Nonce:        0,
		Timestamp:    types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{Address: minerAddr, Value: cs.BlockReward()}},
		Transactions: txns,
	}
	for _, txn := range txns {
		b.MinerPayouts[0].Value = b.MinerPayouts[0].Value.Add(txn.TotalFees())
	}
	for _, txn := range v2txns {
		b.MinerPayouts[0].Value = b.MinerPayouts[0].Value.Add(txn.MinerFee)
	}
	arbData := append(nonSiaPrefix[:], frand.Bytes(8)...)
	if cs.Index.Height+1 >= cs.Network.HardforkV2.AllowHeight {
		b.V2 = &types.V2BlockData{
			Height: cs.Index.Height + 1,
			Transactions: append(v2txns, types.V2Transaction{
				ArbitraryData: arbData,
			}),
		}
		b.V2.Commitment = cs.Commitment(b.MinerPayouts[0].Address, b.Transactions, b.V2Transactions())
	} else {
		b.Transactions = append(b.Transactions, types.Transaction{
			ArbitraryData: [][]byte{arbData},
		})
	}

	results := make(chan uint64, threads)
	step := cs.NonceFactor() * uint64(threads)
	var wg sync.WaitGroup
	defer wg.Wait() // wait for all threads to finish
	for i := range threads {
		wg.Add(1)
		log := log.With(zap.Int("thread", i+1))
		log.Debug("starting mining thread")

		go func(ctx context.Context, header types.BlockHeader, target types.BlockID, worker int, log *zap.Logger) {
			defer wg.Done()

			start := cs.NonceFactor() * uint64(worker)
			for nonce := start; ; nonce += step {
				select {
				case <-ctx.Done():
					log.Debug("stopped mining thread")
					return
				default:
				}

				header.Nonce = nonce
				if header.ID().CmpWork(target) >= 0 {
					results <- nonce
					log.Debug("found nonce", zap.Uint64("nonce", nonce))
					return
				}
			}
		}(ctx, b.Header(), cs.ChildTarget, i, log)
	}

	select {
	case nonce := <-results:
		cancel() // stop other threads

		b.Nonce = nonce
		tip, err := c.ConsensusTip()
		if err != nil {
			return fmt.Errorf("failed to get consensus tip: %w", err)
		} else if tip != cs.Index {
			log.Info("mined stale block", zap.Stringer("current", tip), zap.Stringer("original", cs.Index))
			return nil
		} else if err := c.SyncerBroadcastBlock(b); err != nil {
			return fmt.Errorf("failed to broadcast block: %w", err)
		}
		var height uint64
		if b.V2 != nil {
			height = b.V2.Height
		} else {
			height = cs.Index.Height + 1
		}
		log.Info("mined block", zap.Uint64("height", height), zap.Stringer("blockID", b.ID()), zap.Stringer("fees", b.MinerPayouts[0].Value), zap.Int("transactions", len(b.Transactions)), zap.Int("v2transactions", len(b.V2Transactions())))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseLogLevel(level string) zap.AtomicLevel {
	switch level {
	case "debug":
		return zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		return zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		return zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		return zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		fmt.Printf("invalid log level %q", level)
		os.Exit(1)
	}
	panic("unreachable")
}

func main() {
	var (
		minerAddrStr string

		apiAddress  string
		apiPassword string

		logLevel string

		threads int
	)

	flag.StringVar(&minerAddrStr, "address", "", "address to send mining rewards to")
	flag.StringVar(&apiAddress, "api", "http://localhost:9980/api", "address of the walletd API")
	flag.StringVar(&apiPassword, "password", "", "password for the walletd API")
	flag.IntVar(&threads, "threads", 1, "number of threads to use for mining")
	flag.StringVar(&logLevel, "log.level", "info", "log level")
	flag.Parse()

	var address types.Address
	if err := address.UnmarshalText([]byte(minerAddrStr)); err != nil {
		panic(err)
	}

	c := api.NewClient(apiAddress, apiPassword)
	if _, err := c.ConsensusTip(); err != nil {
		panic(err)
	}

	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.RFC3339TimeEncoder
	cfg.EncodeDuration = zapcore.StringDurationEncoder
	cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder

	cfg.StacktraceKey = ""
	cfg.CallerKey = ""
	encoder := zapcore.NewConsoleEncoder(cfg)

	log := zap.New(zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), parseLogLevel(logLevel)))
	defer log.Sync()

	zap.RedirectStdLog(log)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		default:
		}

		err := mineBlock(ctx, c, address, threads, log)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("failed to mine block", zap.Error(err))
		}
	}
}
