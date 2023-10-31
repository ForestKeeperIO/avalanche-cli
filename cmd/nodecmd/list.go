// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package nodecmd

import (
	"fmt"

	"github.com/ava-labs/avalanche-cli/pkg/ansible"
	"github.com/ava-labs/avalanche-cli/pkg/constants"
	"github.com/ava-labs/avalanche-cli/pkg/models"
	"github.com/ava-labs/avalanche-cli/pkg/utils"
	"github.com/ava-labs/avalanche-cli/pkg/ux"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "(ALPHA Warning) List all clusters together with their nodes",
		Long: `(ALPHA Warning) This command is currently in experimental mode.

The node list command lists all clusters together with their nodes.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(0),
		RunE:         list,
	}

	return cmd
}

func list(_ *cobra.Command, _ []string) error {
	var err error
	clusterConfig := models.ClusterConfig{}
	if app.ClusterConfigExists() {
		clusterConfig, err = app.LoadClusterConfig()
		if err != nil {
			return err
		}
	}
	for clusterName, clusterNodes := range clusterConfig.Clusters {
		ux.Logger.PrintToUser(fmt.Sprintf("Cluster %q", clusterName))
		if err := checkCluster(clusterName); err != nil {
			return err
		}
		if err := setupAnsible(clusterName); err != nil {
			return err
		}
		ansibleHosts, err := ansible.GetHostMapfromAnsibleInventory(app.GetAnsibleInventoryDirPath(clusterName))
		if err != nil {
			return err
		}
		for _, clusterNode := range clusterNodes {
			nodeConfig, err := app.LoadClusterNodeConfig(clusterNode)
			if err != nil {
				return err
			}
			hostName := fmt.Sprintf("%s_%s", constants.AWSNodeAnsiblePrefix, clusterNode)
			if nodeConfig.CloudService == constants.GCPCloudService {
				hostName = fmt.Sprintf("%s_%s", constants.GCPNodeAnsiblePrefix, clusterNode)
			}
			ux.Logger.PrintToUser(fmt.Sprintf("  Node %q to connect: %s", clusterNode, utils.GetSSHConnectionString(ansibleHosts[hostName].IP, ansibleHosts[hostName].SSHPrivateKeyPath)))
		}
	}
	return nil
}
