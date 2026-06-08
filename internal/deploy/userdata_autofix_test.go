package deploy

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/bgdnvk/clanker/internal/maker"
)

func TestGenerateMissingUserDataDoesNotTreatSecretAsECR(t *testing.T) {
	plan := &maker.Plan{
		Provider: "aws",
		Question: "Launch a tiny observer and read secrets from SSM",
		Commands: []maker.Command{
			{Args: []string{"ec2", "run-instances", "--image-id", "ami-123", "--instance-type", "t4g.nano"}},
			{Args: []string{"ssm", "send-command", "--comment", "Read secret runtime settings"}},
		},
	}

	patched := GenerateMissingUserData(plan, nil)
	if hasUserDataFlag(patched.Commands[0].Args) {
		t.Fatalf("secret text should not trigger ECR Docker user-data generation: %#v", patched.Commands[0].Args)
	}
}

func TestGenerateMissingUserDataAddsForRealECRPlan(t *testing.T) {
	plan := &maker.Plan{
		Provider: "aws",
		Question: "Deploy app to EC2 from ECR",
		Commands: []maker.Command{
			{Args: []string{"ecr", "create-repository", "--repository-name", "app"}},
			{Args: []string{"ec2", "run-instances", "--image-id", "ami-123", "--instance-type", "t3.small"}},
		},
	}

	patched := GenerateMissingUserData(plan, nil)
	if !hasUserDataFlag(patched.Commands[1].Args) {
		t.Fatalf("expected ECR app plan to receive Docker user-data: %#v", patched.Commands[1].Args)
	}
}

func TestGenerateMissingUserDataAddsSREObserverBootstrapOnly(t *testing.T) {
	plan := &maker.Plan{
		Provider: "aws",
		Question: "[one-click SRE deploy objective] deploy Clanker SRE observer",
		Commands: []maker.Command{
			{Args: []string{"ec2", "run-instances", "--image-id", "ami-123", "--instance-type", "t4g.nano"}},
			{Args: []string{"ssm", "send-command", "--parameters", `{"commands":["docker run ghcr.io/bgdnvk/clanker:latest clanker sre run --sre"]}`}},
		},
	}

	patched := GenerateMissingUserData(plan, nil)
	userData := userDataValue(patched.Commands[0].Args)
	if strings.TrimSpace(userData) == "" {
		t.Fatalf("expected SRE observer user-data bootstrap")
	}
	decodedBytes, err := base64.StdEncoding.DecodeString(userData)
	if err != nil {
		t.Fatalf("expected generated SRE user-data to be base64 encoded")
	}
	decoded := string(decodedBytes)
	lower := strings.ToLower(decoded)
	if !strings.Contains(lower, "clanker-sre") {
		t.Fatalf("expected SRE bootstrap marker, got: %s", decoded)
	}
	for _, forbidden := range []string{".dkr.ecr.", "imageuri", "no image found", "clanker-app"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("SRE bootstrap contains app deploy marker %q: %s", forbidden, decoded)
		}
	}
}

func TestGenerateMissingUserDataAddsSREObserverBootstrapBeforeNextFlag(t *testing.T) {
	plan := &maker.Plan{
		Provider: "aws",
		Question: "[one-click SRE deploy objective] deploy Clanker SRE observer",
		Commands: []maker.Command{
			{Args: []string{
				"ec2", "run-instances",
				"--image-id", "ami-123",
				"--instance-type", "t4g.nano",
				"--user-data",
				"--metadata-options", "HttpTokens=required,HttpEndpoint=enabled",
			}},
		},
	}

	patched := GenerateMissingUserData(plan, nil)
	args := patched.Commands[0].Args
	userData := userDataValue(args)
	if strings.TrimSpace(userData) == "" || strings.HasPrefix(strings.TrimSpace(userData), "--") {
		t.Fatalf("expected generated user-data before metadata flag, args=%#v", args)
	}
	if got := valueAfter(args, "--metadata-options"); got != "HttpTokens=required,HttpEndpoint=enabled" {
		t.Fatalf("metadata options not preserved, got %q args=%#v", got, args)
	}
}

func hasUserDataFlag(args []string) bool {
	return strings.TrimSpace(userDataValue(args)) != ""
}

func userDataValue(args []string) string {
	for i, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "--user-data" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--user-data=") {
			return strings.TrimPrefix(arg, "--user-data=")
		}
	}
	return ""
}

func valueAfter(args []string, flag string) string {
	for i, arg := range args {
		if strings.TrimSpace(arg) == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
