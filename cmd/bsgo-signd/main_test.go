package main

import (
	"testing"

	"github.com/openSUSE/bsgo/pkg/signd"
)

func TestConfigureSecurity(t *testing.T) {
	mk, _ := signd.NewMasterKey("test-pass")
	defer mk.Destroy()

	engine := signd.NewSignEngine(mk, nil)
	server := signd.NewServer(engine)

	// Valid CIDRs
	if err := configureSecurity(server, "127.0.0.1,10.0.0.0/8", "", "", ""); err != nil {
		t.Fatalf("expected valid CIDRs to succeed: %v", err)
	}

	// Invalid CIDR
	if err := configureSecurity(server, "not-an-ip", "", "", ""); err == nil {
		t.Fatalf("expected invalid CIDR to fail")
	}

	// Empty security settings
	if err := configureSecurity(server, "", "", "", ""); err != nil {
		t.Fatalf("expected empty security settings to succeed: %v", err)
	}
}
