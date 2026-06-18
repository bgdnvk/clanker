package digitalocean

import "testing"

func TestParseDigitalOceanApps(t *testing.T) {
	apps, err := parseDigitalOceanApps(`{"apps":[{"id":"app-1","spec":{"name":"api"}}]}`)
	if err != nil {
		t.Fatalf("parse apps: %v", err)
	}
	if len(apps) != 1 || apps[0].ID != "app-1" || apps[0].Name != "api" {
		t.Fatalf("unexpected apps: %#v", apps)
	}
}
