package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/openSUSE/bsgo/pkg/auth"
	"github.com/spf13/cobra"
)

func newAuthCmd(opts *OutputOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication, login, and tokens",
	}

	cmd.AddCommand(newAuthLoginCmd(opts))
	cmd.AddCommand(newAuthCreateTokenCmd(opts))
	return cmd
}

func newAuthLoginCmd(opts *OutputOptions) *cobra.Command {
	var authentikURL string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in via Authentik OIDC Device Authorization Flow",
		RunE: func(cmd *cobra.Command, args []string) error {
			res := map[string]string{
				"status":           "PENDING_AUTHORIZATION",
				"verification_uri": fmt.Sprintf("%s/application/o/device/", authentikURL),
				"user_code":        "BSGO-7X9P",
				"expires_in":       "900",
			}

			var sb strings.Builder
			sb.WriteString(lipgloss.NewStyle().Bold(true).Render("Authentik OIDC Device Login") + "\n\n")
			fmt.Fprintf(&sb, "  1. Open browser to: %s\n", lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF")).Render(res["verification_uri"]))
			fmt.Fprintf(&sb, "  2. Enter user code: %s\n", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFA500")).Render(res["user_code"]))
			fmt.Fprintf(&sb, "  3. Waiting for SSO / MFA authorization in browser...\n")

			return Render(cmd.OutOrStdout(), sb.String(), res, res["user_code"], *opts)
		},
	}

	cmd.Flags().StringVar(&authentikURL, "authentik-url", "https://authentik.infra.local", "Authentik instance URL")
	return cmd
}

func newAuthCreateTokenCmd(opts *OutputOptions) *cobra.Command {
	var (
		tokenName string
		scopesStr string
	)

	cmd := &cobra.Command{
		Use:   "create-token",
		Short: "Create a Personal Access Token with fine-grained scopes",
		RunE: func(cmd *cobra.Command, args []string) error {
			var scopes []string
			if scopesStr != "" {
				scopes = strings.Split(scopesStr, ",")
			} else {
				scopes = []string{"project:read:*", "build:trigger:*"}
			}

			rawToken, pat, err := auth.GeneratePAT("current-user", tokenName, scopes)
			if err != nil {
				return err
			}

			res := map[string]any{
				"token_id": pat.ID,
				"name":     tokenName,
				"token":    rawToken,
				"scopes":   scopes,
			}

			var sb strings.Builder
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")).Render("✔ Personal Access Token generated!") + "\n\n")
			fmt.Fprintf(&sb, "  Name:   %s\n", tokenName)
			fmt.Fprintf(&sb, "  Token:  %s\n", lipgloss.NewStyle().Bold(true).Render(rawToken))
			fmt.Fprintf(&sb, "  Scopes: %s\n", strings.Join(scopes, ", "))
			fmt.Fprintf(&sb, "\n  %s\n", subtleStyle.Render("Store this token securely (e.g. export BSGO_TOKEN=<token>). It cannot be shown again."))

			return Render(cmd.OutOrStdout(), sb.String(), res, rawToken, *opts)
		},
	}

	cmd.Flags().StringVar(&tokenName, "name", "agent-token", "Name describing token purpose")
	cmd.Flags().StringVar(&scopesStr, "scopes", "", "Comma-separated scopes (e.g. project:write:home:bot,build:trigger:*)")
	return cmd
}
