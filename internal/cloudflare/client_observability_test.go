package cloudflare

import "testing"

func TestParseAIGateways(t *testing.T) {
	for _, body := range []string{
		`{"result":[{"id":"gw-1","name":"prod"}]}`,
		`{"result":{"gateways":[{"id":"gw-2","name":"staging"}]}}`,
	} {
		gateways, err := parseAIGateways(body)
		if err != nil {
			t.Fatalf("parse gateways: %v", err)
		}
		if len(gateways) != 1 || gateways[0].ID == "" || gateways[0].Name == "" {
			t.Fatalf("unexpected gateways for %s: %#v", body, gateways)
		}
	}
}
