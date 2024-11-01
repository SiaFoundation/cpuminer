package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/walletd/api"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"lukechampine.com/frand"
)

func runCPUMiner(c *api.Client, minerAddr types.Address, log *zap.Logger) {
	log.Info("starting cpu miner", zap.String("minerAddr", minerAddr.String()))
	start := time.Now()

	check := func(msg string, err error) bool {
		if err != nil {
			log.Error(msg, zap.Error(err))
			time.Sleep(15 * time.Second)
			return false
		}
		return true
	}

	for {
		elapsed := time.Since(start)
		cs, err := c.ConsensusTipState()
		if !check("failed to get consensus tip state", err) {
			continue
		}

		d, _ := new(big.Int).SetString(cs.Difficulty.String(), 10)
		d.Mul(d, big.NewInt(int64(1+elapsed)))
		log := log.With(zap.Uint64("height", cs.Index.Height+1), zap.Stringer("parentID", cs.Index.ID), zap.Stringer("difficulty", d))

		log.Debug("mining block")
		txns, v2txns, err := c.TxpoolTransactions()
		if !check("failed to get txpool transactions", err) {
			continue
		}

		b := types.Block{
			ParentID:     cs.Index.ID,
			Nonce:        cs.NonceFactor() * frand.Uint64n(100),
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
		if len(v2txns) > 0 || cs.Index.Height+1 >= cs.Network.HardforkV2.RequireHeight {
			b.V2 = &types.V2BlockData{
				Height:       cs.Index.Height + 1,
				Transactions: v2txns,
			}
			b.V2.Commitment = cs.Commitment(cs.TransactionsCommitment(b.Transactions, b.V2Transactions()), b.MinerPayouts[0].Address)
		}

		if !coreutils.FindBlockNonce(cs, &b, time.Minute) {
			log.Debug("failed to find nonce")
			continue
		}
		log.Debug("found nonce", zap.Uint64("nonce", b.Nonce))
		tip, err := c.ConsensusTip()
		if !check("failed to get consensus tip:", err) {
			continue
		}

		if tip != cs.Index {
			log.Info("mined stale block", zap.Stringer("current", tip), zap.Stringer("original", cs.Index))
		} else if err := c.SyncerBroadcastBlock(b); err != nil {
			log.Error("mined invalid block", zap.Error(err))
		} else {
			log.Info("mined block", zap.Stringer("blockID", b.ID()), zap.Stringer("fees", b.MinerPayouts[0].Value), zap.Int("transactions", len(b.Transactions)), zap.Int("v2transactions", len(b.V2Transactions())))
		}
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
	)

	flag.StringVar(&minerAddrStr, "address", "", "address to send mining rewards to")
	flag.StringVar(&apiAddress, "api", "localhost:9980", "address of the walletd API")
	flag.StringVar(&apiPassword, "password", "", "password for the walletd API")
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

	runCPUMiner(c, address, log)
}
