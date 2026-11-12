package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newJobCmd(opts *OutputOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Manage and inspect asynchronous build jobs",
	}

	cmd.AddCommand(newJobLogsCmd(opts))
	return cmd
}

func newJobLogsCmd(opts *OutputOptions) *cobra.Command {
	var follow bool
	var serverURL string

	cmd := &cobra.Command{
		Use:   "logs <job-id>",
		Short: "Stream real-time build execution logs for a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID := args[0]
			if serverURL == "" {
				serverURL = os.Getenv("BSGO_SERVER_URL")
				if serverURL == "" {
					serverURL = "http://localhost:8080"
				}
			}
			return streamJobLogs(cmd.Context(), serverURL, jobID, follow, opts, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream logs in real-time until job completion")
	cmd.Flags().StringVar(&serverURL, "server", "", "BSGO server URL (defaults to http://localhost:8080 or BSGO_SERVER_URL)")
	return cmd
}

func streamJobLogs(ctx context.Context, serverURL, jobID string, follow bool, opts *OutputOptions, out io.Writer) error {
	reqURL := fmt.Sprintf("%s/api/v1/jobs/%s/logs", strings.TrimSuffix(serverURL, "/"), jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed connecting to log server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("log server returned status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		rawText := strings.TrimPrefix(line, "data: ")

		if opts.JSON {
			entry := map[string]string{
				"job_id": jobID,
				"log":    rawText,
			}
			data, _ := json.Marshal(entry)
			fmt.Fprintln(out, string(data))
		} else {
			fmt.Fprintln(out, rawText)
		}

		if !follow && (strings.Contains(rawText, "build SUCCEEDED") || strings.Contains(rawText, "build FAILED") || strings.Contains(rawText, "build ABORTED")) {
			break
		}
	}
	return scanner.Err()
}
