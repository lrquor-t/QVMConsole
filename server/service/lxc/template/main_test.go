package template

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"kvm_console/config"
	"kvm_console/logger"
)

// TestMain ensures config.GlobalConfig is initialized (config.Init populates it,
// rather than a package-level var) so the pure-helper tests can read
// LXCBasePrefix / LXCLxcPath / etc. without depending on a running server.
//
// logger.App/CMD/etc. are normally created by logger.Init (which writes to disk
// via lumberjack); we don't want test runs to touch the filesystem, so point the
// loggers at io.Discard. This matters because utils.ExecCommand logs via
// logger.CMD, and tests exercising InspectRootfsTarball would otherwise panic
// on a nil logger.CMD/App.
func TestMain(m *testing.M) {
	config.Init()
	discard := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 100}))
	logger.App = discard
	logger.CMD = discard
	logger.Request = discard
	logger.Libvirt = discard
	os.Exit(m.Run())
}
