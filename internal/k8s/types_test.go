package k8s

import (
	"encoding/json"
	"testing"
)

func TestClusterResourcesJSON(t *testing.T) {
	resources := ClusterResources{
		ClusterName: "test-cluster",
		ClusterARN:  "arn:aws:eks:us-east-1:123456789:cluster/test-cluster",
		Region:      "us-east-1",
		Status:      "ACTIVE",
		Nodes: []ClusterNodeInfo{
			{
				Name:       "node-1",
				Role:       "worker",
				Status:     "Ready",
				InternalIP: "10.0.1.10",
				ExternalIP: "54.123.45.67",
			},
		},
		Pods: []ClusterPodInfo{
			{
				Name:      "nginx-pod",
				Namespace: "default",
				Status:    "Running",
				Phase:     "Running",
				Ready:     "1/1",
				Restarts:  0,
				IP:        "10.0.1.20",
				Node:      "node-1",
			},
		},
		Services: []ClusterServiceInfo{
			{
				Name:      "nginx-svc",
				Namespace: "default",
				Type:      "ClusterIP",
				ClusterIP: "10.100.1.1",
			},
		},
		PVs:        []ClusterPVInfo{},
		PVCs:       []ClusterPVCInfo{},
		ConfigMaps: []ClusterConfigMapInfo{},
		Ingresses:  []ClusterIngressInfo{},
	}

	data, err := json.Marshal(resources)
	if err != nil {
		t.Fatalf("failed to marshal ClusterResources: %v", err)
	}

	var unmarshaled ClusterResources
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal ClusterResources: %v", err)
	}

	if unmarshaled.ClusterName != resources.ClusterName {
		t.Errorf("ClusterName = %v, want %v", unmarshaled.ClusterName, resources.ClusterName)
	}
	if unmarshaled.Region != resources.Region {
		t.Errorf("Region = %v, want %v", unmarshaled.Region, resources.Region)
	}
	if unmarshaled.Status != resources.Status {
		t.Errorf("Status = %v, want %v", unmarshaled.Status, resources.Status)
	}
	if len(unmarshaled.Nodes) != 1 {
		t.Errorf("Nodes length = %v, want 1", len(unmarshaled.Nodes))
	}
	if len(unmarshaled.Pods) != 1 {
		t.Errorf("Pods length = %v, want 1", len(unmarshaled.Pods))
	}
}

func TestMultiClusterResourcesJSON(t *testing.T) {
	cluster1 := ClusterResources{
		ClusterName: "cluster-1",
		Region:      "us-east-1",
		Status:      "ACTIVE",
		Nodes: []ClusterNodeInfo{
			{
				Name:       "node-1",
				Role:       "worker",
				Status:     "Ready",
				InternalIP: "10.0.1.10",
			},
		},
		Pods:       []ClusterPodInfo{},
		Services:   []ClusterServiceInfo{},
		PVs:        []ClusterPVInfo{},
		PVCs:       []ClusterPVCInfo{},
		ConfigMaps: []ClusterConfigMapInfo{},
	}

	cluster2 := ClusterResources{
		ClusterName: "cluster-2",
		Region:      "us-west-2",
		Status:      "ACTIVE",
		Nodes: []ClusterNodeInfo{
			{
				Name:       "node-2",
				Role:       "worker",
				Status:     "Ready",
				InternalIP: "10.0.2.10",
			},
		},
		Pods:       []ClusterPodInfo{},
		Services:   []ClusterServiceInfo{},
		PVs:        []ClusterPVInfo{},
		PVCs:       []ClusterPVCInfo{},
		ConfigMaps: []ClusterConfigMapInfo{},
	}

	multi := MultiClusterResources{
		Clusters: []ClusterResources{cluster1, cluster2},
	}

	data, err := json.Marshal(multi)
	if err != nil {
		t.Fatalf("failed to marshal MultiClusterResources: %v", err)
	}

	// Verify structure
	var rawJSON map[string]any
	if err := json.Unmarshal(data, &rawJSON); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	clusters, ok := rawJSON["clusters"].([]any)
	if !ok {
		t.Fatal("expected 'clusters' key with array value")
	}
	if len(clusters) != 2 {
		t.Errorf("expected 2 clusters, got %d", len(clusters))
	}

	// Unmarshal back to struct
	var unmarshaled MultiClusterResources
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal MultiClusterResources: %v", err)
	}

	if len(unmarshaled.Clusters) != 2 {
		t.Errorf("Clusters length = %v, want 2", len(unmarshaled.Clusters))
	}
	if unmarshaled.Clusters[0].ClusterName != "cluster-1" {
		t.Errorf("Clusters[0].ClusterName = %v, want cluster-1", unmarshaled.Clusters[0].ClusterName)
	}
	if unmarshaled.Clusters[1].ClusterName != "cluster-2" {
		t.Errorf("Clusters[1].ClusterName = %v, want cluster-2", unmarshaled.Clusters[1].ClusterName)
	}
}

func TestSingleClusterOutputVsMultiCluster(t *testing.T) {
	// Test that single cluster output has direct fields (no wrapper)
	singleCluster := ClusterResources{
		ClusterName: "test-cluster",
		Region:      "us-east-1",
		Status:      "ACTIVE",
		Nodes:       []ClusterNodeInfo{},
		Pods:        []ClusterPodInfo{},
		Services:    []ClusterServiceInfo{},
		PVs:         []ClusterPVInfo{},
		PVCs:        []ClusterPVCInfo{},
		ConfigMaps:  []ClusterConfigMapInfo{},
	}

	singleData, err := json.Marshal(singleCluster)
	if err != nil {
		t.Fatalf("failed to marshal single cluster: %v", err)
	}

	var singleJSON map[string]any
	if err := json.Unmarshal(singleData, &singleJSON); err != nil {
		t.Fatalf("failed to unmarshal single cluster: %v", err)
	}

	// Single cluster should have clusterName directly in the root
	if _, ok := singleJSON["clusterName"]; !ok {
		t.Error("single cluster output should have 'clusterName' at root level")
	}
	if _, ok := singleJSON["clusters"]; ok {
		t.Error("single cluster output should NOT have 'clusters' wrapper")
	}

	// Test that multi-cluster output has clusters wrapper
	multiCluster := MultiClusterResources{
		Clusters: []ClusterResources{singleCluster},
	}

	multiData, err := json.Marshal(multiCluster)
	if err != nil {
		t.Fatalf("failed to marshal multi cluster: %v", err)
	}

	var multiJSON map[string]any
	if err := json.Unmarshal(multiData, &multiJSON); err != nil {
		t.Fatalf("failed to unmarshal multi cluster: %v", err)
	}

	// Multi cluster should have clusters array at root
	if _, ok := multiJSON["clusters"]; !ok {
		t.Error("multi cluster output should have 'clusters' wrapper")
	}
	if _, ok := multiJSON["clusterName"]; ok {
		t.Error("multi cluster output should NOT have 'clusterName' at root level")
	}
}

func TestEmptyMultiClusterResources(t *testing.T) {
	multi := MultiClusterResources{
		Clusters: []ClusterResources{},
	}

	data, err := json.Marshal(multi)
	if err != nil {
		t.Fatalf("failed to marshal empty MultiClusterResources: %v", err)
	}

	expected := `{"clusters":[]}`
	if string(data) != expected {
		t.Errorf("got %s, want %s", string(data), expected)
	}
}

func TestClusterNodeInfo(t *testing.T) {
	node := ClusterNodeInfo{
		Name:       "ip-10-0-1-10.ec2.internal",
		Role:       "worker",
		Status:     "Ready",
		InternalIP: "10.0.1.10",
		ExternalIP: "54.123.45.67",
		InstanceID: "i-1234567890abcdef0",
		Labels: map[string]string{
			"node.kubernetes.io/instance-type": "t3.small",
			"topology.kubernetes.io/zone":      "us-east-1a",
		},
	}

	data, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("failed to marshal ClusterNodeInfo: %v", err)
	}

	var unmarshaled ClusterNodeInfo
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal ClusterNodeInfo: %v", err)
	}

	if unmarshaled.Name != node.Name {
		t.Errorf("Name = %v, want %v", unmarshaled.Name, node.Name)
	}
	if unmarshaled.Role != node.Role {
		t.Errorf("Role = %v, want %v", unmarshaled.Role, node.Role)
	}
	if unmarshaled.Labels["node.kubernetes.io/instance-type"] != "t3.small" {
		t.Errorf("Labels not preserved correctly")
	}
}

func TestClusterPodInfo(t *testing.T) {
	pod := ClusterPodInfo{
		Name:      "nginx-deployment-abc123",
		Namespace: "default",
		Status:    "Running",
		Phase:     "Running",
		Ready:     "1/1",
		Restarts:  0,
		IP:        "10.0.1.50",
		Node:      "node-1",
		Labels: map[string]string{
			"app": "nginx",
		},
		Containers: []ClusterContainerInfo{
			{
				Name:         "nginx",
				Image:        "nginx:latest",
				Ready:        true,
				RestartCount: 0,
				State:        "running",
			},
		},
		Volumes: []ClusterPodVolumeInfo{
			{
				Name:      "config",
				Type:      "configMap",
				Source:    "nginx-config",
				MountPath: "/etc/nginx/conf.d",
			},
		},
	}

	data, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("failed to marshal ClusterPodInfo: %v", err)
	}

	var unmarshaled ClusterPodInfo
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal ClusterPodInfo: %v", err)
	}

	if unmarshaled.Name != pod.Name {
		t.Errorf("Name = %v, want %v", unmarshaled.Name, pod.Name)
	}
	if len(unmarshaled.Containers) != 1 {
		t.Errorf("Containers length = %v, want 1", len(unmarshaled.Containers))
	}
	if len(unmarshaled.Volumes) != 1 {
		t.Errorf("Volumes length = %v, want 1", len(unmarshaled.Volumes))
	}
}

func TestClusterServiceInfo(t *testing.T) {
	service := ClusterServiceInfo{
		Name:                "nginx-service",
		Namespace:           "default",
		Type:                "LoadBalancer",
		ClusterIP:           "10.100.1.1",
		ExternalIP:          "54.123.45.67",
		LoadBalancerIngress: []string{"abc123.us-east-1.elb.amazonaws.com"},
		Ports: []ClusterServicePortInfo{
			{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: "8080",
				NodePort:   30080,
			},
		},
		Selector: map[string]string{
			"app": "nginx",
		},
		Labels: map[string]string{
			"service": "web",
		},
	}

	data, err := json.Marshal(service)
	if err != nil {
		t.Fatalf("failed to marshal ClusterServiceInfo: %v", err)
	}

	var unmarshaled ClusterServiceInfo
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal ClusterServiceInfo: %v", err)
	}

	if unmarshaled.Name != service.Name {
		t.Errorf("Name = %v, want %v", unmarshaled.Name, service.Name)
	}
	if unmarshaled.Type != "LoadBalancer" {
		t.Errorf("Type = %v, want LoadBalancer", unmarshaled.Type)
	}
	if len(unmarshaled.LoadBalancerIngress) != 1 {
		t.Errorf("LoadBalancerIngress length = %v, want 1", len(unmarshaled.LoadBalancerIngress))
	}
}

func TestClusterIngressInfo(t *testing.T) {
	ingress := ClusterIngressInfo{
		Name:             "nginx-ingress",
		Namespace:        "default",
		IngressClassName: "nginx",
		Hosts:            []string{"example.com", "www.example.com"},
		Address:          []string{"192.168.1.100"},
		Rules: []ClusterIngressRuleInfo{
			{
				Host:        "example.com",
				Path:        "/",
				ServiceName: "nginx-service",
				ServicePort: "80",
			},
		},
	}

	data, err := json.Marshal(ingress)
	if err != nil {
		t.Fatalf("failed to marshal ClusterIngressInfo: %v", err)
	}

	var unmarshaled ClusterIngressInfo
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal ClusterIngressInfo: %v", err)
	}

	if unmarshaled.Name != ingress.Name {
		t.Errorf("Name = %v, want %v", unmarshaled.Name, ingress.Name)
	}
	if len(unmarshaled.Hosts) != 2 {
		t.Errorf("Hosts length = %v, want 2", len(unmarshaled.Hosts))
	}
	if len(unmarshaled.Rules) != 1 {
		t.Errorf("Rules length = %v, want 1", len(unmarshaled.Rules))
	}
}
