package template

import (
	"os"
	"testing"

	"kvm_console/config"
)

// TestMain ensures config.GlobalConfig is initialized (config.Init populates it,
// rather than a package-level var) so the pure-helper tests can read
// LXCBasePrefix / LXCLxcPath / etc. without depending on a running server.
func TestMain(m *testing.M) {
	config.Init()
	os.Exit(m.Run())
}
