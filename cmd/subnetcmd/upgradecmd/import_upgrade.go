// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package upgradecmd

import (
	"fmt"
	"os"

	"github.com/ava-labs/avalanche-cli/pkg/ux"
	"github.com/spf13/cobra"
)

var upgradeBytesFilePath string

const upgradeBytesFilePathKey = "upgrade-filepath"

// avalanche subnet upgrade import
func newUpgradeImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import [subnetName]",
		Short: "Generate the configuration file to upgrade subnet nodes",
		Long:  `Upgrades to subnet nodes can be executed by providing a upgrade.json file to the nodes. This command starts a wizard guiding the user generating the required file.`,
		RunE:  upgradeImportCmd,
		Args:  cobra.ExactArgs(1),
	}

	cmd.Flags().StringVar(&upgradeBytesFilePath, upgradeBytesFilePathKey, "", "Import upgrade bytes file into local environment")
	cmd.MarkFlagRequired(upgradeBytesFilePathKey)

	return cmd
}

func upgradeImportCmd(cmd *cobra.Command, args []string) error {
	subnetName := args[0]
	if !app.GenesisExists(subnetName) {
		ux.Logger.PrintToUser("The provided subnet name %q does not exist", subnetName)
		return nil
	}

	if _, err := os.Stat(upgradeBytesFilePath); err != nil {
		if err == os.ErrNotExist {
			return fmt.Errorf("The upgrade file specified with path %q does not exist", upgradeBytesFilePath)
		}
		return err
	}

	fileBytes, err := os.ReadFile(upgradeBytesFilePath)
	if err != nil {
		return fmt.Errorf("failed to read the provided upgrade file: %w", err)
	}

	return writeUpgradeFile(fileBytes, subnetName)
}
