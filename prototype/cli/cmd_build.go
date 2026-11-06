package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Inspect and control build jobs",
}

var buildStatusCmd = &cobra.Command{
	Use:   "status <job-id>",
	Short: "Show status of a build job",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]
		for _, j := range mockJobs {
			if j.ID == id {
				badge := pendingBadge
				switch j.Status {
				case "BUILDING":
					badge = buildingBadge
				case "SUCCEEDED":
					badge = successBadge
				case "FAILED":
					badge = failedBadge
				}

				var sb strings.Builder
				sb.WriteString(titleStyle.Render("Build Job: "+j.ID) + "\n\n")
				fmt.Fprintf(&sb, "  Status:       %s\n", badge.Render())
				fmt.Fprintf(&sb, "  Package:      %s/%s\n", j.Project, j.Package)
				fmt.Fprintf(&sb, "  Target:       %s (%s)\n", j.Target, j.Arch)
				fmt.Fprintf(&sb, "  Priority:     %d (Topological Depth)\n", j.Priority)
				if j.WorkerID != "" {
					fmt.Fprintf(&sb, "  Worker:       %s\n", j.WorkerID)
				}
				fmt.Fprintf(&sb, "  Elapsed:      %s\n", j.Duration)
				sb.WriteString("  NATS Subject: bsgo.jobs.status\n")
				fmt.Fprintf(&sb, "  S3 Prefix:    s3://bsgo-artifacts/%s/%s/%s/%s/\n",
					j.Project, j.Target, j.Arch, j.Package)

				renderOutput(sb.String(), j, j.Status)
				return
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: job '%s' not found\n", id)
	},
}

var buildCancelCmd = &cobra.Command{
	Use:   "cancel <job-id>",
	Short: "Cancel an active or pending build job",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]
		res := map[string]string{
			"status":  "ABORTED",
			"job_id":  id,
			"subject": fmt.Sprintf("bsgo.jobs.%s.control", id),
			"reason":  "USER_REQUESTED_ABORT",
		}

		var sb strings.Builder
		sb.WriteString(failedBadge.SetString("ABORTED").Render() + "\n")
		fmt.Fprintf(&sb, "  Job %s has been signaled to abort.\n", id)
		fmt.Fprintf(&sb, "  Control signal dispatched via bsgo.jobs.%s.control\n", id)

		renderOutput(sb.String(), res, "ABORTED")
	},
}

var followLogs bool

var buildLogsCmd = &cobra.Command{
	Use:   "logs <job-id>",
	Short: "View or stream logs from a build job",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]

		simulatedLogs := []string{
			"[00:00:01] bsgo-worker-x86-01: claiming job " + id + " from bsgo.workers.worker-x86-01.job",
			"[00:00:02] bsgo-worker: downloading chroot RPMs from s3://bsgo-artifacts/cache/...",
			"[00:00:04] obs-build: initializing QEMU/KVM VM overlay root.qcow2 (vCPUs: 16, RAM: 32Gi)",
			"[00:00:06] obs-build: running preinstall (/usr/lib/build/build --root /mount --clean)",
			"[00:00:15] obs-build: unpacking glibc-devel, gcc13, binutils into buildroot",
			"[00:00:30] obs-build: entering build phase (%build) as user abuild",
			"[00:00:35] gcc -O2 -g -Wall -c main.c -o main.o",
			"[00:00:40] gcc -O2 -g -Wall -c solver.c -o solver.o",
			"[00:00:48] gcc -o myapp main.o solver.o -lsolv",
			"[00:00:52] obs-build: build succeeded, extracting packages to .build.packages/",
			"[00:00:55] bsgo-worker: uploading myapp-1.0-1.x86_64.rpm to S3...",
			"[00:00:58] bsgo-worker: build completed with exit code 0",
		}

		if jsonOutput {
			type LogLine struct {
				JobID     string `json:"job_id"`
				Timestamp string `json:"timestamp"`
				Message   string `json:"message"`
			}
			var lines []LogLine
			for _, l := range simulatedLogs {
				lines = append(lines, LogLine{
					JobID:     id,
					Timestamp: time.Now().Format(time.RFC3339),
					Message:   l,
				})
			}
			renderOutput("", lines, "")
			return
		}

		if followLogs {
			fmt.Printf("Subscribed to live NATS stream: bsgo.logs.%s\n\n", id)
			for _, l := range simulatedLogs {
				fmt.Println(l)
				time.Sleep(100 * time.Millisecond) // realistic streaming feel
			}
		} else {
			for _, l := range simulatedLogs {
				fmt.Println(l)
			}
		}
	},
}

func init() {
	buildLogsCmd.Flags().BoolVarP(&followLogs, "follow", "f", false, "Stream live logs via Core NATS")
	buildCmd.AddCommand(buildStatusCmd)
	buildCmd.AddCommand(buildCancelCmd)
	buildCmd.AddCommand(buildLogsCmd)
}
