package chrome

import (
	"os"
	"testing"
)

// requireChromeE2E keeps host-browser tests out of the default unit/integration
// suite. Their result depends on the installed Chrome build and runner image,
// so they are run explicitly by the Browser E2E workflow instead.
func requireChromeE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("NEXBOX_CHROME_E2E") != "1" {
		t.Skip("set NEXBOX_CHROME_E2E=1 to run host Chrome end-to-end tests")
	}
}
