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
	Aliases         []string            `json:"aliases,omitempty"`
	Providers       []string            `json:"providers,omitempty"`
	VerifyCommand   string              `json:"verifyCommand"`
	InstallCommands map[string][]string `json:"installCommands"`
	DocsURL         string              `json:"docsUrl,omitempty"`
}

type ToolStatus struct {
	ID              string              `json:"id"`
	Tool            string              `json:"tool"`
	Binary          string              `json:"binary"`
	Aliases         []string            `json:"aliases,omitempty"`
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
		"oci": {
			ID:            "oci",
			Tool:          "Oracle Cloud Infrastructure CLI",
			Binary:        "oci",
			Providers:     []string{"Oracle Cloud", "OCI", "OKE"},
			VerifyCommand: "oci --version",
			InstallCommands: map[string][]string{
				"darwin":  {"bash -c \"$(curl -L https://raw.githubusercontent.com/oracle/oci-cli/master/scripts/install/install.sh)\""},
				"linux":   {"bash -c \"$(curl -L https://raw.githubusercontent.com/oracle/oci-cli/master/scripts/install/install.sh)\""},
				"windows": {"python -m pip install oci-cli"},
			},
			DocsURL: "https://docs.oracle.com/en-us/iaas/Content/API/SDKDocs/cliinstall.htm",
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
		"flyctl": {
			ID:            "flyctl",
			Tool:          "flyctl",
			Binary:        "fly",
			Providers:     []string{"Fly.io"},
			VerifyCommand: "fly version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install flyctl"},
				"linux":   {"curl -L https://fly.io/install.sh | sh"},
				"windows": {"powershell -NoProfile -ExecutionPolicy Bypass -Command \"iwr https://fly.io/install.ps1 -useb | iex\""},
			},
			DocsURL: "https://fly.io/docs/flyctl/install/",
		},
		"tccli": {
			ID:            "tccli",
			Tool:          "Tencent Cloud CLI",
			Binary:        "tccli",
			Providers:     []string{"Tencent Cloud"},
			VerifyCommand: "tccli --version",
			InstallCommands: map[string][]string{
				"darwin":  {"python3 -m pip install tccli-intl-en"},
				"linux":   {"python3 -m pip install tccli-intl-en"},
				"windows": {"py -m pip install tccli-intl-en"},
			},
			DocsURL: "https://www.tencentcloud.com/document/product/1013/33464",
		},
		"sentry-cli": {
			ID:            "sentry-cli",
			Tool:          "Sentry CLI",
			Binary:        "sentry-cli",
			Aliases:       []string{"sentry"},
			Providers:     []string{"Sentry"},
			VerifyCommand: "sentry-cli --version",
			InstallCommands: map[string][]string{
				"darwin":  {"brew install getsentry/tools/sentry-cli"},
				"linux":   {"curl -sL https://sentry.io/get-cli/ | bash"},
				"windows": {"npm install -g @sentry/cli"},
			},
			DocsURL: "https://docs.sentry.io/cli/installation/",
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
		if _, err := findToolGuideBinary(guide); err == nil {
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
	b.WriteString("3. If the user asked you to install or use Clanker Cloud, perform the install step instead of only reporting missing tools. In Clanker Cloud MCP, call `clanker_cloud_install_setup_dependencies` with `dependencySummary.recommendedMissingTools`; otherwise run `clanker onboarding install --yes <tool>` for selected or detected providers only.\n")
	b.WriteString("4. After installation, run `clanker onboarding scan --format json` again. If a tool still requires browser login, SSO, sudo, or an API token, tell the user exactly what you need and use only the official docs and token URLs from `authGuides`.\n")
	b.WriteString("5. Once auth is configured, start the Clanker Cloud infrastructure scan and wait for it before chat, Kubernetes, research, or security work. Scans can take several minutes; keep reporting that resources are still being scanned instead of failing early.\n")
	b.WriteString("6. For Kubernetes follow-up through the standalone Clanker CLI MCP, use `clanker_k8s_list_clusters` or `clanker_k8s_get_resources` for context and `clanker_k8s_ask_cluster` for natural-language cluster chat. Pass cluster/context/namespace/provider fields when the user names a specific cluster.\n")
	b.WriteString("7. For infrastructure changes, generate a plan first and apply it only after explicit user approval.\n")
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
		"oracle": {
			ID:            "oracle",
			Provider:      "Oracle Cloud Infrastructure",
			Purpose:       "Configure an OCI CLI profile with an API signing key, then provide the tenancy or compartment OCID Clanker should inspect.",
			LoginCommands: []string{"oci setup config", "oci iam compartment list --access-level ACCESSIBLE --compartment-id <tenancy-ocid> --compartment-id-in-subtree true --all"},
			EnvVars:       []string{"OCI_CLI_PROFILE", "OCI_CLI_CONFIG_FILE", "OCI_TENANCY_OCID", "OCI_COMPARTMENT_ID"},
			DocsURL:       "https://docs.oracle.com/en-us/iaas/Content/API/SDKDocs/cliconfigure.htm",
			TokenURL:      "https://docs.oracle.com/en-us/iaas/Content/API/Concepts/apisigningkey.htm",
			Notes:         []string{"Clanker uses local OCI CLI profiles; do not paste OCI private keys into chat."},
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
		"flyio": {
			ID:            "flyio",
			Provider:      "Fly.io",
			Purpose:       "Use a scoped Fly.io token for REST inventory and install flyctl for deploy, logs, SSH, scale, and secrets operations.",
			LoginCommands: []string{"fly auth login", "fly auth whoami", "fly tokens create org <org-slug>"},
			EnvVars:       []string{"FLY_API_TOKEN", "FLY_ACCESS_TOKEN", "FLY_ORG", "FLY_ORG_SLUG"},
			DocsURL:       "https://fly.io/docs/flyctl/auth/",
			TokenURL:      "https://fly.io/docs/security/tokens/",
		},
		"tencent": {
			ID:            "tencent",
			Provider:      "Tencent Cloud",
			Purpose:       "Install tccli, then configure a Tencent Cloud SecretId/SecretKey pair. Prefer sub-user credentials scoped to the services Clanker should inspect.",
			LoginCommands: []string{"tccli configure"},
			EnvVars:       []string{"TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_KEY", "TENCENTCLOUD_REGION", "TENCENT_SECRET_ID", "TENCENT_SECRET_KEY", "TENCENT_REGION"},
			DocsURL:       "https://www.tencentcloud.com/document/product/214/1526",
			TokenURL:      "https://www.tencentcloud.com/document/product/598/32675",
		},
		"verda": {
			ID:            "verda",
			Provider:      "Verda Cloud",
			Purpose:       "Create Verda Cloud API client credentials for GPU/AI infrastructure inventory, actions, and security coverage.",
			LoginCommands: []string{},
			EnvVars:       []string{"VERDA_CLIENT_ID", "VERDA_CLIENT_SECRET", "VERDA_PROJECT_ID"},
			DocsURL:       "https://api.verda.com/v1/docs",
			TokenURL:      "https://console.verda.com/account/api-keys",
		},
		"sentry": {
			ID:            "sentry",
			Provider:      "Sentry",
			Purpose:       "Install the official Sentry CLI or create an auth token, then provide the org slug for issues, releases, monitors, and alert management.",
			LoginCommands: []string{"sentry-cli login", "sentry auth login"},
			EnvVars:       []string{"SENTRY_AUTH_TOKEN", "SENTRY_ORG", "SENTRY_HOST"},
			DocsURL:       "https://docs.sentry.io/api/auth/",
			TokenURL:      "https://docs.sentry.io/api/guides/create-auth-token/",
		},
		"linear": {
			ID:            "linear",
			Provider:      "Linear",
			Purpose:       "Create a Linear API key for issue triage, projects, cycles, comments, and project-management actions.",
			LoginCommands: []string{},
			EnvVars:       []string{"LINEAR_API_KEY", "LINEAR_WORKSPACE_ID", "LINEAR_TEAM"},
			DocsURL:       "https://linear.app/developers/graphql",
			TokenURL:      "https://linear.app/settings/api",
		},
		"notion": {
			ID:            "notion",
			Provider:      "Notion",
			Purpose:       "Create a Notion integration token and share the specific pages/databases with the integration before expecting search or write access.",
			LoginCommands: []string{},
			EnvVars:       []string{"NOTION_API_KEY", "NOTION_TOKEN", "NOTION_INTEGRATION_TOKEN", "NOTION_DATABASE_ID"},
			DocsURL:       "https://developers.notion.com/guides/get-started/authorization",
			TokenURL:      "https://www.notion.so/my-integrations",
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
			Aliases:         append([]string(nil), guide.Aliases...),
			Providers:       append([]string(nil), guide.Providers...),
			VerifyCommand:   guide.VerifyCommand,
			InstallCommands: installCommandsForOS(guide),
			AllCommands:     guide.InstallCommands,
			DocsURL:         guide.DocsURL,
		}
		path, err := findToolGuideBinary(guide)
		if err == nil {
			status.Installed = true
			status.Path = path
			status.Version = detectVersion(ctx, guide, path)
		} else {
			status.Message = strings.Join(toolGuideBinaries(guide), " or ") + " not found in PATH"
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
			if fileExistsAny(wranglerConfigPaths()...) {
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
		{ID: "oracle", Name: "Oracle Cloud", RequiredTools: []string{"oci"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("OCI_CLI_PROFILE", "OCI_CLI_CONFIG_FILE", "OCI_TENANCY_OCID", "OCI_TENANCY_ID", "OCI_COMPARTMENT_ID") {
				notes = append(notes, "oracle env")
			}
			if fileExists(homePath(".oci", "config")) {
				notes = append(notes, "oci config")
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
			if fileExistsAny(vercelAuthPaths()...) {
				notes = append(notes, "vercel auth")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "flyio", Name: "Fly.io", RequiredTools: []string{"flyctl"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("FLY_API_TOKEN", "FLY_ACCESS_TOKEN", "FLY_ORG", "FLY_ORG_SLUG") {
				notes = append(notes, "fly.io env")
			}
			if fileExists(homePath(".fly", "config.yml")) || fileExists(homePath(".fly", "config.yaml")) {
				notes = append(notes, "fly config")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "tencent", Name: "Tencent Cloud", RequiredTools: []string{"tccli"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_KEY", "TENCENT_SECRET_ID", "TENCENT_SECRET_KEY") {
				notes = append(notes, "tencent env")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "verda", Name: "Verda Cloud", RequiredTools: []string{}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("VERDA_CLIENT_ID", "VERDA_CLIENT_SECRET", "VERDA_PROJECT_ID") {
				notes = append(notes, "verda env")
			}
			if fileExists(homePath(".verda", "credentials")) {
				notes = append(notes, "verda credentials")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "sentry", Name: "Sentry", RequiredTools: []string{"sentry-cli"}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("SENTRY_AUTH_TOKEN", "SENTRY_ORG", "SENTRY_HOST") {
				notes = append(notes, "sentry env")
			}
			if fileExists(homePath(".sentryclirc")) {
				notes = append(notes, "sentry config")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "linear", Name: "Linear", RequiredTools: []string{}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("LINEAR_API_KEY", "LINEAR_WORKSPACE_ID", "LINEAR_TEAM") {
				notes = append(notes, "linear env")
			}
			return len(notes) > 0, len(notes) > 0, notes
		}},
		{ID: "notion", Name: "Notion", RequiredTools: []string{}, Detect: func() (bool, bool, []string) {
			notes := []string{}
			if hasAnyEnv("NOTION_API_KEY", "NOTION_TOKEN", "NOTION_INTEGRATION_TOKEN", "NOTION_DATABASE_ID") {
				notes = append(notes, "notion env")
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
	case "oracle", "oracle cloud", "oracle-cloud", "oracle cloud infrastructure", "oracle-cloud-infrastructure", "oci-cli":
		return "oci"
	case "railway":
		return "railway"
	case "supabase":
		return "supabase"
	case "vercel":
		return "vercel"
	case "github", "github-cli":
		return "gh"
	case "fly", "flyio", "fly.io":
		return "flyctl"
	case "tencent", "tencent cloud", "tencent-cloud", "tencentcloud", "tccli":
		return "tccli"
	case "sentry", "sentry-cli", "sentrycli":
		return "sentry-cli"
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

func detectVersion(ctx context.Context, guide ToolGuide, binaryPath string) string {
	parts := strings.Fields(guide.VerifyCommand)
	if len(parts) == 0 {
		return ""
	}
	if strings.TrimSpace(binaryPath) != "" {
		parts[0] = binaryPath
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

func findToolGuideBinary(guide ToolGuide) (string, error) {
	for _, binary := range toolGuideBinaries(guide) {
		if path, err := exec.LookPath(binary); err == nil {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func toolGuideBinaries(guide ToolGuide) []string {
	binaries := []string{}
	seen := map[string]bool{}
	for _, binary := range append([]string{guide.Binary}, guide.Aliases...) {
		binary = strings.TrimSpace(binary)
		if binary == "" || seen[binary] {
			continue
		}
		seen[binary] = true
		binaries = append(binaries, binary)
	}
	if guide.ID == "flyctl" && !seen["flyctl"] {
		binaries = append(binaries, "flyctl")
	}
	return binaries
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

func fileExistsAny(paths ...string) bool {
	for _, path := range paths {
		if fileExists(path) {
			return true
		}
	}
	return false
}

// vercelAuthPaths lists the auth.json locations used by the Vercel CLI: the
// legacy ~/.vercel directory and the platform data directory newer releases
// write to (macOS: ~/Library/Application Support/com.vercel.cli, Linux:
// $XDG_DATA_HOME/com.vercel.cli, Windows: %AppData%/com.vercel.cli).
func vercelAuthPaths() []string {
	paths := []string{homePath(".vercel", "auth.json")}
	switch runtime.GOOS {
	case "darwin":
		paths = append(paths, homePath("Library", "Application Support", "com.vercel.cli", "auth.json"))
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			paths = append(paths, filepath.Join(appData, "com.vercel.cli", "auth.json"))
		}
	default:
		paths = append(paths, filepath.Join(xdgDataHome(), "com.vercel.cli", "auth.json"))
	}
	return paths
}

// wranglerConfigPaths lists the global config locations used by Wrangler:
// $WRANGLER_HOME when set, the legacy ~/.wrangler directory, and the platform
// config directory newer releases use when ~/.wrangler is absent (macOS:
// ~/Library/Preferences/.wrangler, Linux: $XDG_CONFIG_HOME/.wrangler,
// Windows: %AppData%/.wrangler).
func wranglerConfigPaths() []string {
	paths := []string{}
	if custom := strings.TrimSpace(os.Getenv("WRANGLER_HOME")); custom != "" {
		paths = append(paths, filepath.Join(custom, "config", "default.toml"))
	}
	paths = append(paths, homePath(".wrangler", "config", "default.toml"))
	switch runtime.GOOS {
	case "darwin":
		paths = append(paths, homePath("Library", "Preferences", ".wrangler", "config", "default.toml"))
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			paths = append(paths, filepath.Join(appData, ".wrangler", "config", "default.toml"))
		}
	default:
		paths = append(paths, filepath.Join(xdgConfigHome(), ".wrangler", "config", "default.toml"))
	}
	return paths
}

func xdgConfigHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir
	}
	return homePath(".config")
}

func xdgDataHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return dir
	}
	return homePath(".local", "share")
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max]) + "..."
}
