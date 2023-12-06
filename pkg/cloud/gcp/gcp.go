// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package gcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/exp/slices"

	"google.golang.org/api/compute/v1"

	"github.com/ava-labs/avalanche-cli/pkg/constants"
	"github.com/ava-labs/avalanche-cli/pkg/models"
	"github.com/ava-labs/avalanche-cli/pkg/ux"
)

type GcpCloud struct {
	gcpClient *compute.Service
	ctx       context.Context
	projectID string
}

func NewGcpCloud(gcpClient *compute.Service, projectID string, ctx context.Context) (*GcpCloud, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return &GcpCloud{
		gcpClient: gcpClient,
		projectID: projectID,
		ctx:       ctx,
	}, nil
}

// waitForOperation waits for a Google Cloud operation to complete.
func (c *GcpCloud) waitForOperation(operation *compute.Operation, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		// Get the status of the operation
		getOperation, err := c.gcpClient.GlobalOperations.Get(c.projectID, operation.Name).Do()
		if err != nil {
			return fmt.Errorf("error getting operation status: %v", err)
		}

		// Check if the operation has completed
		if getOperation.Status == "DONE" {
			if getOperation.Error != nil {
				return fmt.Errorf("operation failed: %v", getOperation.Error)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("operation did not complete within the specified timeout")
		}
		// Wait before checking the status again
		select {
		case <-c.ctx.Done():
			return fmt.Errorf("operation canceled")
		case <-time.After(1 * time.Second):
		}
	}
}

// SetExistingNetwork uses existing network in GCP
func (c *GcpCloud) SetExistingNetwork(networkName string) (*compute.Network, error) {
	network, err := c.gcpClient.Networks.Get(c.projectID, networkName).Do()
	if err != nil {
		return nil, fmt.Errorf("error getting network %s: %v", networkName, err)
	}
	return network, nil
}

// SetNetwork creates a new network in GCP
func (c *GcpCloud) SetupNetwork(ipAddress, networkName string) (*compute.Network, error) {
	insertOp, err := c.gcpClient.Networks.Insert(c.projectID, &compute.Network{
		Name: networkName,
	}).Do()
	if err != nil {
		return nil, fmt.Errorf("error creating network %s: %v", networkName, err)
	}
	if err := c.waitForOperation(insertOp, constants.CloudOperationTimeout); err != nil {
		return nil, err
	}
	// Retrieve the created firewall
	createdNetwork, err := c.gcpClient.Networks.Get(c.projectID, networkName).Do()
	if err != nil {
		return nil, fmt.Errorf("error retrieving created networks %s: %v", networkName, err)
	}

	// Create firewall rules
	if _, err := c.SetFirewallRule("0.0.0.0/0", fmt.Sprintf("%s-%s", networkName, "default"), networkName, []string{strconv.Itoa(constants.AvalanchegoP2PPort)}); err != nil {
		return nil, err
	}
	if _, err := c.SetFirewallRule(ipAddress+"/32", fmt.Sprintf("%s-%s", networkName, strings.ReplaceAll(ipAddress, ".", "")), networkName, []string{strconv.Itoa(constants.SSHTCPPort), strconv.Itoa(constants.AvalanchegoAPIPort)}); err != nil {
		return nil, err
	}

	return createdNetwork, nil
}

// SetFirewallRule creates a new firewall rule in GCP
func (c *GcpCloud) SetFirewallRule(ipAddress, firewallName, networkName string, ports []string) (*compute.Firewall, error) {
	firewall := &compute.Firewall{
		Name:    firewallName,
		Network: fmt.Sprintf("projects/%s/global/networks/%s", c.projectID, networkName),
		Allowed: []*compute.FirewallAllowed{{IPProtocol: "tcp", Ports: ports}},
		SourceRanges: []string{
			ipAddress,
		},
	}

	insertOp, err := c.gcpClient.Firewalls.Insert(c.projectID, firewall).Do()
	if err != nil {
		return nil, fmt.Errorf("error creating firewall rule %s: %v", firewallName, err)
	}
	if err := c.waitForOperation(insertOp, constants.CloudOperationTimeout); err != nil {
		return nil, err
	}
	return c.gcpClient.Firewalls.Get(c.projectID, firewallName).Do()
}

// SetPublicIP creates a static IP in GCP
func (c *GcpCloud) SetPublicIP(region, nodeName string, numNodes int) ([]string, []string, error) {
	publicIPName := []string{}
	publicIP := []string{}
	for i := 0; i < numNodes; i++ {
		staticIPName := fmt.Sprintf("%s-%s", "GCPStaticIPPrefix", nodeName)
		address := &compute.Address{
			Name:        staticIPName,
			AddressType: "EXTERNAL",
			NetworkTier: "PREMIUM",
		}

		insertOp, err := c.gcpClient.Addresses.Insert(c.projectID, region, address).Do()
		if err != nil {
			return nil, nil, fmt.Errorf("error creating static IP %s: %v", staticIPName, err)
		}
		if err := c.waitForOperation(insertOp, constants.CloudOperationTimeout); err != nil {
			return nil, nil, err
		}
		PublicIP, err := c.gcpClient.Addresses.Get(c.projectID, region, staticIPName).Do()
		if err != nil {
			return nil, nil, fmt.Errorf("error retrieving created static IP %s: %v", staticIPName, err)
		}
		publicIPName = append(publicIPName, PublicIP.Name)
		publicIP = append(publicIP, PublicIP.Address)
	}

	return publicIPName, publicIP, nil
}

// SetupInstances creates GCP instances
func (c *GcpCloud) SetupInstances(zone, networkName, sshPublicKey, ami string, staticIPName []string, instancePrefix string, numNodes int, instanceType string) ([]*compute.Instance, error) {
	if len(staticIPName) != numNodes {
		return nil, fmt.Errorf("len(staticIPName) != numNodes")
	}
	instances := make([]*compute.Instance, numNodes)
	sshKey := fmt.Sprintf("ubuntu:%s", strings.TrimSuffix(sshPublicKey, "\n"))
	automaticRestart := true
	for i := 0; i < numNodes; i++ {
		instanceName := fmt.Sprintf("%s-%d", instancePrefix, i)
		instance := &compute.Instance{
			Name:        instanceName,
			MachineType: fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", c.projectID, zone, instanceType),
			Metadata: &compute.Metadata{
				Items: []*compute.MetadataItems{
					{Key: "ssh-keys", Value: &sshKey},
				},
			},
			NetworkInterfaces: []*compute.NetworkInterface{
				{
					Network: fmt.Sprintf("projects/%s/global/networks/%s", c.projectID, networkName),
					AccessConfigs: []*compute.AccessConfig{
						{
							Name: "External NAT",
						},
					},
				},
			},
			Disks: []*compute.AttachedDisk{
				{
					InitializeParams: &compute.AttachedDiskInitializeParams{
						DiskSizeGb: 1000,
					},
					Boot:       true, // Set this if it's the boot disk
					AutoDelete: true,
					Source:     ami, // Specify the source image here
				},
			},
			Scheduling: &compute.Scheduling{
				AutomaticRestart: &automaticRestart,
			},
		}
		if staticIPName != nil {
			instance.NetworkInterfaces[0].AccessConfigs[0].NatIP = staticIPName[i]
		}

		insertOp, err := c.gcpClient.Instances.Insert(c.projectID, zone, instance).Do()
		if err != nil {
			return nil, fmt.Errorf("error creating instance %s: %v", instanceName, err)
		}
		if err := c.waitForOperation(insertOp, constants.CloudOperationTimeout); err != nil {
			return nil, err
		}
		instances[i], err = c.gcpClient.Instances.Get(c.projectID, zone, instanceName).Do()
		if err != nil {
			return nil, fmt.Errorf("error retrieving created instance %s: %v", instanceName, err)
		}
	}
	return instances, nil
}

// // Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// // See the file LICENSE for licensing terms.

func (c *GcpCloud) GetUbuntuImageID() (string, error) {
	imageListCall := c.gcpClient.Images.List(constants.GCPDefaultImageProvider).Filter(constants.GCPImageFilter)
	imageList, err := imageListCall.Do()
	if err != nil {
		return "", err
	}
	imageID := ""
	for _, image := range imageList.Items {
		if image.Deprecated == nil {
			imageID = image.Name
			break
		}
	}
	return imageID, nil
}

// CheckFirewallExists checks that firewall firewallName exists in GCP project projectName
func (c *GcpCloud) CheckFirewallExists(firewallName string) (bool, error) {
	firewallListCall := c.gcpClient.Firewalls.List(c.projectID)
	firewallList, err := firewallListCall.Do()
	if err != nil {
		return false, err
	}
	for _, firewall := range firewallList.Items {
		if firewall.Name == firewallName {
			return true, nil
		}
	}
	return false, nil
}

// CheckNetworkExists checks that network networkName exists in GCP project projectName
func (c *GcpCloud) CheckNetworkExists(networkName string) (bool, error) {
	networkListCall := c.gcpClient.Networks.List(c.projectID)
	networkList, err := networkListCall.Do()
	if err != nil {
		return false, err
	}
	for _, network := range networkList.Items {
		if network.Name == networkName {
			return true, nil
		}
	}
	return false, nil
}

// GetInstancePublicIPs gets public IP(s) of GCP instance(s) without static IP and returns a map
// with gcp instance id as key and public ip as value
func (c *GcpCloud) GetInstancePublicIPs(zone string, nodeIDs []string) (map[string]string, error) {
	instancesListCall := c.gcpClient.Instances.List(c.projectID, zone)
	instancesList, err := instancesListCall.Do()
	if err != nil {
		return nil, err
	}

	instanceIDToIP := make(map[string]string)
	for _, instance := range instancesList.Items {
		if slices.Contains(nodeIDs, instance.Name) {
			if len(instance.NetworkInterfaces) > 0 && len(instance.NetworkInterfaces[0].AccessConfigs) > 0 {
				instanceIDToIP[instance.Name] = instance.NetworkInterfaces[0].AccessConfigs[0].NatIP
			}
		}
	}
	return instanceIDToIP, nil
}

// checkInstanceIsRunning checks that GCP instance nodeID is running in GCP
func (c *GcpCloud) checkInstanceIsRunning(zone, nodeID string) (bool, error) {
	instanceGetCall := c.gcpClient.Instances.Get(c.projectID, zone, nodeID)
	instance, err := instanceGetCall.Do()
	if err != nil {
		return false, err
	}
	if instance.Status != "RUNNING" {
		return false, fmt.Errorf("error %s is not running", nodeID)
	}
	return true, nil
}

func (c *GcpCloud) StopGCPNode(nodeConfig models.NodeConfig, clusterName string, releasePublicIP bool) error {
	isRunning, err := c.checkInstanceIsRunning(nodeConfig.Region, nodeConfig.NodeID)
	if err != nil {
		return err
	}
	if !isRunning {
		noRunningNodeErr := fmt.Errorf("no running node with instance id %s is found in cluster %s", nodeConfig.NodeID, clusterName)
		return noRunningNodeErr
	}
	ux.Logger.PrintToUser(fmt.Sprintf("Stopping node instance %s in cluster %s...", nodeConfig.NodeID, clusterName))
	instancesStopCall := c.gcpClient.Instances.Stop(c.projectID, nodeConfig.Region, nodeConfig.NodeID)
	if _, err = instancesStopCall.Do(); err != nil {
		return err
	}
	if releasePublicIP && nodeConfig.ElasticIP != "" {
		ux.Logger.PrintToUser(fmt.Sprintf("Releasing static IP address %s ...", nodeConfig.ElasticIP))
		// GCP node region is stored in format of "us-east1-b", we need "us-east1"
		region := strings.Join(strings.Split(nodeConfig.Region, "-")[:2], "-")
		addressReleaseCall := c.gcpClient.Addresses.Delete(c.projectID, region, fmt.Sprintf("%s-%s", constants.GCPStaticIPPrefix, nodeConfig.NodeID))
		if _, err = addressReleaseCall.Do(); err != nil {
			return fmt.Errorf("%s, %w", constants.ErrReleasingGCPStaticIP, err)
		}
	}
	return nil
}
