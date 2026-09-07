// rmtunnel is a small, self-contained reverse-tunnel core: TCP and TCPMux
// transports only, no web panel, no telegram bot, no Noise stealth layer —
// on purpose. It exists so every knob that affects performance is a field in
// config.go you can find in ten seconds and a code path in server.go/client.go
// you can actually read start to finish. See README.md for the architecture
// and docs/TUNING.md for what to try first.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if len(os.Args) < 2 {
		runMenu()
		return
	}

	if os.Args[1] == "menu" {
		runMenu()
		return
	}

	if os.Args[1] == "bench" {
		if err := runBench(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bench:", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) != 3 || (os.Args[1] != "server" && os.Args[1] != "client") {
		printUsage()
		os.Exit(2)
	}
	role, path := os.Args[1], os.Args[2]

	cfg, err := LoadConfig(path)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	cfg.Role = role

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch role {
	case "server":
		srv := NewServer(cfg)
		go statsLoop(ctx, srv.Stats)
		if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
			log.Fatalf("server stopped: %v", err)
		}
	case "client":
		cli := NewClient(cfg)
		go statsLoop(ctx, cli.Stats)
		cli.Run(ctx)
	}
	log.Println("shut down cleanly")
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `usage:
  %s                                (interactive menu)
  %s menu
  %s server <config.toml>
  %s client <config.toml>
  %s bench server <listen_addr> <token>
  %s bench client <server_addr> <token>
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

func statsLoop(ctx context.Context, snapshot func() string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Printf("stats: %stotal transferred=%s", snapshot(), humanBytes(TotalBytesTransferred()))
		}
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
