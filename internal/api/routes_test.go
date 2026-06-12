package api

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TestWriteTencentClientErr_NoRecursion is the regression test for the
// infinite-recursion bug rafeegnash caught in PR #165 review. Before the
// fix, the catch-all branch of writeTencentClientErr called itself
// instead of writeError — the first credential-missing request crashed
// the process with `fatal error: stack overflow`. This test exercises a
// Tencent handler with no credentials set and asserts we get a clean 401
// envelope back.
func TestWriteTencentClientErr_NoRecursion(t *testing.T) {
	// Scrub every credential surface tencent.ResolveCredentials reads so
	// the path that previously recursed is hit.
	scrubTencentCreds(t)

	srv := New(Config{Token: "test-token", Insecure: false}, log.New(io.Discard, "", 0))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tencent/regions", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"tencent_credentials"`) {
		t.Errorf("expected error code 'tencent_credentials' in body, got %q", body)
	}
}

// TestWriteTencentClientErr_InvalidRegion locks in the other branch — a
// validation failure should return 400 with `invalid_region`, not the
// catch-all 401. Cheap to assert and exercises the typed errors.As path.
func TestWriteTencentClientErr_InvalidRegion(t *testing.T) {
	// Set fake creds so we get past tencent.NewClient and reach the
	// region validator inside tencentClient.
	scrubTencentCreds(t)
	t.Setenv("TENCENTCLOUD_SECRET_ID", "fake-id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "fake-key")
	t.Setenv("TENCENTCLOUD_REGION", "ap-singapore")

	srv := New(Config{Token: "test-token", Insecure: false}, log.New(io.Discard, "", 0))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tencent/regions?region=evil%20region", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"invalid_region"`) {
		t.Errorf("expected error code 'invalid_region' in body, got %q", body)
	}
}

// TestWriteTencentClientErr_Direct asserts the helper itself returns the
// right shape for each branch without going through a handler — the
// minimum the recursion fix had to satisfy.
func TestWriteTencentClientErr_Direct(t *testing.T) {
	t.Run("invalid region branch", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeTencentClientErr(rec, &errInvalidRegion{value: "bad"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid-region: want 400, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"invalid_region"`) {
			t.Errorf("missing invalid_region code: %s", rec.Body.String())
		}
	})
	t.Run("catch-all branch", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeTencentClientErr(rec, errors.New("creds missing"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("catch-all: want 401, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"tencent_credentials"`) {
			t.Errorf("missing tencent_credentials code: %s", rec.Body.String())
		}
	})
	t.Run("wrapped invalid region survives errors.As", func(t *testing.T) {
		// tencentClient wraps errInvalidRegion with %w — the typed As
		// path needs to keep working through the wrap.
		rec := httptest.NewRecorder()
		wrapped := fmt.Errorf("client init: %w", &errInvalidRegion{value: "bad"})
		writeTencentClientErr(rec, wrapped)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("wrapped invalid-region: want 400, got %d", rec.Code)
		}
	})
}

func TestWriteRawDataRejectsInvalidJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeRawData(rec, `{"ok":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d body=%q", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"data":{"ok":true}`) {
			t.Fatalf("unexpected body: %q", rec.Body.String())
		}
	})

	t.Run("invalid", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeRawData(rec, `<script>alert(1)</script>`)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("want 502, got %d body=%q", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"invalid_json"`) {
			t.Fatalf("missing invalid_json code: %q", rec.Body.String())
		}
	})
}

// scrubTencentCreds wipes every credential surface ResolveCredentials reads.
// Restoration is automatic via t.Setenv / viper.Reset, but we still need to
// strip values that may be present in the developer's shell env.
func scrubTencentCreds(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"TENCENTCLOUD_SECRET_ID", "TENCENT_SECRET_ID",
		"TENCENTCLOUD_SECRET_KEY", "TENCENT_SECRET_KEY",
		"TENCENTCLOUD_REGION", "TENCENT_REGION",
	} {
		prev, had := os.LookupEnv(k)
		os.Unsetenv(k)
		if had {
			t.Cleanup(func() { os.Setenv(k, prev) })
		}
	}
	// viper.GetString reads in-memory configuration — clear the keys
	// ResolveCredentials looks at so this test isn't perturbed by a
	// previous test that called viper.Set.
	for _, k := range []string{"tencent.secret_id", "tencent.secret_key", "tencent.region"} {
		viper.Set(k, "")
	}
	t.Cleanup(func() {
		for _, k := range []string{"tencent.secret_id", "tencent.secret_key", "tencent.region"} {
			viper.Set(k, "")
		}
	})
}
