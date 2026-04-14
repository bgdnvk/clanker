package gcp

import (
	"context"
	"fmt"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	iam "cloud.google.com/go/iam/admin/apiv1"
	"cloud.google.com/go/iam/admin/apiv1/adminpb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/sqladmin/v1"
)

func gcpFallbackRegions() []string {
	return []string{
		"us-central1", "us-east1", "us-east4", "us-west1", "us-west2",
		"europe-west1", "europe-west2", "asia-east1", "asia-northeast1",
	}
}

func gcpFallbackZones() []string {
	return []string{
		"us-central1-a", "us-central1-b",
		"us-east1-b",
		"us-east4-a", "us-east4-b",
		"us-west1-a",
		"us-west2-a",
		"europe-west1-b",
	}
}

func shortGCPName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

func (c *Client) sdkFallbackSection(ctx context.Context, sectionName string) (string, error) {
	switch strings.TrimSpace(sectionName) {
	case "IAM Service Accounts":
		return c.sdkServiceAccounts(ctx)
	case "Cloud Run Services":
		return c.sdkCloudRunServices(ctx)
	case "Compute Instances":
		return c.sdkComputeInstances(ctx)
	case "VPC Networks":
		return c.sdkVPCNetworks(ctx)
	case "GKE Clusters":
		return c.sdkGKEClusters(ctx)
	case "Cloud SQL Instances":
		return c.sdkCloudSQLInstances(ctx)
	case "Cloud Storage Buckets":
		return c.sdkStorageBuckets(ctx)
	default:
		return "", nil
	}
}

func (c *Client) sdkServiceAccounts(ctx context.Context) (string, error) {
	client, err := iam.NewIamClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	it := client.ListServiceAccounts(ctx, &adminpb.ListServiceAccountsRequest{
		Name: fmt.Sprintf("projects/%s", c.projectID),
	})

	lines := make([]string, 0)
	for {
		account, nextErr := it.Next()
		if nextErr == iterator.Done {
			break
		}
		if nextErr != nil {
			return "", nextErr
		}
		lines = append(lines, fmt.Sprintf("%s\t%s", strings.TrimSpace(account.Email), strings.TrimSpace(account.DisplayName)))
	}

	return strings.Join(lines, "\n"), nil
}

func (c *Client) sdkCloudRunServices(ctx context.Context) (string, error) {
	client, err := run.NewServicesRESTClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	lines := make([]string, 0)
	for _, region := range gcpFallbackRegions() {
		parent := fmt.Sprintf("projects/%s/locations/%s", c.projectID, region)
		it := client.ListServices(ctx, &runpb.ListServicesRequest{Parent: parent})
		for {
			service, nextErr := it.Next()
			if nextErr == iterator.Done {
				break
			}
			if nextErr != nil {
				return "", nextErr
			}
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s", shortGCPName(service.GetName()), region, strings.TrimSpace(service.GetUri())))
		}
	}

	return strings.Join(lines, "\n"), nil
}

func (c *Client) sdkComputeInstances(ctx context.Context) (string, error) {
	client, err := compute.NewInstancesRESTClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	lines := make([]string, 0)
	for _, zone := range gcpFallbackZones() {
		it := client.List(ctx, &computepb.ListInstancesRequest{Project: c.projectID, Zone: zone})
		for {
			instance, nextErr := it.Next()
			if nextErr == iterator.Done {
				break
			}
			if nextErr != nil {
				break
			}

			privateIP := ""
			publicIP := ""
			for _, ni := range instance.GetNetworkInterfaces() {
				if privateIP == "" {
					privateIP = strings.TrimSpace(ni.GetNetworkIP())
				}
				for _, ac := range ni.GetAccessConfigs() {
					if publicIP == "" {
						publicIP = strings.TrimSpace(ac.GetNatIP())
					}
				}
			}

			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s", instance.GetName(), zone, instance.GetStatus(), privateIP, publicIP))
		}
	}

	return strings.Join(lines, "\n"), nil
}

func (c *Client) sdkVPCNetworks(ctx context.Context) (string, error) {
	client, err := compute.NewNetworksRESTClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	it := client.List(ctx, &computepb.ListNetworksRequest{Project: c.projectID})
	lines := make([]string, 0)
	for {
		network, nextErr := it.Next()
		if nextErr == iterator.Done {
			break
		}
		if nextErr != nil {
			return "", nextErr
		}
		mode := "custom"
		if network.GetAutoCreateSubnetworks() {
			mode = "auto"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s", network.GetName(), mode, strings.TrimSpace(network.GetDescription())))
	}

	return strings.Join(lines, "\n"), nil
}

func (c *Client) sdkGKEClusters(ctx context.Context) (string, error) {
	client, err := container.NewClusterManagerRESTClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	lines := make([]string, 0)
	for _, region := range gcpFallbackRegions() {
		parent := fmt.Sprintf("projects/%s/locations/%s", c.projectID, region)
		resp, listErr := client.ListClusters(ctx, &containerpb.ListClustersRequest{Parent: parent})
		if listErr != nil {
			continue
		}
		for _, cluster := range resp.Clusters {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", cluster.GetName(), cluster.GetLocation(), cluster.GetStatus().String(), cluster.GetCurrentMasterVersion()))
		}
	}

	return strings.Join(lines, "\n"), nil
}

func (c *Client) sdkCloudSQLInstances(ctx context.Context) (string, error) {
	service, err := sqladmin.NewService(ctx)
	if err != nil {
		return "", err
	}

	resp, err := service.Instances.List(c.projectID).Context(ctx).Do()
	if err != nil {
		return "", err
	}

	lines := make([]string, 0, len(resp.Items))
	for _, instance := range resp.Items {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", instance.Name, instance.Region, strings.TrimSpace(instance.DatabaseVersion), strings.TrimSpace(instance.State)))
	}

	return strings.Join(lines, "\n"), nil
}

func (c *Client) sdkStorageBuckets(ctx context.Context) (string, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	it := client.Buckets(ctx, c.projectID)
	lines := make([]string, 0)
	for {
		bucket, nextErr := it.Next()
		if nextErr == iterator.Done {
			break
		}
		if nextErr != nil {
			return "", nextErr
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s", bucket.Name, bucket.Location, bucket.StorageClass))
	}

	return strings.Join(lines, "\n"), nil
}
