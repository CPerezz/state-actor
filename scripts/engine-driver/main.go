// Command engine-driver is a standalone CL mock that drives the engine
// API of a running execution-layer client (besu / nethermind / reth /
// erigon) in dev mode. Wraps internal/engineapi.EngineDriver.
//
// Usage:
//
//	engine-driver \
//	  -engine http://127.0.0.1:8551 \
//	  -eth http://127.0.0.1:8545 \
//	  -fork osaka \
//	  -block-time 1s
//
// Runs DriveLoop until SIGINT/SIGTERM. JWT is NOT supported — boot the
// EL with --engine-jwt-disabled or equivalent.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	engineapi "github.com/nerolation/state-actor/internal/engineapi"
)

func main() {
	engineURL := flag.String("engine", "http://127.0.0.1:8551", "engine API URL")
	ethURL := flag.String("eth", "http://127.0.0.1:8545", "JSON-RPC URL")
	fork := flag.String("fork", "osaka", "engine method version: cancun | prague | osaka")
	blockTime := flag.Duration("block-time", time.Second, "slot duration")
	flag.Parse()

	driver := &engineapi.EngineDriver{
		EngineURL: *engineURL,
		EthRPCURL: *ethURL,
		BlockTime: *blockTime,
		Fork:      engineapi.Fork(*fork),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("signal received, shutting down driver")
		cancel()
	}()

	log.Printf("engine-driver: engine=%s eth=%s fork=%s block-time=%s",
		*engineURL, *ethURL, *fork, *blockTime)
	if err := driver.DriveLoop(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("driver exited with error: %v", err)
	}
	log.Printf("driver shut down cleanly")
}
