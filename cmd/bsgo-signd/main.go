package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/openSUSE/bsgo/pkg/signd"
)

func main() {
	var (
		listenAddr string
		passphrase string
	)

	flag.StringVar(&listenAddr, "listen", "0.0.0.0:5167", "Network address to listen on")
	flag.StringVar(&passphrase, "passphrase", "", "Master key passphrase (defaults to SIGNER_MASTER_KEY env)")
	flag.Parse()

	if passphrase == "" {
		passphrase = os.Getenv("SIGNER_MASTER_KEY")
		if passphrase == "" {
			fmt.Fprintln(os.Stderr, "error: master key passphrase required (via --passphrase or SIGNER_MASTER_KEY)")
			os.Exit(1)
		}
	}

	masterKey, err := signd.NewMasterKey(passphrase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed initializing master key: %v\n", err)
		os.Exit(1)
	}
	defer masterKey.Destroy()

	engine := signd.NewSignEngine(masterKey, nil)
	server := signd.NewServer(engine)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down bsgo-signd...")
		_ = server.Close()
	}()

	fmt.Printf("[bsgo-signd] Started bare-metal signing daemon on %s\n", listenAddr)
	if err := server.Start(listenAddr); err != nil {
		fmt.Fprintf(os.Stderr, "server exited: %v\n", err)
		os.Exit(1)
	}
}
