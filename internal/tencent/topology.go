package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	cdb "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cdb/v20170320"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	postgres "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/postgres/v20170312"
	tke "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20180525"
	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

// Topology is the flat list-of-lists shape returned by /api/v1/tencent/topology.
// The frontend joins resources via the *ID fields. This is the simplest
// structure that supports both region-scoped views (group CVMs by subnet) and
// orphan detection (any CVM whose subnet_id is empty).
type Topology struct {
	Region         string             `json:"region"`
	VPCs           []TopologyVPC      `json:"vpcs"`
	Subnets        []TopologySubnet   `json:"subnets"`
	CVMs           []TopologyCVM      `json:"cvms"`
	SecurityGroups []TopologySG       `json:"security_groups"`
	MySQL          []TopologyDB       `json:"mysql"`
	Postgres       []TopologyDB       `json:"postgres"`
	Clusters       []TopologyCluster  `json:"clusters"`
	Warnings       []string           `json:"warnings,omitempty"`
}

type TopologyVPC struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CIDR      string `json:"cidr"`
	IsDefault bool   `json:"is_default"`
}

type TopologySubnet struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	CIDR  string `json:"cidr"`
	Zone  string `json:"zone"`
	VpcID string `json:"vpc_id"`
}

type TopologyCVM struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	State     string   `json:"state"`
	Type      string   `json:"type"`
	Zone      string   `json:"zone,omitempty"`
	PrivateIP string   `json:"private_ip,omitempty"`
	PublicIP  string   `json:"public_ip,omitempty"`
	VpcID     string   `json:"vpc_id,omitempty"`
	SubnetID  string   `json:"subnet_id,omitempty"`
	SGIDs     []string `json:"sg_ids,omitempty"`
}

type TopologySG struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsDefault   bool   `json:"is_default"`
}

type TopologyDB struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status,omitempty"`
	Engine  string `json:"engine,omitempty"`
	VpcID   string `json:"vpc_id,omitempty"` // empty for classic-network instances
	Zone    string `json:"zone,omitempty"`
}

type TopologyCluster struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status,omitempty"`
	Version  string `json:"k8s_version,omitempty"`
	NodeNum  uint64 `json:"node_num,omitempty"`
	VpcID    string `json:"vpc_id,omitempty"`
}

// TopologyJSON fetches every resource type concurrently for one region and
// returns the assembled topology as a JSON string. Errors per resource type
// are collected as warnings — a partial topology is more useful than nothing.
func (c *Client) TopologyJSON(ctx context.Context, region string) (string, error) {
	if strings.TrimSpace(region) != "" {
		c = c.WithRegion(region)
	}
	t := Topology{Region: c.Region()}

	var wg sync.WaitGroup
	var mu sync.Mutex
	warn := func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
		t.Warnings = append(t.Warnings, fmt.Sprintf("%s: %v", name, err))
	}

	wg.Add(7)

	go func() {
		defer wg.Done()
		v, err := c.topoVPCs()
		if err != nil {
			warn("vpcs", err)
			return
		}
		mu.Lock()
		t.VPCs = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v, err := c.topoSubnets()
		if err != nil {
			warn("subnets", err)
			return
		}
		mu.Lock()
		t.Subnets = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v, err := c.topoCVMs()
		if err != nil {
			warn("cvms", err)
			return
		}
		mu.Lock()
		t.CVMs = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v, err := c.topoSGs()
		if err != nil {
			warn("security_groups", err)
			return
		}
		mu.Lock()
		t.SecurityGroups = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v, err := c.topoMySQL()
		if err != nil {
			warn("mysql", err)
			return
		}
		mu.Lock()
		t.MySQL = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v, err := c.topoPostgres()
		if err != nil {
			warn("postgres", err)
			return
		}
		mu.Lock()
		t.Postgres = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v, err := c.topoClusters()
		if err != nil {
			warn("clusters", err)
			return
		}
		mu.Lock()
		t.Clusters = v
		mu.Unlock()
	}()
	wg.Wait()

	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c *Client) topoVPCs() ([]TopologyVPC, error) {
	cl, err := c.VPC()
	if err != nil {
		return nil, err
	}
	req := vpc.NewDescribeVpcsRequest()
	offsetStr, limitStr := "0", "100"
	req.Offset = &offsetStr
	req.Limit = &limitStr
	resp, err := cl.DescribeVpcs(req)
	if err != nil {
		return nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	out := make([]TopologyVPC, 0, len(resp.Response.VpcSet))
	for _, v := range resp.Response.VpcSet {
		out = append(out, TopologyVPC{
			ID:        derefStringRaw(v.VpcId),
			Name:      derefStringRaw(v.VpcName),
			CIDR:      derefStringRaw(v.CidrBlock),
			IsDefault: derefBool(v.IsDefault),
		})
	}
	return out, nil
}

func (c *Client) topoSubnets() ([]TopologySubnet, error) {
	cl, err := c.VPC()
	if err != nil {
		return nil, err
	}
	resp, err := cl.DescribeSubnets(vpc.NewDescribeSubnetsRequest())
	if err != nil {
		return nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	out := make([]TopologySubnet, 0, len(resp.Response.SubnetSet))
	for _, s := range resp.Response.SubnetSet {
		out = append(out, TopologySubnet{
			ID:    derefStringRaw(s.SubnetId),
			Name:  derefStringRaw(s.SubnetName),
			CIDR:  derefStringRaw(s.CidrBlock),
			Zone:  derefStringRaw(s.Zone),
			VpcID: derefStringRaw(s.VpcId),
		})
	}
	return out, nil
}

func (c *Client) topoCVMs() ([]TopologyCVM, error) {
	cl, err := c.CVM()
	if err != nil {
		return nil, err
	}
	req := cvm.NewDescribeInstancesRequest()
	var offset, limit int64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cl.DescribeInstances(req)
	if err != nil {
		return nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	out := make([]TopologyCVM, 0, len(resp.Response.InstanceSet))
	for _, in := range resp.Response.InstanceSet {
		row := TopologyCVM{
			ID:        derefStringRaw(in.InstanceId),
			Name:      derefStringRaw(in.InstanceName),
			State:     derefStringRaw(in.InstanceState),
			Type:      derefStringRaw(in.InstanceType),
			PrivateIP: firstIP(in.PrivateIpAddresses),
			PublicIP:  firstIP(in.PublicIpAddresses),
			SGIDs:     stringSlice(in.SecurityGroupIds),
		}
		if in.Placement != nil {
			row.Zone = derefStringRaw(in.Placement.Zone)
		}
		if in.VirtualPrivateCloud != nil {
			row.VpcID = derefStringRaw(in.VirtualPrivateCloud.VpcId)
			row.SubnetID = derefStringRaw(in.VirtualPrivateCloud.SubnetId)
		}
		out = append(out, row)
	}
	return out, nil
}

func (c *Client) topoSGs() ([]TopologySG, error) {
	cl, err := c.VPC()
	if err != nil {
		return nil, err
	}
	resp, err := cl.DescribeSecurityGroups(vpc.NewDescribeSecurityGroupsRequest())
	if err != nil {
		return nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	out := make([]TopologySG, 0, len(resp.Response.SecurityGroupSet))
	for _, g := range resp.Response.SecurityGroupSet {
		out = append(out, TopologySG{
			ID:          derefStringRaw(g.SecurityGroupId),
			Name:        derefStringRaw(g.SecurityGroupName),
			Description: derefStringRaw(g.SecurityGroupDesc),
			IsDefault:   derefBool(g.IsDefault),
		})
	}
	return out, nil
}

func (c *Client) topoMySQL() ([]TopologyDB, error) {
	cl, err := newCDBClient(c, c.creds.Region)
	if err != nil {
		return nil, err
	}
	req := cdb.NewDescribeDBInstancesRequest()
	var offset, limit uint64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cl.DescribeDBInstances(req)
	if err != nil {
		return nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	out := make([]TopologyDB, 0, len(resp.Response.Items))
	for _, i := range resp.Response.Items {
		// CDB returns VPC as an integer ID (UniqVpcId is the string form when present).
		vpcID := derefStringRaw(i.UniqVpcId)
		out = append(out, TopologyDB{
			ID:     derefStringRaw(i.InstanceId),
			Name:   derefStringRaw(i.InstanceName),
			Status: mysqlStatus(i.Status),
			Engine: "mysql " + derefStringRaw(i.EngineVersion),
			VpcID:  vpcID,
			Zone:   derefStringRaw(i.Zone),
		})
	}
	return out, nil
}

func (c *Client) topoPostgres() ([]TopologyDB, error) {
	cl, err := newPostgresClient(c, c.creds.Region)
	if err != nil {
		return nil, err
	}
	req := postgres.NewDescribeDBInstancesRequest()
	var offset, limit uint64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cl.DescribeDBInstances(req)
	if err != nil {
		return nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	out := make([]TopologyDB, 0, len(resp.Response.DBInstanceSet))
	for _, i := range resp.Response.DBInstanceSet {
		out = append(out, TopologyDB{
			ID:     derefStringRaw(i.DBInstanceId),
			Name:   derefStringRaw(i.DBInstanceName),
			Status: derefStringRaw(i.DBInstanceStatus),
			Engine: "postgres " + derefStringRaw(i.DBVersion),
			VpcID:  derefStringRaw(i.VpcId),
			Zone:   derefStringRaw(i.Zone),
		})
	}
	return out, nil
}

func (c *Client) topoClusters() ([]TopologyCluster, error) {
	cl, err := newTKEClient(c, c.creds.Region)
	if err != nil {
		return nil, err
	}
	req := tke.NewDescribeClustersRequest()
	var offset, limit int64 = 0, 100
	req.Offset = &offset
	req.Limit = &limit
	resp, err := cl.DescribeClusters(req)
	if err != nil {
		return nil, friendlyError(err)
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	out := make([]TopologyCluster, 0, len(resp.Response.Clusters))
	for _, k := range resp.Response.Clusters {
		row := TopologyCluster{
			ID:      derefStringRaw(k.ClusterId),
			Name:    derefStringRaw(k.ClusterName),
			Status:  derefStringRaw(k.ClusterStatus),
			Version: derefStringRaw(k.ClusterVersion),
			NodeNum: derefUint64Raw(k.ClusterNodeNum),
		}
		if k.ClusterNetworkSettings != nil {
			row.VpcID = derefStringRaw(k.ClusterNetworkSettings.VpcId)
		}
		out = append(out, row)
	}
	return out, nil
}

func firstIP(ptrs []*string) string {
	for _, p := range ptrs {
		if p != nil && strings.TrimSpace(*p) != "" {
			return *p
		}
	}
	return ""
}
