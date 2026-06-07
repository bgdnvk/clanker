package maker

import (
	"encoding/base64"
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

func TestMaybeGenerateEC2UserDataGeneratesSREWhenFlagFollowedByFlag(t *testing.T) {
	args := []string{
		"ec2", "run-instances",
		"--image-id", "resolve:ssm:/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64",
		"--instance-type", "t4g.nano",
		"--iam-instance-profile", "Name=clanker-sre-observer",
		"--subnet-id", "subnet-0e871857b30184b52",
		"--security-group-ids", "sg-0dd1c8d59ccce43c0",
		"--user-data",
		"--metadata-options", "HttpTokens=required,HttpEndpoint=enabled",
		"--query", "Instances[0].InstanceId",
		"--output", "text",
	}
	bindings := map[string]string{
		"CLANKER_SRE_DEPLOY_ID": "sre-aws-test",
		"CLANKER_SRE_PROVIDER":  "aws",
	}

	out := maybeGenerateEC2UserData(args, bindings, ExecOptions{Region: "us-east-2"})
	loc := findEC2UserDataArg(out)
	if !loc.hasUsableValue {
		t.Fatalf("expected generated user-data value, got args: %v", out)
	}
	if strings.HasPrefix(strings.TrimSpace(loc.value), "--") {
		t.Fatalf("user-data still points at another flag: %v", out)
	}
	if got := flagValue(out, "--metadata-options"); got != "HttpTokens=required,HttpEndpoint=enabled" {
		t.Fatalf("metadata options not preserved, got %q in %v", got, out)
	}
	decoded, ok := decodeLikelyBase64UserData(loc.value)
	if !ok {
		raw, err := base64.StdEncoding.DecodeString(loc.value)
		if err != nil {
			t.Fatalf("generated user-data is not base64: %v", err)
		}
		decoded = string(raw)
	}
	for _, want := range []string{"docker run", "sre run --sre", "/clanker/sre/sre-aws-test/"} {
		if !strings.Contains(decoded, want) {
			t.Fatalf("generated SRE user-data missing %q:\n%s", want, decoded)
		}
	}
	if err := ValidateArgsNoConsecutiveFlags(out); err != nil {
		t.Fatalf("generated args failed validation: %v\n%v", err, out)
	}
}

func TestSanitizeRunInstancesNormalizesPlaceholderSubnetFlag(t *testing.T) {
	args := []string{
		"ec2", "run-instances",
		"--image-id", "ami-123",
		"--instance-type", "t4g.nano",
		"--subnet-0e871857b30184b52", "subnet-0e871857b30184b52",
	}

	out := sanitizeCommandArgsForExecution(args, map[string]string{})
	if got := flagValue(out, "--subnet-id"); got != "subnet-0e871857b30184b52" {
		t.Fatalf("subnet flag was not normalized, got %q args=%v", got, out)
	}
	if strings.Contains(strings.Join(out, " "), "--subnet-0e871857b30184b52") {
		t.Fatalf("malformed subnet flag still present: %v", out)
	}
}

func TestLearnPlanBindingsRunInstancesTextOutput(t *testing.T) {
	bindings := map[string]string{}
	args := []string{
		"ec2", "run-instances",
		"--query", "Instances[0].InstanceId",
		"--output", "text",
	}

	learnPlanBindings(args, "i-063c4bd0092250469\n", bindings, 10)
	if got := bindings["INSTANCE_ID"]; got != "i-063c4bd0092250469" {
		t.Fatalf("INSTANCE_ID = %q, want real run-instances text output", got)
	}
}

func TestLearnPlanBindingsFromProducesTextChoosesCompatibleToken(t *testing.T) {
	bindings := map[string]string{}
	learnPlanBindingsFromProduces(map[string]string{"INSTANCE_ID": "$"}, "i-0ba0559e0f90bee9c\ti-063c4bd0092250469", bindings)
	if got := bindings["INSTANCE_ID"]; got != "i-0ba0559e0f90bee9c" {
		t.Fatalf("INSTANCE_ID = %q, want first compatible EC2 id", got)
	}
}
