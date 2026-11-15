package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/openSUSE/bsgo/pkg/signd"
)

func configureSecurity(server *signd.Server, allowCIDRs, tlsCert, tlsKey, clientCA string) error {
	if allowCIDRs != "" {
		cidrs := strings.Split(allowCIDRs, ",")
		if err := server.SetAllowedCIDRs(cidrs); err != nil {
			return fmt.Errorf("failed configuring allowed CIDRs: %w", err)
		}
	}

	if tlsCert != "" && tlsKey != "" {
		tlsCfg, err := signd.LoadServerTLSConfig(tlsCert, tlsKey, clientCA)
		if err != nil {
			return fmt.Errorf("failed configuring TLS: %w", err)
		}
		server.SetTLSConfig(tlsCfg)
	}

	return nil
}

func main() {
	var (
		listenAddr string
		passphrase string
		allowCIDRs string
		tlsCert    string
		tlsKey     string
		clientCA   string
		obsName    string
		keyDays    int
		keyBits    int
	)

	flag.StringVar(&listenAddr, "listen", "0.0.0.0:5167", "Network address to listen on")
	flag.StringVar(&passphrase, "passphrase", "", "Master key passphrase (defaults to SIGNER_MASTER_KEY env)")
	flag.StringVar(&allowCIDRs, "allow-cidrs", "", "Comma-separated list of allowed client IP addresses or CIDR subnets")
	flag.StringVar(&tlsCert, "tls-cert", "", "Path to TLS server certificate PEM file")
	flag.StringVar(&tlsKey, "tls-key", "", "Path to TLS server private key PEM file")
	flag.StringVar(&clientCA, "client-ca", "", "Path to client CA certificate PEM file for mTLS authentication")
	flag.StringVar(&obsName, "obs-name", "bsgo", "Instance name used as the domain of key user IDs: <project> OBS Project <project@NAME>")
	flag.IntVar(&keyDays, "key-days", 800, "Default lifetime in days of generated project keys")
	flag.IntVar(&keyBits, "key-bits", 4096, "RSA key size of generated project keys")
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
	engine.SetKeyParams(signd.KeyParams{ObsName: obsName, DefaultDays: keyDays, RSABits: keyBits})
	server := signd.NewServer(engine)

	if err := configureSecurity(server, allowCIDRs, tlsCert, tlsKey, clientCA); err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}

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
