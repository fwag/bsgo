// Command bsgo-sign-helper is the gpg stand-in that rpmsign runs to sign RPMs.
// It is not meant to be run by hand: the signing stage points rpm's
// %__gpg_sign_cmd at it and passes the project, the encrypted key blob and the
// bsgo-signd address in the environment. It hashes the document locally and
// asks bsgo-signd to sign the digest; keys never reach this process in the
// clear. See docs/specs/05-signd-protocol.md.
//
// Usage (as rpm invokes gpg):
//
//	bsgo-sign-helper [--no-armor ...] [--digest-algo=sha256] -sbo SIGFILE [-- PLAINTEXT]
//
// Environment: BSGO_SIGND_ADDR, BSGO_SIGN_PROJECT, BSGO_SIGN_ENCKEY,
// BSGO_SIGN_CREATED (unix time of the signature), optional BSGO_SIGN_FLAVOR and
// BSGO_SIGND_TLS_CERT, BSGO_SIGND_TLS_KEY, BSGO_SIGND_SERVER_CA.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/openSUSE/bsgo/pkg/signing"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := signing.RunHelper(ctx, os.Args[1:], os.Getenv, os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "bsgo-sign-helper: %v\n", err)
		stop()
		os.Exit(2)
	}
}
