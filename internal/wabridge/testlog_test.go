package wabridge

import waLog "go.mau.fi/whatsmeow/util/log"

// testLogger returns a quiet logger for tests.
func testLogger() waLog.Logger {
	return waLog.Noop
}
