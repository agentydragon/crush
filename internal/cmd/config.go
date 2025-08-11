package cmd

import (
	"fmt"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Print the current effective configuration",
	Long:  "Print the current effective configuration after applying defaults and environment resolution.",
	RunE: func(cmd *cobra.Command, args []string) error {
		redact, _ := cmd.Flags().GetBool("redact")
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		debug, _ := cmd.Flags().GetBool("debug")
		if _, err := config.Init(cwd, debug); err != nil {
			return err
		}
		bts, err := config.Get().EffectiveJSON(redact)
		if err != nil {
			return err
		}
		fmt.Println(string(bts))
		return nil
	},
}

func init() {
	configCmd.Flags().Bool("redact", true, "Redact secrets like API keys and tokens")
	rootCmd.AddCommand(configCmd)
}
