package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/version"
	"github.com/rescale/rescale-int/internal/wailsapp"
)

func main() {
	host := flag.String("host", "127.0.0.1", "HTTP bind host")
	port := flag.Int("port", 0, "HTTP bind port")
	flag.Parse()

	if *port <= 0 {
		fmt.Fprintln(os.Stderr, "--port is required")
		os.Exit(2)
	}

	cli.Version = version.Version
	cli.BuildTime = version.BuildTime

	addr := fmt.Sprintf("%s:%d", *host, *port)
	if err := wailsapp.RunEmbeddedBridge(addr); err != nil {
		fmt.Fprintf(os.Stderr, "embedded bridge failed: %v\n", err)
		os.Exit(1)
	}
}
