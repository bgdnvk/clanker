package maker

import (
	"testing"

	"github.com/bgdnvk/clanker/internal/verda"
)

// verdaTestBaseURL redirects the verda package's REST base URL to the given
// test-server URL and returns the previous value so the caller can restore
// it via a defer. Wraps verda.SetBaseURLForTest to keep test files in this
// package from importing verda directly.
func verdaTestBaseURL(t *testing.T, url string) string {
	t.Helper()
	return verda.SetBaseURLForTest(url)
}
