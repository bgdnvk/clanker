package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type ToolGuide struct {
	ID              string              `json:"id"`
	Tool            string              `json:"tool"`
	Binary          string              `json:"binary"`
	Providers       []string            `json:"providers,omitempty"`
	VerifyCommand   string              `json:"verifyCommand"`
	InstallCommands map[string][]string `json:"installCommands"`
	DocsURL         string              `json:"docsUrl,omitempty"`
}

type ToolStatus struct {
	ID              string              `json:"id"`
	Tool            string              `json:"tool"`
	Binary          string              `json:"binary"`
	Providers       []string            `json:"providers,omitempty"`
	Installed       bool                `json:"installed"`
	Path            string              `json:"path,omitempty"`
	Version         string              `json:"version,omitempty"`
	VerifyCommand   string              `json:"verifyCommand"`
	InstallCommands []string            `json:"installCommands,omitempty"`
	AllCommands     map[string][]string `json:"allPlatformInstallCommands,omitempty"`
	DocsURL         string              `json:"docsUrl,omitempty"`
	Message         string              `json:"message,omitempty"`
}

type ProviderStatus struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Wanted         bool     `json:"wanted"`
	Detected       bool     `json:"detected"`
	Configured     bool     `json:"configured"`
	RequiredTools  []string `json:"requiredTools"`
	MissingTools   []string `json:"missingTools,omitempty"`
	Ready          bool     `json:"ready"`
	DetectionNotes []string `json:"detectionNotes,omitempty"`
}

type AuthGuide struct {
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	Purpose       string   `json:"purpose,omitempty"`
	LoginCommands []string `json:"loginCommands,omitempty"`
	EnvVars       []string `json:"envVars,omitempty"`
	DocsURL       string   `json:"docsUrl,omitempty"`
	TokenURL      string   `json:"tokenUrl,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

type ScanOptions struct {
	WantedProviders []string
}

type ScanResult struct {
	OK                bool                  `json:"ok"`
	GeneratedAt       string                `json:"generatedAt"`
	OS                string                `json:"os"`
	Arch              string                `json:"arch"`
	Providers         []ProviderStatus      `json:"providers"`
	Tools             map[string]ToolStatus `json:"tools"`
	MissingTools      []ToolStatus          `json:"missingTools"`
	RecommendedTools  []string              `json:"recommendedTools"`
	AuthGuides        map[string]AuthGuide  `json:"authGuides"`
	AgentInstructions string                `json:"agentInstructions"`
	InstallHint       string                `json:"installHint,omitempty"`
}

type InstallOptions struct {
	Tools           []string
	WantedProviders []string
	DryRun          bool
	AssumeYes       bool
	Timeout         time.Duration
}

type InstallStepResult struct {
	Command string `json:"command"`
	Skipped bool   `json:"skipped,omitempty"`
	OK      bool   `json:"ok"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

type InstallToolResult struct {
	ID        string              `json:"id"`
	Tool      string              `json:"tool"`
	Binary    string              `json:"binary"`
	Installed bool                `json:"installed"`
	DryRun    bool                `json:"dryRun,omitempty"`
	Commands  []string            `json:"commands,omitempty"`
	Steps     []InstallStepResult `json:"steps,omitempty"`
	Error     string              `json:"error,omitempty"`
}

type InstallResult struct {
	OK       bool                `json:"ok"`
	OS       string              `json:"os"`
	Arch     string              `json:"arch"`
	Results  []InstallToolResult `json:"results"`
	NextScan *ScanResult         `json:"nextScan,omitempty"`
	Message  string              `json:"message,omitempty"`
}

type providerGuide struct {
	ID            string
	Name          string
	RequiredTools []string
	Detect        func() (configured bool, detected bool, notes []string)
}

func Guides() map[string]ToolGuide {
	return map[string]ToolGuide{
		"aws": {
			ID:            "aws",
			Tool:          "AWS CLI",
			Binary:        "aws",
			Providers:     []string{"AWS", "EKS"},
			VerifyCommand: "aws --version",
			InstallCommands: map[string][]string{
				"darwin":  {"curl \"https://awscli.amazonaws.com/AWSCLIV2.pkg\" -o \"AWSCLIV2.pkg\"", "sudo installer -pkg AWSCLIV2.pkg -target /"},
				"linux":   {"curl \"https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip\" -o \"awscliv2.zip\"", "unzip -o awscliv2.zip", "sudo ./aws/install --update"},
				"windows": {"winget install Amazon.AWSCLI"},
			},
			DocsURL: "https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html",
		},
		"gcloud": {
			ID:            "gcloud",
			Tool:          "Google Cloud CLI",
			Binary:        "gcloud",
			Providers:     []string{"GCP", "GKE"},
			VerifyCommand: "gcloud version",
			InstallCommands: map[string][]string{
				"darwin":  {"curl https://sdk.cloud.google.com | bash"},
				"linux":   {"curl https://sdk.cloud.google.com | bash"},
				"windows": {"winget install Google.CloudSDK"},
			},
			DocsURL: "https://docs.cloud.google.com/sdk/docs/install-sdk",
		},
		"az": {
			ID:            "az",
			Tool:          "Azure CLI",
			Binary:        "az",
			Providers:     []string{"Azure", "AKS"},
			VerifyCommand: "az version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install azure-cli"},
				"linux":   {"curl -sL https://aka.ms/InstallAzureCLIDeb | sudo bash"},
				"windows": {"winget install Microsoft.AzureCLI"},
			},
			DocsURL: "https://learn.microsoft.com/en-us/cli/azure/install-azure-cli?view=azure-cli-latest",
		},
		"wrangler": {
			ID:            "wrangler",
			Tool:          "Wrangler",
			Binary:        "wrangler",
			Providers:     []string{"Cloudflare"},
			VerifyCommand: "wrangler --version",
			InstallCommands: map[string][]string{
				"darwin":  {"npm install -g wrangler"},
				"linux":   {"npm install -g wrangler"},
				"windows": {"npm install -g wrangler"},
			},
			DocsURL: "https://developers.cloudflare.com/workers/wrangler/install-and-update/",
		},
		"doctl": {
			ID:            "doctl",
			Tool:          "DigitalOcean CLI",
			Binary:        "doctl",
			Providers:     []string{"DigitalOcean", "DOKS"},
			VerifyCommand: "doctl version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install doctl"},
				"linux":   {"snap install doctl"},
				"windows": {"winget install DigitalOcean.Doctl"},
			},
			DocsURL: "https://docs.digitalocean.com/reference/doctl/how-to/install/",
		},
		"hcloud": {
			ID:            "hcloud",
			Tool:          "Hetzner Cloud CLI",
			Binary:        "hcloud",
			Providers:     []string{"Hetzner"},
			VerifyCommand: "hcloud version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install hcloud"},
				"linux":   {"wget -q https://github.com/hetznercloud/cli/releases/latest/download/hcloud-linux-amd64.tar.gz -O hcloud.tar.gz", "tar -xzf hcloud.tar.gz", "sudo install -m 0755 hcloud /usr/local/bin/hcloud"},
				"windows": {"winget install HetznerCloud.CLI"},
			},
			DocsURL: "https://github.com/hetznercloud/cli",
		},
		"kubectl": {
			ID:            "kubectl",
			Tool:          "kubectl",
			Binary:        "kubectl",
			Providers:     []string{"Kubernetes", "EKS", "GKE", "AKS"},
			VerifyCommand: "kubectl version --client",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install kubectl"},
				"linux":   {"curl -LO https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl", "sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl"},
				"windows": {"winget install Kubernetes.kubectl"},
			},
			DocsURL: "https://kubernetes.io/docs/tasks/tools/",
		},
		"gh": {
			ID:            "gh",
			Tool:          "GitHub CLI",
			Binary:        "gh",
			Providers:     []string{"GitHub", "GitHub Models"},
			VerifyCommand: "gh --version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install gh"},
				"linux":   {"type -p curl >/dev/null || sudo apt-get install curl -y", "curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg", "echo \"deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\" | sudo tee /etc/apt/sources.list.d/github-cli.list >/dev/null", "sudo apt-get update && sudo apt-get install gh -y"},
				"windows": {"winget install GitHub.cli"},
			},
			DocsURL: "https://github.com/cli/cli#installation",
		},
		"terraform": {
			ID:            "terraform",
			Tool:          "Terraform",
			Binary:        "terraform",
			Providers:     []string{"Terraform", "Maker", "Deploy"},
			VerifyCommand: "terraform version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew tap hashicorp/tap", "brew install hashicorp/tap/terraform"},
				"linux":   {"sudo apt-get update && sudo apt-get install -y gnupg software-properties-common", "wget -O- https://apt.releases.hashicorp.com/gpg | gpg --dearmor | sudo tee /usr/share/keyrings/hashicorp-archive-keyring.gpg >/dev/null", "echo \"deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main\" | sudo tee /etc/apt/sources.list.d/hashicorp.list", "sudo apt-get update && sudo apt-get install -y terraform"},
				"windows": {"winget install Hashicorp.Terraform"},
			},
			DocsURL: "https://developer.hashicorp.com/terraform/install",
		},
		"opentofu": {
			ID:            "opentofu",
			Tool:          "OpenTofu",
			Binary:        "tofu",
			Providers:     []string{"OpenTofu", "Terraform", "Maker", "Deploy"},
			VerifyCommand: "tofu version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install opentofu"},
				"linux":   {"curl --proto '=https' --tlsv1.2 -fsSL https://get.opentofu.org/install-opentofu.sh | sh"},
				"windows": {"winget install OpenTofu.Tofu"},
			},
			DocsURL: "https://opentofu.org/docs/intro/install/",
		},
		"docker": {
			ID:            "docker",
			Tool:          "Docker",
			Binary:        "docker",
			Providers:     []string{"Docker", "Local builds"},
			VerifyCommand: "docker version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install --cask docker"},
				"linux":   {"curl -fsSL https://get.docker.com | sh"},
				"windows": {"winget install Docker.DockerDesktop"},
			},
			DocsURL: "https://docs.docker.com/get-docker/",
		},
		"railway": {
			ID:            "railway",
			Tool:          "Railway CLI",
			Binary:        "railway",
			Providers:     []string{"Railway"},
			VerifyCommand: "railway --version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install railway"},
				"linux":   {"npm install -g @railway/cli"},
				"windows": {"npm install -g @railway/cli"},
			},
			DocsURL: "https://docs.railway.com/cli",
		},
		"supabase": {
			ID:            "supabase",
			Tool:          "Supabase CLI",
			Binary:        "supabase",
			Providers:     []string{"Supabase"},
			VerifyCommand: "supabase --version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install supabase/tap/supabase"},
				"linux":   {"npm install -g supabase"},
				"windows": {"scoop bucket add supabase https://github.com/supabase/scoop-bucket.git", "scoop install supabase"},
			},
			DocsURL: "https://supabase.com/docs/guides/local-development/cli/getting-started",
		},
		"vercel": {
			ID:            "vercel",
			Tool:          "Vercel CLI",
			Binary:        "vercel",
			Providers:     []string{"Vercel"},
			VerifyCommand: "vercel --version",
			InstallCommands: map[string][]string{
				"darwin":  {"npm install -g vercel"},
				"linux":   {"npm install -g vercel"},
				"windows": {"npm install -g vercel"},
			},
			DocsURL: "https://vercel.com/docs/cli",
		},
	}
}

func Scan(ctx context.Context, opts ScanOptions) ScanResult {
	tools := scanTools(ctx)
	wanted := normalizeSet(opts.WantedProviders)
	providers := scanProviders(wanted, tools)
	recommended := recommendedToolIDs(providers)
	missingTools := make([]ToolStatus, 0)
	for _, id := range recommended {
		tool, ok := tools[id]
		if ok && !tool.Installed {
			missingTools = append(missingTools, tool)
		}
	}
	sort.Slice(missingTools, func(i, j int) bool { return missingTools[i].ID < missingTools[j].ID })
	authGuides := AuthGuides()
	return ScanResult{
		OK:                true,
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		OS:                runtime.GOOS,
		Arch:              runtime.GOARCH,
		Providers:         providers,
		Tools:             tools,
		MissingTools:      missingTools,
		RecommendedTools:  recommended,
		AuthGuides:        authGuides,
		AgentInstructions: BuildAgentInstructions(providers, missingTools, authGuides),
		InstallHint:       "Run clanker onboarding install --yes <tool> or use the official vendor install commands for your OS.",
	}
}

func Install(ctx context.Context, opts InstallOptions) InstallResult {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	scan := Scan(ctx, ScanOptions{WantedProviders: opts.WantedProviders})
	toolIDs := normalizeInstallTools(opts.Tools, scan)
	result := InstallResult{OK: true, OS: runtime.GOOS, Arch: runtime.GOARCH}
	if len(toolIDs) == 0 {
		result.Message = "No missing provider CLI tools selected."
		next := Scan(ctx, ScanOptions{WantedProviders: opts.WantedProviders})
		result.NextScan = &next
		return result
	}
	guides := Guides()
	for _, id := range toolIDs {
		guide, ok := guides[id]
		if !ok {
			result.OK = false
			result.Results = append(result.Results, InstallToolResult{ID: id, Error: "unknown tool"})
			continue
		}
		commands := installCommandsForOS(guide)
		toolResult := InstallToolResult{
			ID:       guide.ID,
			Tool:     guide.Tool,
			Binary:   guide.Binary,
			DryRun:   opts.DryRun,
			Commands: commands,
		}
		if len(commands) == 0 {
			toolResult.Error = "no install commands for " + runtime.GOOS
			result.OK = false
			result.Results = append(result.Results, toolResult)
			continue
		}
		if opts.DryRun {
			toolResult.Installed = scan.Tools[id].Installed
			result.Results = append(result.Results, toolResult)
			continue
		}
		if !opts.AssumeYes {
			toolResult.Error = "refusing to install without --yes"
			result.OK = false
			result.Results = append(result.Results, toolResult)
			continue
		}
		for _, command := range commands {
			stepCtx, cancel := context.WithTimeout(ctx, timeout)
			step := runInstallCommand(stepCtx, command)
			cancel()
			toolResult.Steps = append(toolResult.Steps, step)
			if !step.OK {
				toolResult.Error = step.Error
				result.OK = false
				break
			}
		}
		if _, err := exec.LookPath(guide.Binary); err == nil {
			toolResult.Installed = true
		}
		result.Results = append(result.Results, toolResult)
	}
	next := Scan(ctx, ScanOptions{WantedProviders: opts.WantedProviders})
	result.NextScan = &next
	return result
}

func MarshalPretty(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func BuildAgentInstructions(providers []ProviderStatus, missing []ToolStatus, authGuides map[string]AuthGuide) string {
	var b strings.Builder
	b.WriteString("Clanker Cloud onboarding task:\n")
	b.WriteString("1. Run `clanker onboarding scan --format json` on this machine.\n")
	b.WriteString("2. Ask the user which cloud providers they want Clanker Cloud to manage if no provider is detected or selected.\n")
	b.WriteString("3. For selected or detected providers, install only the missing provider CLIs with `clanker onboarding install --yes <tool>`.\n")
	b.WriteString("4. After installation, run `clanker onboarding scan --format json` again and help the user authenticate each provider CLI using only the official docs and token URLs from `authGuides`.\n")
	if len(missing) > 0 {
		ids := make([]string, 0, len(missing))
		for _, tool := range missing {
			ids = append(ids, tool.ID)
		}
		sort.Strings(ids)
		b.WriteString("\nMissing recommended tools now: " + strings.Join(ids, ", ") + ".\n")
	}
	detected := make([]string, 0)
	for _, provider := range providers {
		if provider.Wanted || provider.Detected || provider.Configured {
			detected = append(detected, provider.ID)
		}
	}
	if len(detected) > 0 {
		sort.Strings(detected)
		b.WriteString("Detected or selected providers: " + strings.Join(detected, ", ") + ".\n")
		for _, id := range detected {
			guide, ok := authGuides[id]
			if !ok {
				continue
			}
			b.WriteString("- " + guide.Provider + " auth: ")
			if len(guide.LoginCommands) > 0 {
				b.WriteString(strings.Join(guide.LoginCommands, " ; "))
			} else {
				b.WriteString(guide.Purpose)
			}
			if guide.DocsURL != "" {
				b.WriteString(" Docs: " + guide.DocsURL)
			}
			if guide.TokenURL != "" {
				b.WriteString(" Token/account: " + guide.TokenURL)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func AuthGuides() map[string]AuthGuide {
	return map[string]AuthGuide{
		"aws": {
			ID:            "aws",
			Provider:      "AWS",
			Purpose:       "Use an AWS CLI profile or SSO session. Prefer short-lived SSO when available; use IAM access keys only when your AWS account requires them.",
			LoginCommands: []string{"aws configure sso", "aws sso login --profile <profile>", "aws configure --profile <profile>"},
			EnvVars:       []string{"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"},
			DocsURL:       "https://docs.aws.amazon.com/cli/latest/userguide/getting-started-quickstart.html",
			TokenURL:      "https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_access-keys.html",
			Notes:         []string{"Clanker reads local AWS profiles; do not paste AWS secrets into chat."},
		},
		"gcp": {
			ID:            "gcp",
			Provider:      "Google Cloud",
			Purpose:       "Authenticate the gcloud CLI and Application Default Credentials, then select the project Clanker Cloud should inspect.",
			LoginCommands: []string{"gcloud auth login", "gcloud auth application-default login", "gcloud config set project <project-id>"},
			EnvVars:       []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT"},
			DocsURL:       "https://docs.cloud.google.com/sdk/docs/authenticate",
			TokenURL:      "https://docs.cloud.google.com/docs/authentication/provide-credentials-adc",
		},
		"azure": {
			ID:            "azure",
			Provider:      "Azure",
			Purpose:       "Sign in with Azure CLI and select the subscription Clanker Cloud should inspect.",
			LoginCommands: []string{"az login", "az account set --subscription <subscription-id>"},
			EnvVars:       []string{"AZURE_SUBSCRIPTION_ID", "AZURE_TENANT_ID", "AZURE_CLIENT_ID"},
			DocsURL:       "https://learn.microsoft.com/en-us/cli/azure/authenticate-azure-cli-interactively?view=azure-cli-latest",
		},
		"cloudflare": {
			ID:            "cloudflare",
			Provider:      "Cloudflare",
			Purpose:       "Use Wrangler login for browser auth or create a scoped Cloudflare API token in the Cloudflare dashboard.",
			LoginCommands: []string{"wrangler login"},
			EnvVars:       []string{"CLOUDFLARE_API_TOKEN", "CF_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"},
			DocsURL:       "https://developers.cloudflare.com/workers/wrangler/",
			TokenURL:      "https://developers.cloudflare.com/fundamentals/api/get-started/create-token/",
		},
		"digitalocean": {
			ID:            "digitalocean",
			Provider:      "DigitalOcean",
			Purpose:       "Create a scoped DigitalOcean personal access token, then initialize doctl or save the token locally.",
			LoginCommands: []string{"doctl auth init"},
			EnvVars:       []string{"DIGITALOCEAN_ACCESS_TOKEN", "DO_API_TOKEN"},
			DocsURL:       "https://docs.digitalocean.com/reference/doctl/how-to/install/",
			TokenURL:      "https://docs.digitalocean.com/reference/api/create-personal-access-token/",
		},
		"hetzner": {
			ID:            "hetzner",
			Provider:      "Hetzner",
			Purpose:       "Create a Hetzner Cloud API token and save it with hcloud or HCLOUD_TOKEN.",
			LoginCommands: []string{"hcloud context create <name>"},
			EnvVars:       []string{"HCLOUD_TOKEN", "HETZNER_API_TOKEN"},
			DocsURL:       "https://github.com/hetznercloud/cli",
		},
		"kubernetes": {
			ID:            "kubernetes",
			Provider:      "Kubernetes",
			Purpose:       "Use your cloud provider CLI to write kubeconfig, or point KUBECONFIG at an existing cluster config.",
			LoginCommands: []string{"aws eks update-kubeconfig --name <cluster> --region <region> --profile <profile>", "gcloud container clusters get-credentials <cluster> --region <region> --project <project-id>", "az aks get-credentials --resource-group <group> --name <cluster>"},
			EnvVars:       []string{"KUBECONFIG"},
			DocsURL:       "https://kubernetes.io/docs/tasks/access-application-cluster/configure-access-multiple-clusters/",
		},
		"github": {
			ID:            "github",
			Provider:      "GitHub",
			Purpose:       "Use GitHub CLI browser login for repo and Models/Copilot features, or create a scoped GitHub token when automation needs one.",
			LoginCommands: []string{"gh auth login"},
			EnvVars:       []string{"GITHUB_TOKEN", "GH_TOKEN"},
			DocsURL:       "https://cli.github.com/manual/gh_auth_login",
			TokenURL:      "https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens",
		},
		"railway": {
			ID:            "railway",
			Provider:      "Railway",
			Purpose:       "Use Railway CLI browser login for local work, or create an account/workspace token from Railway settings for agent or CI use.",
			LoginCommands: []string{"railway login", "railway whoami"},
			EnvVars:       []string{"RAILWAY_API_TOKEN", "RAILWAY_TOKEN"},
			DocsURL:       "https://docs.railway.com/cli",
			TokenURL:      "https://docs.railway.com/integrations/api",
		},
		"supabase": {
			ID:            "supabase",
			Provider:      "Supabase",
			Purpose:       "Use a Supabase personal access token for CLI and Management API access.",
			LoginCommands: []string{"supabase login"},
			EnvVars:       []string{"SUPABASE_ACCESS_TOKEN"},
			DocsURL:       "https://supabase.com/docs/reference/cli/introduction",
			TokenURL:      "https://supabase.com/docs/reference/api/introduction",
		},
		"vercel": {
			ID:            "vercel",
			Provider:      "Vercel",
			Purpose:       "Use Vercel CLI browser login or a Vercel token for deployments and project inventory.",
			LoginCommands: []string{"vercel login", "vercel whoami"},
			EnvVars:       []string{"VERCEL_TOKEN"},
			DocsURL:       "https://vercel.com/docs/cli",
			TokenURL:      "https://vercel.com/docs/rest-api#authentication",
		},
	}
}

func scanTools(ctx context.Context) map[string]ToolStatus {
	guides := Guides()
	keys := make([]string, 0, len(guides))
	for key := range guides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]ToolStatus, len(guides))
	for _, key := range keys {
		guide := guides[key]
		status := ToolStatus{
			ID:              guide.ID,
			Tool:            guide.Tool,
			Binary:          guide.Binary,
			Providers:       append([]string(nil), guide.Providers...),
			VerifyCommand:   guide.VerifyCommand,
			InstallCommands: installCommandsForOS(guide),
			AllCommands:     guide.InstallCommands,
			DocsURL:         guide.DocsURL,
		}
		if path, err := exec.LookPath(guide.Binary); err == nil {
			status.Installed = true
			status.Path = path
			status.Version = detectVersion(ctx, guide)
		} else {
			status.Message = guide.Binary + " not found in PATH"
		}
		result[key] = status
	}
	return result
}

func scanProviders(wanted map[string]bool, tools map[string]ToolStatus) []ProviderStatus {
	guides := providerGuides()
	result := make([]ProviderStatus, 0, len(guides))
	for _, guide := range guides {
		configured, detected, notes := guide.Detect()
		missing := make([]string, 0)
		for _, toolID := range guide.RequiredTools {
			if tool, ok := tools[toolID]; ok && !tool.Installed {
				missing = append(missing, toolID)
			}
		}
		sort.Strings(missing)
		result = append(result, ProviderStatus{
			ID:             guide.ID,
			Name:           guide.Name,
			Wanted:         wanted[guide.ID],
			Detected:       detected,
			Configured:     configured,
			RequiredTools:  append([]string(nil), guide.RequiredTools...),
			MissingTools:   missing,
			Ready:          len(missing) == 0 && (wanted[guide.ID] || detected || configured),
			DetectionNotes: notes,
		})
	}
	return result
}

func providerGuides() []providerGuide {
	return []providerGuide{
		{ID: "aws", Name: "AWS", RequiredTools: []string{"aws"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN") {
				notes = append(notes, "aws env")
			}
			if fileExists(homePath(".aws", "credentials")) || fileExists(homePath(".aws", "config")) {
				notes = append(notes, "aws config")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "gcp", Name: "GCP", RequiredTools: []string{"gcloud"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "GOOGLE_APPLICATION_CREDENTIALS") {
				notes = append(notes, "gcp env")
			}
			if fileExists(homePath(".config", "gcloud", "application_default_credentials.json")) {
				notes = append(notes, "application default credentials")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "azure", Name: "Azure", RequiredTools: []string{"az"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("AZURE_SUBSCRIPTION_ID", "AZURE_TENANT_ID", "AZURE_CLIENT_ID") {
				notes = append(notes, "azure env")
			}
			if fileExists(homePath(".azure", "azureProfile.json")) {
				notes = append(notes, "azure profile")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "cloudflare", Name: "Cloudflare", RequiredTools: []string{"wrangler"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("CLOUDFLARE_API_TOKEN", "CF_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID") {
				notes = append(notes, "cloudflare env")
			}
			if fileExists(homePath(".wrangler", "config", "default.toml")) {
				notes = append(notes, "wrangler config")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "digitalocean", Name: "DigitalOcean", RequiredTools: []string{"doctl"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("DIGITALOCEAN_ACCESS_TOKEN", "DO_API_TOKEN") {
				notes = append(notes, "digitalocean env")
			}
			if fileExists(homePath(".config", "doctl", "config.yaml")) {
				notes = append(notes, "doctl config")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "hetzner", Name: "Hetzner", RequiredTools: []string{"hcloud"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("HCLOUD_TOKEN", "HETZNER_API_TOKEN") {
				notes = append(notes, "hetzner env")
			}
			if fileExists(homePath(".config", "hcloud", "cli.toml")) {
				notes = append(notes, "hcloud config")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "kubernetes", Name: "Kubernetes", RequiredTools: []string{"kubectl"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("KUBECONFIG") {
				notes = append(notes, "kubeconfig env")
			}
			if fileExists(homePath(".kube", "config")) {
				notes = append(notes, "kubeconfig")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "github", Name: "GitHub", RequiredTools: []string{"gh"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("GITHUB_TOKEN", "GH_TOKEN") {
				notes = append(notes, "github env")
			}
			if fileExists(homePath(".config", "gh", "hosts.yml")) {
				notes = append(notes, "gh hosts")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "railway", Name: "Railway", RequiredTools: []string{"railway"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("RAILWAY_API_TOKEN", "RAILWAY_TOKEN") {
				notes = append(notes, "railway env")
			}
			if fileExists(homePath(".railway", "config.json")) {
				notes = append(notes, "railway config")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "supabase", Name: "Supabase", RequiredTools: []string{"supabase"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("SUPABASE_ACCESS_TOKEN") {
				notes = append(notes, "supabase env")
			}
			if fileExists(homePath(".supabase", "access-token")) {
				notes = append(notes, "supabase access token")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "vercel", Name: "Vercel", RequiredTools: []string{"vercel"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("VERCEL_TOKEN") {
				notes = append(notes, "vercel env")
			}
			if fileExists(homePath(".vercel", "auth.json")) {
				notes = append(notes, "vercel auth")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "terraform", Name: "Terraform", RequiredTools: []string{"terraform"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if fileExists("main.tf") || fileExists("terraform.tfstate") || fileExists(".terraform.lock.hcl") {
				notes = append(notes, "terraform files")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
	}
}

func recommendedToolIDs(providers []ProviderStatus) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, provider := range providers {
		if !provider.Wanted && !provider.Detected && !provider.Configured {
			continue
		}
		for _, id := range provider.RequiredTools {
			if seen[id] {
				continue
			}
			seen[id] = true
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

func normalizeInstallTools(raw []string, scan ScanResult) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range raw {
		for _, part := range strings.Split(value, ",") {
			id := normalizeToolID(part)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			result = append(result, id)
		}
	}
	if len(result) == 0 {
		for _, tool := range scan.MissingTools {
			if !seen[tool.ID] {
				seen[tool.ID] = true
				result = append(result, tool.ID)
			}
		}
	}
	sort.Strings(result)
	return result
}

func normalizeToolID(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "awscli", "aws-cli":
		return "aws"
	case "google", "google-cloud", "google-cloud-cli":
		return "gcloud"
	case "azure", "azure-cli":
		return "az"
	case "cloudflare":
		return "wrangler"
	case "digitalocean", "digital-ocean":
		return "doctl"
	case "hetzner":
		return "hcloud"
	case "railway":
		return "railway"
	case "supabase":
		return "supabase"
	case "vercel":
		return "vercel"
	case "github", "github-cli":
		return "gh"
	default:
		return value
	}
}

func normalizeSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" {
				continue
			}
			result[part] = true
		}
	}
	return result
}

func detectVersion(ctx context.Context, guide ToolGuide) string {
	parts := strings.Fields(guide.VerifyCommand)
	if len(parts) == 0 {
		return ""
	}
	versionCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(versionCtx, parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func installCommandsForOS(guide ToolGuide) []string {
	if commands := guide.InstallCommands[runtime.GOOS]; len(commands) > 0 {
		return append([]string(nil), commands...)
	}
	return nil
}

func runInstallCommand(ctx context.Context, command string) InstallStepResult {
	result := InstallStepResult{Command: command}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command)
	default:
		cmd = exec.CommandContext(ctx, "sh", "-lc", command)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		result.Error = fmt.Sprintf("%v", err)
		if text := strings.TrimSpace(output.String()); text != "" {
			result.Output = truncate(text, 4000)
		}
		return result
	}
	result.OK = true
	result.Output = truncate(strings.TrimSpace(output.String()), 4000)
	return result
}

func hasAnyEnv(keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func homePath(parts ...string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(parts...)
	}
	all := append([]string{home}, parts...)
	return filepath.Join(all...)
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max]) + "..."
}
