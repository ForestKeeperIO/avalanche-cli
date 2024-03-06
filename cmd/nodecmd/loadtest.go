// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package nodecmd

import (
	"fmt"
	"path/filepath"

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
	"github.com/spf13/cobra"
	"golang.org/x/exp/maps"
)

var (
	loadTestScriptPath string
	loadTestScriptArgs string
	loadTestRepoURL    string
	loadTestBuildCmd   string
	loadTestCmd        string
)

func newLoadTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "loadtest [clusterName]",
		Short: "(ALPHA Warning) Start loadtest for existing devnet cluster",
		Long: `(ALPHA Warning) This command is currently in experimental mode. 

The node loadtest command starts a loadtest run for the existing devnet cluster. It creates a separated cloud server and
this loadtest script will be run on the provisioned cloud server in the same cloud and region as the cluster.
Loadtest script will be run in ubuntu user home directory with the provided arguments.
After loadtest is done it will deliver generated reports if any along with loadtest logs before terminating used cloud server.`,

		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE:         createLoadTest,
	}
	cmd.Flags().BoolVar(&useAWS, "aws", false, "create loadtest node in AWS cloud")
	cmd.Flags().BoolVar(&useGCP, "gcp", false, "create loadtest in GCP cloud")
	cmd.Flags().StringVar(&nodeType, "node-type", "default", "cloud instance type for loadtest script")
	cmd.Flags().StringVar(&loadTestScriptPath, "loadtest-script-path", "", "loadtest script path")
	cmd.Flags().StringVar(&loadTestScriptArgs, "loadtest-script-args", "", "loadtest script arguments")
	cmd.Flags().BoolVar(&authorizeAccess, "authorize-access", false, "authorize CLI to create cloud resources")
	cmd.Flags().StringVar(&awsProfile, "aws-profile", constants.AWSDefaultCredential, "aws profile to use")
	cmd.Flags().BoolVar(&useSSHAgent, "use-ssh-agent", false, "use ssh agent(ex: Yubikey) for ssh auth")
	cmd.Flags().StringVar(&sshIdentity, "ssh-agent-identity", "", "use given ssh identity(only for ssh agent). If not set, default will be used")
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
	if err := preLoadTestChecks(clusterName); err != nil {
		return err
	}
	var loadTestNodeConfig models.RegionConfig
	var loadTestCloudConfig models.CloudConfig
	var err error
	if loadTestScriptPath == "" {
		loadTestScriptPath = "loadtest.sh"
	}
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
			loadTestCloudConfig, err = createAWSInstances(loadTestEc2SvcMap, nodeType, map[string]int{separateHostRegion: 1}, []string{separateHostRegion}, ami, true)
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
		var gcpRegions map[string]int
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
			loadTestCloudConfig, err = createGCPInstance(gcpClient, nodeType, map[string]int{separateHostRegion: 1}, imageID, clusterName, true)
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
	ux.Logger.PrintToUser("Building load test binary ...")
	if err := ssh.RunSSHBuildLoadTest(separateHosts[0], loadTestRepoURL, loadTestBuildCmd); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Successfully built load test binary!")
	ux.Logger.PrintToUser("Running load test ...")
	if err := ssh.RunSSHRunLoadTest(separateHosts[0], loadTestCmd); err != nil {
		return err
	}
	ux.Logger.PrintToUser("Load test successfully run!")
	return nil
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
