package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/gabriel-samfira/localshow/config"
	"github.com/gabriel-samfira/localshow/database"
	"github.com/spf13/cobra"
)

var csvFile string

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Import failed login attempts from a csv file",
	Long:  ``,
	RunE: func(cmd *cobra.Command, args []string) error {
		if csvFile == "" {
			return fmt.Errorf("csv file not specified")
		}
		ctx, stop := signal.NotifyContext(context.Background(), signals...)
		defer stop()

		if _, err := os.Stat(csvFile); err != nil {
			return fmt.Errorf("failed to stat csv file: %w", err)
		}

		cfg, err := config.NewConfig(cfgFile)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		db, err := database.NewSQLDatabase(ctx, cfg.Database)
		if err != nil {
			return fmt.Errorf("failed to create database: %w", err)
		}

		return db.ImportFromCSV(csvFile)
	},
}

func init() {
	importCmd.Flags().StringVarP(&csvFile, "csv-file", "f", "", "CSV file to import from")

	rootCmd.AddCommand(importCmd)
}
