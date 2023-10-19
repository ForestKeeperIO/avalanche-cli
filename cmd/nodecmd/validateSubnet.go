// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package nodecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ava-labs/avalanchego/vms/platformvm/status"

	"github.com/ava-labs/avalanche-cli/pkg/ansible"
	"github.com/ava-labs/avalanche-cli/pkg/ssh"

	subnetcmd "github.com/ava-labs/avalanche-cli/cmd/subnetcmd"
	"github.com/ava-labs/avalanche-cli/pkg/models"
	"github.com/ava-labs/avalanche-cli/pkg/ux"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/spf13/cobra"
)

func newValidateSubnetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subnet [clusterName] [subnetName]",
		Short: "(ALPHA Warning) Join a Subnet as a validator",
		Long: `(ALPHA Warning) This command is currently in experimental mode.

The node validate subnet command enables all nodes in a cluster to be validators of a Subnet.
If the command is run before the nodes are Primary Network validators, the command will first
make the nodes Primary Network validators before making them Subnet validators. 
If The command is run before the nodes are bootstrapped on the Primary Network, the command will fail. 
You can check the bootstrap status by calling avalanche node status <clusterName>
If The command is run before the nodes are synced to the subnet, the command will fail.
You can check the subnet sync status by calling avalanche node status <clusterName> --subnet <subnetName>`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(2),
		RunE:         validateSubnet,
	}
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

func parseSubnetSyncOutput(byteValue []byte) (string, error) {
	var result map[string]interface{}
	if err := json.Unmarshal(byteValue, &result); err != nil {
		return "", err
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

func addNodeAsSubnetValidator(nodeID, subnetName string, network models.Network, currentNodeIndex, nodeCount int) error {
	ux.Logger.PrintToUser("Adding the node as a Subnet Validator...")
	if err := subnetcmd.CallAddValidator(subnetName, nodeID, network); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Node %s successfully added as Subnet validator! (%d / %d)", nodeID, currentNodeIndex+1, nodeCount)
	ux.Logger.PrintToUser("======================================")
	return nil
}

// getNodeSubnetSyncStatus checks if node ansibleNodeID is bootstrapped to blockchain blockchainID
// if getNodeSubnetSyncStatus is called from node validate subnet command, it will fail if
// node status is not 'syncing'. If getNodeSubnetSyncStatus is called from node status command,
// it will return true node status is 'syncing'
func getNodeSubnetSyncStatus(blockchainID string, host models.Host) (string, error) {
	ux.Logger.PrintToUser("Checking if node %s is synced to subnet ...", host.NodeID)
	resp, err := ssh.RunSSHSubnetSyncStatus(host, blockchainID)
	if err != nil {
		return "", err
	}
	subnetSyncStatus, err := parseSubnetSyncOutput(resp)
	if err != nil {
		return "", err
	}
	return subnetSyncStatus, nil
}

func waitForNodeToBePrimaryNetworkValidator(nodeID ids.NodeID) error {
	ux.Logger.PrintToUser("Waiting for the node to start as a Primary Network Validator...")
	// wait for 20 seconds because we set the start time to be in 20 seconds
	time.Sleep(20 * time.Second)
	// long polling: try up to 5 times
	for i := 0; i < 5; i++ {
		isValidator, err := checkNodeIsPrimaryNetworkValidator(nodeID, models.Fuji)
		if err != nil {
			return err
		}
		if isValidator {
			break
		}
		time.Sleep(5 * time.Second)
	}
	return nil
}

func validateSubnet(_ *cobra.Command, args []string) error {
	clusterName := args[0]
	subnetName := args[1]
	if err := checkCluster(clusterName); err != nil {
		return err
	}
	err := setupAnsible(clusterName)
	if err != nil {
		return err
	}
	notBootstrappedNodes, err := checkClusterIsBootstrapped(clusterName)
	if err != nil {
		return err
	}
	if len(notBootstrappedNodes) > 0 {
		return fmt.Errorf("node(s) %s are not bootstrapped yet, please try again later", notBootstrappedNodes)
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
	hosts, err := ansible.GetInventoryFromAnsibleInventoryFile(app.GetAnsibleInventoryDirPath(clusterName))
	if err != nil {
		return err
	}
	ux.Logger.PrintToUser("Note that we have staggered the end time of validation period to increase by 24 hours for each node added if multiple nodes are added as Primary Network validators simultaneously")
	failedNodes := []string{}
	nodeErrors := []error{}

	NodeSubnetSyncStatus := map[string]string{}
	nodeResultChannel := make(chan models.NodeStringResult, len(hosts))
	parallelWaitGroup := sync.WaitGroup{}
	for _, host := range hosts {
		parallelWaitGroup.Add(1)
		go func(nodeResultChannel chan models.NodeStringResult, host models.Host) {
			defer parallelWaitGroup.Done()
			subnetSyncStatus, err := getNodeSubnetSyncStatus(blockchainID.String(), host)
			nodeResultChannel <- models.NodeStringResult{NodeID: host.NodeID, Value: subnetSyncStatus, Err: err}
		}(nodeResultChannel, host)
	}
	parallelWaitGroup.Wait()
	close(nodeResultChannel)
	for NodeSubnetSyncStatusResult := range nodeResultChannel {
		if NodeSubnetSyncStatusResult.Err != nil {
			NodeSubnetSyncStatus[NodeSubnetSyncStatusResult.NodeID] = ""
		} else {
			NodeSubnetSyncStatus[NodeSubnetSyncStatusResult.NodeID] = NodeSubnetSyncStatusResult.Value
		}
	}
	for i, host := range hosts {
		nodeIDStr, err := getClusterNodeID(clusterName, host)
		if err != nil {
			ux.Logger.PrintToUser("Failed to add node %s as subnet validator due to %s", host, err.Error())
			failedNodes = append(failedNodes, host.NodeID)
			nodeErrors = append(nodeErrors, err)
			continue
		}
		nodeID, err := ids.NodeIDFromString(nodeIDStr)
		if err != nil {
			ux.Logger.PrintToUser("Failed to add node %s as subnet validator due to %s", host, err.Error())
			failedNodes = append(failedNodes, host.NodeID)
			nodeErrors = append(nodeErrors, err)
			continue
		}
		// we have to check if node is synced to subnet before adding the node as a validator
		subnetSyncStatus := NodeSubnetSyncStatus[host.NodeID]
		if subnetSyncStatus == "" {
			ux.Logger.PrintToUser("Failed to get subnet sync status for node %s", host)
			failedNodes = append(failedNodes, host.NodeID)
			nodeErrors = append(nodeErrors, err)
			continue
		}
		if subnetSyncStatus != status.Syncing.String() {
			failedNodes = append(failedNodes, host.NodeID)
			if subnetSyncStatus == status.Validating.String() {
				ux.Logger.PrintToUser("Failed to add node %s as subnet validator as node is already a subnet validator", host)
				nodeErrors = append(nodeErrors, errors.New("node is already a subnet validator"))
			} else {
				ux.Logger.PrintToUser("Failed to add node %s as subnet validator as node is not synced to subnet yet", host)
				nodeErrors = append(nodeErrors, errors.New("node is not synced to subnet yet, please try again later"))
			}
			continue
		}
		addedNodeAsPrimaryNetworkValidator, err := addNodeAsPrimaryNetworkValidator(nodeID, models.Fuji, i, host.GetInstanceID())
		if err != nil {
			ux.Logger.PrintToUser("Failed to add node %s as subnet validator due to %s", host, err.Error())
			failedNodes = append(failedNodes, host.NodeID)
			nodeErrors = append(nodeErrors, err)
			continue
		}
		if addedNodeAsPrimaryNetworkValidator {
			if err := waitForNodeToBePrimaryNetworkValidator(nodeID); err != nil {
				ux.Logger.PrintToUser("Failed to add node %s as subnet validator due to %s", host, err.Error())
				failedNodes = append(failedNodes, host.NodeID)
				nodeErrors = append(nodeErrors, err)
				continue
			}
		}
		err = addNodeAsSubnetValidator(nodeIDStr, subnetName, models.Fuji, i, len(hosts))
		if err != nil {
			ux.Logger.PrintToUser("Failed to add node %s as subnet validator due to %s", host, err.Error())
			failedNodes = append(failedNodes, host.NodeID)
			nodeErrors = append(nodeErrors, err)
		}
	}
	if len(failedNodes) > 0 {
		ux.Logger.PrintToUser("Failed nodes: ")
		for i, node := range failedNodes {
			ux.Logger.PrintToUser("node %s failed due to %s", node, nodeErrors[i])
		}
		return fmt.Errorf("node(s) %s failed to validate subnet %s", failedNodes, subnetName)
	} else {
		ux.Logger.PrintToUser("All nodes in cluster %s are successfully added as Subnet validators!", clusterName)
	}
	return nil
}
