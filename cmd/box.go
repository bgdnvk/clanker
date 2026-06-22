package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bgdnvk/clanker/internal/clankerbox"
	"github.com/spf13/cobra"
)

func newBoxCmd() *cobra.Command {
	var agent string
	var region string
	var name string
	var image string
	var projectID string
	var serviceAccount string
	var artifactRepo string
	var stateBucket string
	var controlPlaneURL string
	var requireAuth bool
	var websocketTimeout int

	boxCmd := &cobra.Command{
		Use:   "box",
		Short: "Run and describe Clanker Box agent sandboxes",
	}

	catalogCmd := &cobra.Command{
		Use:   "catalog",
		Short: "Print supported Clanker Box agents and regions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
				"agents":  clankerbox.Agents(),
				"regions": clankerbox.Regions(),
			})
		},
	}

	manifestCmd := &cobra.Command{
		Use:   "manifest",
		Short: "Print the Cloud Run runtime manifest for a Clanker Box",
		RunE: func(cmd *cobra.Command, args []string) error {
			manifest, err := clankerbox.NewManifest(name, agent, region, clankerbox.ManifestOptions{
				ProjectID:            projectID,
				Image:                image,
				ServiceAccountEmail:  serviceAccount,
				ArtifactRepository:   artifactRepo,
				StateBucket:          stateBucket,
				ControlPlaneBaseURL:  controlPlaneURL,
				RequireAuth:          requireAuth,
				WebSocketTimeoutMins: websocketTimeout,
			})
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(manifest)
		},
	}
	manifestCmd.Flags().StringVar(&name, "name", "clanker-box", "Box display name")
	manifestCmd.Flags().StringVar(&agent, "agent", "clanker-cli", "Agent to run (hermes, openclaw, codex, claude-code, clanker-cli)")
	manifestCmd.Flags().StringVar(&region, "region", "us-central1", "Cloud Run region")
	manifestCmd.Flags().StringVar(&image, "image", "", "Container image URI")
	manifestCmd.Flags().StringVar(&projectID, "project", "", "GCP project ID")
	manifestCmd.Flags().StringVar(&serviceAccount, "service-account", "", "Runtime service account email")
	manifestCmd.Flags().StringVar(&artifactRepo, "artifact-repo", "", "Artifact Registry repository")
	manifestCmd.Flags().StringVar(&stateBucket, "state-bucket", "", "Cloud Storage state bucket")
	manifestCmd.Flags().StringVar(&controlPlaneURL, "control-plane-url", "", "Clanker Cloud control-plane URL")
	manifestCmd.Flags().BoolVar(&requireAuth, "require-auth", true, "Require X-API-Key or Bearer token for message endpoints")
	manifestCmd.Flags().IntVar(&websocketTimeout, "websocket-timeout-minutes", 60, "Cloud Run WebSocket request timeout target")

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve a Clanker Box agent runtime over HTTP",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := clankerbox.RuntimeConfigFromEnv(Version)
			if strings.TrimSpace(agent) != "" {
				cfg.Agent = agent
			}
			if strings.TrimSpace(region) != "" {
				cfg.Region = region
			}
			if strings.TrimSpace(name) != "" {
				cfg.Name = name
			}
			port := strings.TrimSpace(os.Getenv("PORT"))
			if port == "" {
				port = "8080"
			}
			addr := ":" + port
			fmt.Fprintf(cmd.ErrOrStderr(), "[clanker-box] serving %s in %s on %s\n", cfg.Agent, cfg.Region, addr)
			return clankerbox.NewServer(cfg, nil).ListenAndServe(addr)
		},
	}
	serveCmd.Flags().StringVar(&name, "name", "", "Box display name")
	serveCmd.Flags().StringVar(&agent, "agent", "", "Agent to run")
	serveCmd.Flags().StringVar(&region, "region", "", "Cloud Run region")

	boxCmd.AddCommand(catalogCmd, manifestCmd, serveCmd)
	return boxCmd
}
