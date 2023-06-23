// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package subnetcmd

import (
	"fmt"
	"github.com/ava-labs/avalanche-cli/pkg/models"
	"github.com/ava-labs/avalanche-cli/pkg/ux"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
)

// avalanche subnet teleporter
func newTeleporterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "teleporter [subnetName]",
		Short:        "set up subnet teleporter",
		Long:         `Enables teleporter functionality in subnet`,
		SilenceUsage: true,
		RunE:         setUpTeleporter,
		Args:         cobra.ExactArgs(2),
	}
	cmd.Flags().BoolVarP(&deployLocal, "local", "l", false, "deploy to a local network")
	return cmd
}

func setUpTeleporter(cmd *cobra.Command, args []string) error {
	vmFile = "/Users/raymondsukanto/go/src/github.com/ava-labs/avalanchego/build/plugins/srEXiWaHuhNyGwPUi444Tu47ZEDwxTWrbQiuD7FmgSAQ6X7Dy"
	genesisFile = "./warp_genesisA.json"
	forceCreate = true
	useCustom = true
	createErr := createSubnetConfig(cmd, args[:1])
	if createErr != nil {
		return createErr
	}
	genesisFile = "./warp_genesisB.json"
	createErr = createSubnetConfig(cmd, args[1:])
	if createErr != nil {
		return createErr
	}
	deployLocal = true
	deployErr := deploySubnet(cmd, args[:1])
	if deployErr != nil {
		return deployErr
	}

	sc, err := app.LoadSidecar(args[0])
	if err != nil {
		return fmt.Errorf("unable to load sidecar: %w", err)
	}
	subnetIDA := sc.Networks[models.Local.String()].BlockchainID
	subnetURLA := "http://127.0.0.1:9650/ext/bc/" + subnetIDA.String() + "/rpc"
	ux.Logger.PrintToUser(fmt.Sprintf("Subnet A URL: %s", subnetURLA))

	deployErr = deploySubnet(cmd, args[1:])
	if deployErr != nil {
		return deployErr
	}
	sc, err = app.LoadSidecar(args[1])
	if err != nil {
		return fmt.Errorf("unable to load sidecar: %w", err)
	}
	subnetIDB := sc.Networks[models.Local.String()].BlockchainID
	subnetURLB := "http://127.0.0.1:9650/ext/bc/" + subnetIDB.String() + "/rpc"
	cChainURL := "http://127.0.0.1:9650/ext/bc/C/rpc"
	ux.Logger.PrintToUser(fmt.Sprintf("Subnet B URL: %s", subnetURLB))

	if err := exec.Command("go", "run", "./teleporter/contract-deployment/contractDeploymentTools.go", "constructKeylessTx", "./teleporter/contracts/out/TeleporterMessenger.sol/TeleporterMessenger.json").Run(); err != nil {
		return err
	}

	private_key := "0x56289e99c94b6912bfc12adc093c9b51124f0dc54ac7a766b2bc5ccf558d8027"
	teleporter_deploy_address_bytes, err := os.ReadFile("./UniversalTeleporterDeployerAddress.txt")
	teleporter_deploy_address := string(teleporter_deploy_address_bytes)
	teleporter_deploy_tx_bytes, err := os.ReadFile("./UniversalTeleporterDeployerTransaction.txt")
	teleporter_deploy_tx := string(teleporter_deploy_tx_bytes)

	if err := exec.Command("cast", "send", "--private-key", private_key, "--value", "50ether", teleporter_deploy_address, "--rpc-url", subnetURLA).Run(); err != nil {
		return err
	}
	if err := exec.Command("cast", "send", "--private-key", private_key, "--value", "50ether", teleporter_deploy_address, "--rpc-url", subnetURLB).Run(); err != nil {
		return err
	}
	if err := exec.Command("cast", "send", "--private-key", private_key, "--value", "50ether", teleporter_deploy_address, "--rpc-url", cChainURL).Run(); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Sent ether to teleporter deployer on both subnets")

	if err := exec.Command("cast", "publish", "--rpc-url", subnetURLA, teleporter_deploy_tx).Run(); err != nil {
		return err
	}
	if err := exec.Command("cast", "publish", "--rpc-url", subnetURLB, teleporter_deploy_tx).Run(); err != nil {
		return err
	}
	if err := exec.Command("cast", "publish", "--rpc-url", cChainURL, teleporter_deploy_tx).Run(); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Deployed teleporter on all subnets")

	relayer_address := "0xA100fF48a37cab9f87c8b5Da933DA46ea1a5fb80"

	if err := exec.Command("cast", "send", "--private-key", private_key, "--value", "50ether", relayer_address, "--rpc-url", subnetURLA).Run(); err != nil {
		return err
	}
	if err := exec.Command("cast", "send", "--private-key", private_key, "--value", "50ether", relayer_address, "--rpc-url", subnetURLB).Run(); err != nil {
		return err
	}
	if err := exec.Command("cast", "send", "--private-key", private_key, "--value", "50ether", relayer_address, "--rpc-url", cChainURL).Run(); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Sent ether to relayer account on all subnets")

	return nil
}
