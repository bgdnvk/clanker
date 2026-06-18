package vercel

import (
	"strings"
	"testing"
)

func TestParseVercelDeploymentsEnvelope(t *testing.T) {
	deployments, err := parseVercelDeployments(`{"deployments":[{"uid":"dep_1","name":"api","state":"READY"}]}`)
	if err != nil {
		t.Fatalf("parse deployments: %v", err)
	}
	if len(deployments) != 1 || deployments[0].UID != "dep_1" || deployments[0].Name != "api" {
		t.Fatalf("unexpected deployments: %#v", deployments)
	}
}

func TestFormatVercelDeploymentEvents(t *testing.T) {
	got := formatVercelDeploymentEvents(`{"events":[{"type":"stdout","created":1710000000000,"payload":{"text":"api error"}}]}`, 5)
	for _, want := range []string{"2024-03-09T16:00:00Z", "stdout", "api error"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted events missing %q: %s", want, got)
		}
	}
}
