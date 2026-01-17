// Package coordinator orchestrates dependency-aware parallel agent execution.
package coordinator

import (
	"context"
	"fmt"
	"sync"
	"time"

	dt "github.com/bgdnvk/clanker/internal/agent/decisiontree"
	"github.com/bgdnvk/clanker/internal/agent/model"
	awsclient "github.com/bgdnvk/clanker/internal/aws"
	"github.com/bgdnvk/clanker/internal/k8s"
	"github.com/spf13/viper"
)

func verboseAgents() bool {
	return viper.GetBool("debug") || viper.GetBool("agent.trace")
}

// ParallelAgent represents a running worker instance.
type ParallelAgent struct {
	ID         string
	Type       AgentType
	Status     string
	StartTime  time.Time
	EndTime    time.Time
	Context    *model.AgentContext
	Results    model.AWSData
	Error      error
	Operations []awsclient.LLMOperation
}

// Coordinator drives decision-tree-based parallel execution.
type Coordinator struct {
	DecisionTree *dt.Tree
	MainContext  *model.AgentContext

	client    *awsclient.Client
	registry  *AgentRegistry
	dataBus   *SharedDataBus
	scheduler *DependencyScheduler
}

// New returns a ready-to-use coordinator.
func New(mainContext *model.AgentContext, client *awsclient.Client) *Coordinator {
	return &Coordinator{
		DecisionTree: dt.New(),
		MainContext:  mainContext,
		client:       client,
		registry:     NewAgentRegistry(),
		dataBus:      NewSharedDataBus(),
		scheduler:    NewDependencyScheduler(),
	}
}

// Analyze traverses the decision tree for the provided query.
func (c *Coordinator) Analyze(query string) []*dt.Node {
	return c.DecisionTree.Traverse(query, c.MainContext)
}

// SpawnAgents starts agents grouped by dependency order.
func (c *Coordinator) SpawnAgents(ctx context.Context, applicable []*dt.Node) {
	agentConfigs := make(map[string]AgentConfig)

	for _, node := range applicable {
		for _, name := range node.AgentTypes {
			agt, ok := c.lookupAgentType(name)
			if !ok {
				continue
			}
			if existing, exists := agentConfigs[name]; !exists || node.Priority > existing.Priority {
				agentConfigs[name] = AgentConfig{
					Priority:   node.Priority,
					Parameters: node.Parameters,
					AgentType:  agt,
				}
			}
		}
	}

	if len(agentConfigs) == 0 {
		return
	}

	planned := c.scheduler.Plan(agentConfigs)
	verbose := verboseAgents()

	for _, group := range planned {
		if verbose {
			fmt.Printf("📊 Executing order group %d with %d agents\n", group.Order, len(group.Agents))
		}
		var wg sync.WaitGroup
		for _, cfg := range group.Agents {
			if !c.scheduler.Ready(cfg.AgentType, c.dataBus) {
				if verbose {
					fmt.Printf("⏸️  Agent %s waiting for dependencies\n", cfg.AgentType.Name)
				}
				continue
			}
			agent := c.newParallelAgent(cfg)
			c.registry.Register(agent)
			wg.Add(1)
			go c.runPlannedAgent(ctx, &wg, agent)
		}
		wg.Wait()
		if verbose {
			fmt.Printf("✅ Order group %d completed\n", group.Order)
		}
	}
}

// WaitForCompletion blocks until all agents finish or timeout occurs.
func (c *Coordinator) WaitForCompletion(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	verbose := verboseAgents()

	for time.Now().Before(deadline) {
		stats := c.registry.Stats()
		completed := stats.Completed + stats.Failed
		if stats.Total > 0 && completed >= stats.Total {
			if verbose {
				fmt.Printf("🎉 All %d agents completed (%d successful, %d failed)\n",
					stats.Total, stats.Completed, stats.Failed)
			}
			return nil
		}
		if verbose && completed > 0 {
			fmt.Printf("⏳ Waiting for agents: %d/%d completed\n", completed, stats.Total)
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for agents to complete")
}

// AggregateResults merges successful agent outputs.
func (c *Coordinator) AggregateResults() model.AWSData {
	aggregated := make(model.AWSData)
	for _, agent := range c.registry.Agents() {
		if agent.Status != "completed" {
			continue
		}
		agentKey := agent.Type.Name
		aggregated[agentKey] = agent.Results
		for key, value := range agent.Results {
			aggregated[fmt.Sprintf("%s_%s", agentKey, key)] = value
		}
	}

	stats := c.registry.Stats()
	aggregated["_metadata"] = model.AWSData{
		"total_agents":    stats.Total,
		"completed_count": stats.Completed,
		"failed_count":    stats.Failed,
		"decision_path":   c.DecisionTree.CurrentPath,
		"execution_time":  time.Now().Format(time.RFC3339),
	}

	return aggregated
}

// Stats exposes snapshot counters for callers needing execution metrics.
func (c *Coordinator) Stats() AgentStats {
	return c.registry.Stats()
}

func (c *Coordinator) runPlannedAgent(ctx context.Context, wg *sync.WaitGroup, agent *ParallelAgent) {
	defer wg.Done()
	verbose := verboseAgents()
	if verbose {
		fmt.Printf("  ✨ Started %s agent (ID: %s) with dependencies\n", agent.Type.Name, agent.ID)
	}

	c.runParallelAgent(ctx, agent)
	c.persistProvidedData(agent)

	if verbose {
		fmt.Printf("✅ Agent %s completed, provided data: %v\n",
			agent.Type.Name, agent.Type.Dependencies.ProvidedData)
	}
}

func (c *Coordinator) runParallelAgent(ctx context.Context, agent *ParallelAgent) {
	defer func() {
		agent.EndTime = time.Now()
		if agent.Error != nil {
			agent.Status = "failed"
			c.registry.MarkFailed()
			return
		}
		agent.Status = "completed"
		c.registry.MarkCompleted()
	}()

	verbose := verboseAgents()
	if verbose {
		fmt.Printf("🤖 Agent %s (%s) executing %d operations\n",
			agent.ID, agent.Type.Name, len(agent.Operations))
	}

	for _, op := range agent.Operations {
		var (
			result any
			err    error
		)

		switch op.Operation {
		case "k8s_get_cluster_resources":
			k8sAgent := k8s.NewAgentWithOptions(k8s.AgentOptions{
				Debug:      verbose,
				AWSProfile: viper.GetString("aws.default_profile"),
				Region:     viper.GetString("aws.default_region"),
				Kubeconfig: viper.GetString("k8s.kubeconfig"),
			})
			clusterName := viper.GetString("k8s.cluster")
			if clusterName == "" {
				clusterName = "current"
			}
			qopts := k8s.QueryOptions{
				ClusterName: clusterName,
				Namespace:   viper.GetString("k8s.namespace"),
				AWSProfile:  viper.GetString("aws.default_profile"),
				Region:      viper.GetString("aws.default_region"),
				Kubeconfig:  viper.GetString("k8s.kubeconfig"),
			}
			result, err = k8sAgent.GetClusterResources(ctx, clusterName, qopts)
		case "discover_services":
			result, err = c.discoverServicesWithAI(ctx, op.Parameters)
		case "investigate_service_logs":
			var discovered map[string]any
			if skip, exists := op.Parameters["skip_discovery"]; exists && skip == true {
				discovered = make(map[string]any)
				for k, v := range c.MainContext.ServiceData {
					discovered[k] = v
				}
			} else {
				discovered = agent.Results
			}
			result, err = c.investigateServiceLogsWithAI(ctx, op.Parameters, discovered)
		default:
			result, err = c.client.ExecuteOperation(ctx, op.Operation, op.Parameters)
		}

		if err != nil {
			agent.Error = err
			if verbose {
				fmt.Printf("❌ Agent %s operation %s failed: %v\n", agent.ID, op.Operation, err)
			}
			if op.Operation == "discover_services" || op.Operation == "investigate_service_logs" {
				agent.Error = nil
				continue
			}
			return
		}

		key := fmt.Sprintf("%s_%s", agent.Type.Name, op.Operation)
		agent.Results[key] = result
		if verbose {
			fmt.Printf("✅ Agent %s completed operation: %s\n", agent.ID, op.Operation)
		}
	}
}
