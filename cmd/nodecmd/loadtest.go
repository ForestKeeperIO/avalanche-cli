// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package nodecmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ava-labs/avalanche-cli/pkg/ansible"
	"github.com/ava-labs/avalanche-cli/pkg/application"
	awsAPI "github.com/ava-labs/avalanche-cli/pkg/cloud/aws"
	gcpAPI "github.com/ava-labs/avalanche-cli/pkg/cloud/gcp"
	"github.com/ava-labs/avalanche-cli/pkg/constants"
	"github.com/ava-labs/avalanche-cli/pkg/models"
	"github.com/ava-labs/avalanche-cli/pkg/prompts"
	"github.com/ava-labs/avalanche-cli/pkg/ssh"
	"github.com/ava-labs/avalanche-cli/pkg/utils"
	"github.com/ava-labs/avalanche-cli/pkg/ux"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/spf13/cobra"
	"golang.org/x/exp/maps"
	"gopkg.in/yaml.v3"
)

var (
	loadTestRepoURL  string
	loadTestBuildCmd string
	loadTestCmd      string
)

func newLoadTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "loadtest [clusterName] [subnetName]",
		Short: "(ALPHA Warning) Start loadtest for existing devnet cluster",
		Long: `(ALPHA Warning) This command is currently in experimental mode. 

The node loadtest command starts a loadtest run for the existing devnet cluster. It creates a separated cloud server and
this loadtest script will be run on the provisioned cloud server in the same cloud and region as the cluster.
Loadtest script will be run in ubuntu user home directory with the provided arguments.
After loadtest is done it will deliver generated reports if any along with loadtest logs before terminating used cloud server.`,

		SilenceUsage: true,
		Args:         cobra.ExactArgs(2),
		RunE:         createLoadTest,
	}
	cmd.Flags().BoolVar(&useAWS, "aws", false, "create loadtest node in AWS cloud")
	cmd.Flags().BoolVar(&useGCP, "gcp", false, "create loadtest in GCP cloud")
	cmd.Flags().StringVar(&nodeType, "node-type", "default", "cloud instance type for loadtest script")
	cmd.Flags().BoolVar(&authorizeAccess, "authorize-access", false, "authorize CLI to create cloud resources")
	cmd.Flags().StringVar(&awsProfile, "aws-profile", constants.AWSDefaultCredential, "aws profile to use")
	cmd.Flags().BoolVar(&useSSHAgent, "use-ssh-agent", false, "use ssh agent(ex: Yubikey) for ssh auth")
	cmd.Flags().StringVar(&sshIdentity, "ssh-agent-identity", "", "use given ssh identity(only for ssh agent). If not set, default will be used")
	cmd.Flags().StringVar(&loadTestRepoURL, "loadTestRepoURL", "", "load test repo url to use")
	cmd.Flags().StringVar(&loadTestBuildCmd, "loadTestBuildCmd", "", "command to build load test binary")
	cmd.Flags().StringVar(&loadTestCmd, "loadTestCmd", "", "command to run load test")
	return cmd
}

func preLoadTestChecks(clusterName string) error {
	if err := checkCluster(clusterName); err != nil {
		return err
	}
	if useAWS && useGCP {
		return fmt.Errorf("could not use both AWS and GCP cloud options")
	}
	if !useAWS && awsProfile != constants.AWSDefaultCredential {
		return fmt.Errorf("could not use AWS profile for non AWS cloud option")
	}
	if sshIdentity != "" && !useSSHAgent {
		return fmt.Errorf("could not use ssh identity without using ssh agent")
	}
	if useSSHAgent && !utils.IsSSHAgentAvailable() {
		return fmt.Errorf("ssh agent is not available")
	}
	clusterNodes, err := getClusterNodes(clusterName)
	if err != nil {
		return err
	}
	if len(clusterNodes) == 0 {
		return fmt.Errorf("no nodes found for loadtesting in the cluster %s", clusterName)
	}
	return nil
}

func createLoadTest(_ *cobra.Command, args []string) error {
	clusterName := args[0]
	subnetName = args[1]
	if err := preLoadTestChecks(clusterName); err != nil {
		return err
	}
	var loadTestNodeConfig models.RegionConfig
	var loadTestCloudConfig models.CloudConfig
	var err error
	// set ssh-Key
	if useSSHAgent && sshIdentity == "" {
		sshIdentity, err = setSSHIdentity()
		if err != nil {
			return err
		}
	}
	clusterNodes, err := getClusterNodes(clusterName)
	if err != nil {
		return err
	}
	existingSeparateInstance, err = getExistingMonitoringInstance(clusterName)
	if err != nil {
		return err
	}
	cloudService := ""
	if existingSeparateInstance != "" {
		ux.Logger.PrintToUser("Will be using cloud instance %s to run load test...", existingSeparateInstance)
		separateNodeConfig, err := app.LoadClusterNodeConfig(existingSeparateInstance)
		if err != nil {
			return err
		}
		cloudService = separateNodeConfig.CloudService
	} else {
		ux.Logger.PrintToUser("Creating a separate instance to run load test...")
		cloudService, err = setCloudService()
		if err != nil {
			return err
		}
		nodeType = "default"
		nodeType, err = setCloudInstanceType(cloudService)
		if err != nil {
			return err
		}
	}
	separateHostRegion := ""
	cloudSecurityGroupList, err := getCloudSecurityGroupList(clusterNodes)
	if err != nil {
		return err
	}
	// we only support endpoint in the same cluster
	filteredSGList := utils.Filter(cloudSecurityGroupList, func(sg regionSecurityGroup) bool { return sg.cloud == cloudService })
	if len(filteredSGList) == 0 {
		return fmt.Errorf("no endpoint found in the  %s", cloudService)
	}
	sgRegions := []string{}
	for index := range filteredSGList {
		sgRegions = append(sgRegions, filteredSGList[index].region)
	}
	switch cloudService {
	case constants.AWSCloudService:
		var ec2SvcMap map[string]*awsAPI.AwsCloud
		var ami map[string]string
		loadTestEc2SvcMap := make(map[string]*awsAPI.AwsCloud)
		if existingSeparateInstance == "" {
			ec2SvcMap, ami, _, err = getAWSCloudConfig(awsProfile, true, sgRegions)
			if err != nil {
				return err
			}
			regions := maps.Keys(ec2SvcMap)
			separateHostRegion = regions[0]
			loadTestEc2SvcMap[separateHostRegion] = ec2SvcMap[separateHostRegion]
			loadTestCloudConfig, err = createAWSInstances(loadTestEc2SvcMap, nodeType, map[string]NumNodes{separateHostRegion: {1, 0}}, []string{separateHostRegion}, ami, true)
			if err != nil {
				return err
			}
			loadTestNodeConfig = loadTestCloudConfig[separateHostRegion]
		} else {
			loadTestNodeConfig, separateHostRegion, err = getNodeCloudConfig(existingSeparateInstance)
			if err != nil {
				return err
			}
			loadTestEc2SvcMap, err = getAWSMonitoringEC2Svc(awsProfile, separateHostRegion)
			if err != nil {
				return err
			}
		}
		if !useStaticIP {
			// get loadtest public
			loadTestPublicIPMap, err := loadTestEc2SvcMap[separateHostRegion].GetInstancePublicIPs(loadTestNodeConfig.InstanceIDs)
			if err != nil {
				return err
			}
			loadTestNodeConfig.PublicIPs = []string{loadTestPublicIPMap[loadTestNodeConfig.InstanceIDs[0]]}
		}
		if existingSeparateInstance == "" {
			for _, sg := range filteredSGList {
				if err = grantAccessToPublicIPViaSecurityGroup(ec2SvcMap[sg.region], loadTestNodeConfig.PublicIPs[0], sg.securityGroup, sg.region); err != nil {
					return err
				}
			}
		}
	case constants.GCPCloudService:
		var gcpClient *gcpAPI.GcpCloud
		var gcpRegions map[string]NumNodes
		var imageID string
		var projectName string
		if existingSeparateInstance == "" {
			// Get GCP Credential, zone, Image ID, service account key file path, and GCP project name
			gcpClient, gcpRegions, imageID, _, projectName, err = getGCPConfig(true)
			if err != nil {
				return err
			}
			regions := maps.Keys(gcpRegions)
			separateHostRegion = regions[0]
			loadTestCloudConfig, err = createGCPInstance(gcpClient, nodeType, map[string]NumNodes{separateHostRegion: {1, 0}}, imageID, clusterName, true)
			if err != nil {
				return err
			}
			loadTestNodeConfig = loadTestCloudConfig[separateHostRegion]
		} else {
			_, projectName, _, err = getGCPCloudCredentials()
			if err != nil {
				return err
			}
			loadTestNodeConfig, separateHostRegion, err = getNodeCloudConfig(existingSeparateInstance)
			if err != nil {
				return err
			}
		}
		if !useStaticIP {
			loadTestPublicIPMap, err := gcpClient.GetInstancePublicIPs(separateHostRegion, loadTestNodeConfig.InstanceIDs)
			if err != nil {
				return err
			}
			loadTestNodeConfig.PublicIPs = []string{loadTestPublicIPMap[loadTestNodeConfig.InstanceIDs[0]]}
		}
		if existingSeparateInstance == "" {
			if err = grantAccessToPublicIPViaFirewall(gcpClient, projectName, loadTestNodeConfig.PublicIPs[0], "loadtest"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("cloud service %s is not supported", cloudService)
	}
	if existingSeparateInstance == "" {
		if err := saveExternalHostConfig(loadTestNodeConfig, separateHostRegion, cloudService, clusterName); err != nil {
			return err
		}
	}
	var separateHosts []*models.Host
	separateHostInventoryPath := filepath.Join(app.GetAnsibleInventoryDirPath(clusterName), constants.MonitoringDir)
	if existingSeparateInstance == "" {
		if err = ansible.CreateAnsibleHostInventory(separateHostInventoryPath, loadTestNodeConfig.CertFilePath, cloudService, map[string]string{loadTestNodeConfig.InstanceIDs[0]: loadTestNodeConfig.PublicIPs[0]}, nil); err != nil {
			return err
		}
	}
	separateHosts, err = ansible.GetInventoryFromAnsibleInventoryFile(separateHostInventoryPath)
	if err != nil {
		return err
	}

	if err := GetLoadTestScript(app); err != nil {
		return err
	}

	// waiting for all nodes to become accessible
	if existingSeparateInstance == "" {
		failedHosts := waitForHosts(separateHosts)
		if failedHosts.Len() > 0 {
			for _, result := range failedHosts.GetResults() {
				ux.Logger.PrintToUser("Instance %s failed to provision with error %s. Please check instance logs for more information", result.NodeID, result.Err)
			}
			return fmt.Errorf("failed to provision node(s) %s", failedHosts.GetNodeList())
		}
		ux.Logger.PrintToUser("Separate instance %s provisioned successfully", separateHosts[0].NodeID)
	}

	subnetID, chainID, err := getDeployedSubnetInfo()
	if err != nil {
		return err
	}

	if err := createClusterYAMLFile(clusterName, subnetID, chainID, separateHosts[0]); err != nil {
		return err
	}

	if err := ssh.RunSSHCopyYAMLFile(separateHosts[0], app.GetClusterYAMLFilePath(clusterName)); err != nil {
		fmt.Printf("we have error here %s \n", err)
		return err
	}
	ux.Logger.PrintToUser("Setting up load test environment ...")
	if err := ssh.RunSSHBuildLoadTest(separateHosts[0], loadTestRepoURL, loadTestBuildCmd); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Successfully set up load test environment!")
	if err := ssh.RunSSHRunLoadTest(separateHosts[0], loadTestCmd); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Load test successfully run!")

	return nil
}

func getDeployedSubnetInfo() (string, string, error) {
	sidecarFile := filepath.Join(app.GetSubnetDir(), subnetName, constants.SidecarFileName)
	var sidecar models.Sidecar
	if _, err := os.Stat(sidecarFile); err == nil {
		// read in sidecar file
		jsonBytes, err := os.ReadFile(sidecarFile)
		if err != nil {
			return "", "", fmt.Errorf("failed reading file %s: %w", sidecarFile, err)
		}
		err = json.Unmarshal(jsonBytes, &sidecar)
		if err != nil {
			return "", "", fmt.Errorf("failed unmarshaling file %s: %w", sidecarFile, err)
		}
	}
	if sidecar.Networks != nil {
		model, ok := sidecar.Networks["Devnet"]
		if ok {
			if model.SubnetID != ids.Empty && model.BlockchainID != ids.Empty {
				return model.SubnetID.String(), model.BlockchainID.String(), nil
			}
		}
	}
	return "", "", fmt.Errorf("unable to find deployed Devnet info at cluster_config.json")
}

func createClusterYAMLFile(clusterName, subnetID, chainID string, separateHost *models.Host) error {
	clusterYAMLFilePath := filepath.Join(app.GetAnsibleInventoryDirPath(clusterName), constants.ClusterYAMLFileName)
	if _, err := os.Stat(clusterYAMLFilePath); err == nil {
		if err = os.Remove(clusterYAMLFilePath); err != nil {
			return err
		}
	}
	yamlFile, err := os.OpenFile(clusterYAMLFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, constants.WriteReadReadPerms)
	if err != nil {
		return err
	}
	defer yamlFile.Close()

	enc := yaml.NewEncoder(yamlFile)

	clusterInfoMap := make(map[string]string)
	clustersConfig := models.ClustersConfig{}
	if app.ClustersConfigExists() {
		clustersConfig, err = app.LoadClustersConfig()
		if err != nil {
			return err
		}
	}
	clusterConf := clustersConfig.Clusters[clusterName]
	if err := checkCluster(clusterName); err != nil {
		return err
	}
	validatorCount := 0
	apiNodeCount := 0
	for _, cloudID := range clusterConf.GetCloudIDs() {
		validatorIPEnvPrefix := "IP_VALIDATOR_"
		validatorCloudIDEnvPrefix := "CLOUD_ID_VALIDATOR_"
		validatorNodeIDEnvPrefix := "NODE_ID_VALIDATOR_"
		apiIPEnvPrefix := "IP_API_"
		apiCloudIDEnvPrefix := "CLOUD_ID_API_"
		nodeConfig, err := app.LoadClusterNodeConfig(cloudID)
		if err != nil {
			return err
		}
		nodeIDStr := ""
		if clusterConf.IsAvalancheGoHost(cloudID) {
			nodeID, err := getNodeID(app.GetNodeInstanceDirPath(cloudID))
			if err != nil {
				return err
			}
			nodeIDStr = nodeID.String()
		}
		roles := clusterConf.GetHostRoles(nodeConfig)
		switch roles[0] {
		case "Node":
			validatorIPEnvPrefix += strconv.Itoa(validatorCount)
			validatorCloudIDEnvPrefix += strconv.Itoa(validatorCount)
			validatorNodeIDEnvPrefix += strconv.Itoa(validatorCount)
			clusterInfoMap[validatorIPEnvPrefix] = nodeConfig.ElasticIP
			clusterInfoMap[validatorCloudIDEnvPrefix] = cloudID
			clusterInfoMap[validatorNodeIDEnvPrefix] = nodeIDStr
			validatorCount += 1
		case "API":
			apiIPEnvPrefix += strconv.Itoa(apiNodeCount)
			apiCloudIDEnvPrefix += strconv.Itoa(apiNodeCount)
			clusterInfoMap[apiIPEnvPrefix] = nodeConfig.ElasticIP
			clusterInfoMap[apiCloudIDEnvPrefix] = cloudID
			apiNodeCount += 1
		default:
		}

	}
	clusterInfoMap["SUBNET_ID"] = subnetID
	clusterInfoMap["CHAIN_ID"] = chainID
	clusterInfoMap["IP_MONITORING"] = separateHost.IP
	clusterInfoMap["CLOUD_ID_MONITORING"] = separateHost.GetCloudID()
	return enc.Encode(clusterInfoMap)
}

func GetLoadTestScript(app *application.Avalanche) error {
	var err error
	if loadTestRepoURL != "" {
		ux.Logger.PrintToUser("Checking source code repository URL %s", loadTestRepoURL)
		if err := prompts.ValidateURL(loadTestRepoURL); err != nil {
			ux.Logger.PrintToUser("Invalid repository url %s: %s", loadTestRepoURL, err)
			loadTestRepoURL = ""
		}
	}
	if loadTestRepoURL == "" {
		loadTestRepoURL, err = app.Prompt.CaptureURL("Source code repository URL")
		if err != nil {
			return err
		}
	}
	if loadTestBuildCmd == "" {
		loadTestBuildCmd, err = app.Prompt.CaptureString("What is the build command?")
		if err != nil {
			return err
		}
	}
	if loadTestCmd == "" {
		loadTestCmd, err = app.Prompt.CaptureString("What is the load test command?")
		if err != nil {
			return err
		}
	}
	return nil
}
