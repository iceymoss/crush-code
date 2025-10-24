package cmd

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"golang.org/x/exp/slog"
	"os"
)

func run() error {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	app, err := setupApp(cmd)
	if err != nil {
		return err
	}
	defer app.Shutdown()

	if !app.Config().IsConfigured() {
		return fmt.Errorf("no providers configured - please run 'crush' to set up a provider interactively")
	}

	prompt := "哈喽，如何设计一条rust入门到大师的计划？"

	prompt, err = MaybePrependStdin(prompt)
	if err != nil {
		slog.Error("Failed to read from stdin", "error", err)
		return err
	}

	if prompt == "" {
		return fmt.Errorf("no prompt provided")
	}

	quiet := false

	// Run non-interactive flow using the App method
	return app.RunNonInteractive(cmd.Context(), prompt, quiet)
}

func ExecuteTest() {
	err := run()
	if err != nil {
		os.Exit(1)
	}
}
