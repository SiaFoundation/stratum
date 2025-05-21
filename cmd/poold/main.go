package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"

	"go.sia.tech/poold/stratum"
	"go.sia.tech/walletd/v2/api"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	var (
		stratumAddress  = ":9988"
		logLevel        = "debug"
		walletdAddr     = "http://localhost:9980/api"
		walletdPassword = "sia is cool"
	)
	flag.StringVar(&stratumAddress, "stratum.address", stratumAddress, "address to listen on")
	flag.StringVar(&walletdAddr, "walletd.address", walletdAddr, "address of walletd")
	flag.StringVar(&walletdPassword, "walletd.password", walletdPassword, "password for walletd")
	flag.StringVar(&logLevel, "log.level", logLevel, "log level")
	flag.Parse()

	cfg := zap.NewProductionEncoderConfig()
	cfg.TimeKey = "" // prevent duplicate timestamps
	cfg.EncodeTime = zapcore.RFC3339TimeEncoder
	cfg.EncodeDuration = zapcore.StringDurationEncoder
	cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder

	cfg.StacktraceKey = ""
	cfg.CallerKey = ""
	encoder := zapcore.NewConsoleEncoder(cfg)

	var level zap.AtomicLevel
	switch logLevel {
	case "debug":
		level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		level = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		fmt.Printf("invalid log level %q", level)
		os.Exit(1)
	}

	log := zap.New(zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), level))
	defer log.Sync()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	l, err := net.Listen("tcp", stratumAddress)
	if err != nil {
		log.Fatal("failed to create listener", zap.Error(err))
	}
	defer l.Close()

	client := api.NewClient(walletdAddr, walletdPassword)
	if _, err := client.ConsensusTipState(); err != nil {
		log.Fatal("failed to connect to walletd", zap.Error(err))
	}

	s := stratum.NewServer(client, log.Named("stratum"))
	defer s.Close()

	log.Info("stratum server started", zap.String("address", l.Addr().String()))
	go func() {
		if err := s.Listen(l); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Error("failed to listen", zap.Error(err))
		}
	}()

	<-ctx.Done()
}
