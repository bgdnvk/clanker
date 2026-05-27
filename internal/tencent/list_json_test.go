package tencent

import (
	"context"
	"strings"
	"testing"
)

// TestEmitTypedJSON_DispatchRejectsUnknownTypes verifies the dispatch
// table errors out clearly on a resource-type the caller mistyped.
// We can't easily exercise the JSON* methods themselves without a real
// Tencent client + SDK roundtrip, but we can pin the dispatch arms.
func TestEmitTypedJSON_DispatchRejectsUnknownTypes(t *testing.T) {
	// nil client is safe here because we expect to short-circuit on
	// the unknown-type case before any method dispatch.
	_, err := emitTypedJSON(context.Background(), nil, "definitely-not-a-real-type")
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
	if !strings.Contains(err.Error(), "unknown resource type") {
		t.Errorf("error should mention 'unknown resource type', got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-type") {
		t.Errorf("error should echo the bad value for debuggability, got %q", err.Error())
	}
}

// TestEmitTypedJSON_SubnetReturnsHelpfulMessage verifies that the
// subnets case — which doesn't have a dedicated JSON method (it's
// embedded in JSONVPCs) — returns a guidance message rather than a
// generic "unknown type" so the user knows where to look.
func TestEmitTypedJSON_SubnetReturnsHelpfulMessage(t *testing.T) {
	_, err := emitTypedJSON(context.Background(), nil, "subnets")
	if err == nil {
		t.Fatal("expected error for subnets type")
	}
	if !strings.Contains(err.Error(), "tencent list vpc --format json") {
		t.Errorf("error should redirect the user to `vpc --format json`, got %q", err.Error())
	}
}
