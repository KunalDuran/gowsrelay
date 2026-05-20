package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/KunalDuran/gowsrelay/client"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := client.TunnelConfig{
		Scheme: "ws",
		Host:   "localhost:8090",
		Path:   "/ws",
		Topic:  "UwBLmcwuQ4_rce",
	}

	// --- Option 1: TCP port forwarding (original behavior) ---
	// ep, err := client.TCPEndpoint("google.com", "443")
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// if err := client.CreateWebSocketTunnel(ctx, cfg, ep); err != nil {
	// 	log.Fatal(err)
	// }

	// --- Option 2: Pre-configured command ---
	// Spawns the command immediately; remote side receives its stdout.
	// cmd, err := client.NewCmdEndpoint("ipconfig")
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// if err := client.CreateWebSocketTunnel(ctx, cfg, cmd); err != nil {
	// 	log.Fatal(err)
	// }

	// --- Option 3: Command-on-demand (kubectl exec model) ---
	// Connects first; the remote subscriber sends the command as the first
	// WS message (e.g. "ipconfig /all"), then receives the output.
	lazy := client.NewCmdEndpoint()
	if err := client.CreateWebSocketTunnel(ctx, cfg, lazy); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}
