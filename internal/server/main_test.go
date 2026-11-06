package server

import (
	"os"
	"testing"

	"mikrodash/internal/netguard"
	"mikrodash/internal/routeros"
)

// TestMain lets this package's fake routers, which listen on loopback, be
// dialled. The guard on that dial (GHSA-mcwr-rm79-fr4h) is proven where it
// lives, in internal/routeros.
func TestMain(m *testing.M) {
	routeros.SetDialReach(netguard.Any)
	os.Exit(m.Run())
}
