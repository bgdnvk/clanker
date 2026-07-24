package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/bgdnvk/clanker/internal/clankercloud"
	"github.com/spf13/cobra"
)

var cloudCmd = &cobra.Command{
	Use:   "cloud",
	Short: "Work with Clanker Cloud and the local desktop app",
}

var cloudDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run the Clanker Cloud local setup doctor",
	RunE: func(cmd *cobra.Command, args []string) error {
		launch, _ := cmd.Flags().GetBool("launch")
		client := clankercloud.NewClient()
		ctx := cmd.Context()

		if launch {
			client.LaunchApp(ctx, clankercloud.LaunchOptions{Wait: true, TimeoutSeconds: 60})
		}

		result, err := client.CallAPI(ctx, http.MethodGet, "/api/setup/doctor", nil, nil, "")
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(result.Body, "", "  ")
		if err != nil {
			return fmt.Errorf("encode doctor result: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		if result.Status < 200 || result.Status >= 300 {
			return fmt.Errorf("setup doctor returned status %d", result.Status)
		}
		return nil
	},
}

func init() {
	cloudDoctorCmd.Flags().Bool("launch", false, "launch Clanker Cloud and wait for the local backend before running the doctor")
	cloudCmd.AddCommand(cloudDoctorCmd)
	rootCmd.AddCommand(cloudCmd)
}
