package maker

import (
	"strings"
	"testing"
)

func TestShouldAutoPrepareImageSkipsSREObserverPlan(t *testing.T) {
	args := []string{"ec2", "run-instances", "--image-id", "ami-123", "--instance-type", "t4g.nano"}
	question := "[one-click SRE deploy objective] deploy https://github.com/bgdnvk/clanker as a Clanker SRE observer"
	bindings := map[string]string{"PLAN_QUESTION": question}

	if shouldAutoPrepareImage(args, question, bindings, ExecOptions{Profile: "default", Region: "us-east-2"}) {
		t.Fatalf("SRE observer plan must not trigger app image preparation")
	}
}

func TestInjectEnvVarsAsInstanceTagsSkipsSecretEnvVars(t *testing.T) {
	args := []string{
		"ec2", "run-instances",
		"--tag-specifications",
		`[{"ResourceType":"instance","Tags":[{"Key":"Name","Value":"app"}]}]`,
	}
	bindings := map[string]string{
		"ENV_OPENAI_API_KEY": "sk-secret",
		"ENV_PUBLIC_FLAG":    "enabled",
		"IMAGE_URI":          "123456789012.dkr.ecr.us-east-2.amazonaws.com/app:latest",
	}

	out := injectEnvVarsAsInstanceTags(args, bindings)
	joined := strings.Join(out, " ")
	if strings.Contains(joined, "OPENAI_API_KEY") || strings.Contains(joined, "sk-secret") {
		t.Fatalf("secret env var leaked into EC2 tags: %s", joined)
	}
	if !strings.Contains(joined, "ENV_PUBLIC_FLAG") {
		t.Fatalf("non-secret env var should remain tag fallback: %s", joined)
	}
	if !strings.Contains(joined, "ImageUri") {
		t.Fatalf("app image tag should remain for non-SRE app deploy: %s", joined)
	}
}

func TestInjectEnvVarsAsInstanceTagsSkipsSREConfigAndAppTags(t *testing.T) {
	args := []string{
		"ec2", "run-instances",
		"--tag-specifications",
		`[{"ResourceType":"instance","Tags":[{"Key":"clanker-sre","Value":"true"}]}]`,
	}
	bindings := map[string]string{
		"CLANKER_SRE_DEPLOY_ID":            "sre-123",
		"ENV_CLANKER_SRE_DEPLOY_ID":        "sre-123",
		"ENV_CLANKER_CEREBRO_URL":          "https://example.invalid",
		"ENV_CLANKER_CEREBRO_INGEST_TOKEN": "token-value",
		"IMAGE_URI":                        "123456789012.dkr.ecr.us-east-2.amazonaws.com/app:latest",
		"APP_PORT":                         "8080",
	}

	out := injectEnvVarsAsInstanceTags(args, bindings)
	joined := strings.Join(out, " ")
	for _, forbidden := range []string{"ImageUri", "AppPort", "CLANKER_CEREBRO", "CLANKER_SRE_DEPLOY_ID", "token-value"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("SRE runtime/app config leaked into tags via %q: %s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "clanker-sre") {
		t.Fatalf("existing SRE metadata tag should be preserved: %s", joined)
	}
}

func TestSRESSMPathUsesDeployID(t *testing.T) {
	if got := sanitizeSSMPathSegment("sre/aws test:1"); got != "sre-aws-test-1" {
		t.Fatalf("unexpected sanitized SSM segment: %q", got)
	}
}
