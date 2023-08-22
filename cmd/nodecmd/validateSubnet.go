// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package nodecmd

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/ava-labs/avalanchego/vms/platformvm/status"

	"github.com/ava-labs/avalanche-cli/pkg/ansible"

	"github.com/ava-labs/avalanche-cli/cmd/subnetcmd"
	"github.com/ava-labs/avalanche-cli/pkg/models"
	"github.com/ava-labs/avalanche-cli/pkg/ux"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/spf13/cobra"
)

func newValidateSubnetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subnet [clusterName]",
		Short: "(ALPHA Warning) Join a Subnet as a validator",
		Long: `(ALPHA Warning) This command is currently in experimental mode.

The node validate subnet command enables all nodes in a cluster to be validators of a Subnet.
If the command is run before the nodes are Primary Network validators, the command will first
make the nodes Primary Network validators before making them Subnet validators. 
If The command is run before the nodes are bootstrapped on the Primary Network, the command will fail. 
You can check the bootstrap status by calling avalanche node status <clusterName>`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE:         validateSubnet,
	}
	cmd.Flags().StringVar(&subnetName, "subnet", "", "specify the subnet the node is validating")
	cmd.Flags().BoolVarP(&deployTestnet, "testnet", "t", false, "set up validator in testnet (alias to `fuji`)")
	cmd.Flags().BoolVarP(&deployTestnet, "fuji", "f", false, "set up validator in fuji (alias to `testnet`")
	cmd.Flags().BoolVarP(&deployMainnet, "mainnet", "m", false, "set up validator in mainnet")
	cmd.Flags().StringVarP(&keyName, "key", "k", "", "select the key to use [fuji only]")
	cmd.Flags().StringSliceVar(&ledgerAddresses, "ledger-addrs", []string{}, "use the given ledger addresses")
	cmd.Flags().Uint64Var(&weight, "stake-amount", 0, "how many AVAX to stake in the validator")
	cmd.Flags().DurationVar(&duration, "staking-period", 0, "how long validator validates for after start time")
	cmd.Flags().BoolVarP(&useLedger, "ledger", "g", false, "use ledger instead of key (always true on mainnet, defaults to false on fuji)")

	return cmd
}

func parseSubnetSyncOutput(filePath string, printOutput bool) (string, error) {
	jsonFile, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer jsonFile.Close()
	byteValue, _ := io.ReadAll(jsonFile)
	var result map[string]interface{}
	if err := json.Unmarshal(byteValue, &result); err != nil {
		return "", err
	}
	if printOutput {
		if err = printJSONOutput(byteValue); err != nil {
			return "", err
		}
	}
	statusInterface, ok := result["result"].(map[string]interface{})
	if ok {
		status, ok := statusInterface["status"].(string)
		if ok {
			return status, nil
		}
	}
	return "", errors.New("unable to parse subnet sync status")
}

func addNodeAsSubnetValidator(nodeID string, network models.Network) error {
	ux.Logger.PrintToUser("Adding the node as a Subnet Validator...")
	if err := subnetcmd.CallAddValidator(subnetName, nodeID, network); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Node successfully added as Subnet validator!")
	return nil
}

func getNodeSubnetSyncStatus(blockchainID, clusterName string, printOutput bool) (bool, error) {
	ux.Logger.PrintToUser("Checking if node is synced to subnet ...")
	if err := app.CreateAnsibleStatusFile(app.GetSubnetSyncJSONFile()); err != nil {
		return false, err
	}
	if err := ansible.RunAnsiblePlaybookSubnetSyncStatus(app.GetAnsibleDir(), app.GetSubnetSyncJSONFile(), blockchainID, app.GetAnsibleInventoryPath(clusterName)); err != nil {
		return false, err
	}
	subnetSyncStatus, err := parseSubnetSyncOutput(app.GetSubnetSyncJSONFile(), printOutput)
	if err != nil {
		return false, err
	}
	if err = app.RemoveAnsibleStatusDir(); err != nil {
		return false, err
	}
	if subnetSyncStatus == status.Syncing.String() {
		return true, nil
	} else if subnetSyncStatus == status.Validating.String() {
		return false, errors.New("node is already a subnet validator")
	}
	return false, nil
}

func waitForNodeToBePrimaryNetworkValidator(nodeID ids.NodeID) {
	ux.Logger.PrintToUser("Waiting for the node to start as a Primary Network Validator...")
	// wait for 20 seconds because we set the start time to be in 20 seconds
	time.Sleep(20 * time.Second)
	// long polling: try up to 5 times
	for i := 0; i < 5; i++ {
		// checkNodeIsPrimaryNetworkValidator only returns err if node is already a Primary Network validator
		if err := checkNodeIsPrimaryNetworkValidator(nodeID, models.Fuji); err != nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
}

func validateSubnet(_ *cobra.Command, args []string) error {
	clusterName := args[0]
	err := setupAnsible()
	if err != nil {
		return err
	}
	isBootstrapped, err := checkNodeIsBootstrapped(clusterName, false)
	if err != nil {
		return err
	}
	if !isBootstrapped {
		return errors.New("node is not bootstrapped yet, please try again later")
	}
	nodeIDStr, err := getClusterNodeID(clusterName)
	if err != nil {
		return err
	}
	nodeID, err := ids.NodeIDFromString(nodeIDStr)
	if err != nil {
		return err
	}
	if _, err = subnetcmd.ValidateSubnetNameAndGetChains([]string{subnetName}); err != nil {
		return err
	}
	sc, err := app.LoadSidecar(subnetName)
	if err != nil {
		return err
	}
	blockchainID := sc.Networks[models.Fuji.String()].BlockchainID
	if blockchainID == ids.Empty {
		return ErrNoBlockchainID
	}
	// we have to check if node is synced to subnet before adding the node as a validator
	isSubnetSynced, err := getNodeSubnetSyncStatus(blockchainID.String(), clusterName, false)
	if err != nil {
		return err
	}
	if !isSubnetSynced {
		return errors.New("node is not synced to subnet yet, please try again later")
	}
	addedNodeAsPrimaryNetworkValidator, err := addNodeAsPrimaryNetworkValidator(nodeID, models.Fuji)
	if err != nil {
		return err
	}
	if addedNodeAsPrimaryNetworkValidator {
		waitForNodeToBePrimaryNetworkValidator(nodeID)
	}
	return addNodeAsSubnetValidator(nodeIDStr, models.Fuji)
}
