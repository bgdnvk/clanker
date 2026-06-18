package flyio

import "testing"

func TestParseFlyioAppsEnvelopeAndBareArray(t *testing.T) {
	for _, body := range []string{
		`{"apps":[{"name":"api"}]}`,
		`[{"name":"worker"}]`,
	} {
		apps, err := parseFlyioApps(body)
		if err != nil {
			t.Fatalf("parse apps: %v", err)
		}
		if len(apps) != 1 || apps[0].Name == "" {
			t.Fatalf("unexpected apps for %s: %#v", body, apps)
		}
	}
}
