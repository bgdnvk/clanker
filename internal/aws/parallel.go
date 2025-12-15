package aws

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// executeOperationsWithProfile executes operations with a given profile
func (c *Client) executeOperationsWithProfile(ctx context.Context, operations []LLMOperation, profile *AIProfile) (string, error) {
	// Check if local rate limiting is enabled (default: true)
	localMode := viper.GetBool("local_mode")
	delayMs := viper.GetInt("local_delay_ms")
	verbose := viper.GetBool("debug")

	// Default to local mode if not explicitly set
	if !viper.IsSet("local_mode") {
		localMode = true
	}

	// Default delay for local mode
	if localMode && delayMs == 0 {
		delayMs = 100 // 100ms delay between calls by default
	}

	if verbose {
		log.Printf("🔧 Parallel execution: %d operations, local_mode=%v, delay=%dms", len(operations), localMode, delayMs)
	}

	// Create channels for results
	resultChan := make(chan LLMOperationResult, len(operations))
	var wg sync.WaitGroup

	// Execute all operations concurrently (or with rate limiting in local mode)
	for i, op := range operations {
		wg.Add(1)

		// Add delay for local mode to prevent system overload
		if localMode && i > 0 {
			if verbose {
				log.Printf("⏱️  Local mode delay: %dms before operation %d/%d: %s", delayMs, i+1, len(operations), op.Operation)
			}
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		go func(index int, operation string, params map[string]interface{}) {
			defer wg.Done()
			if verbose {
				log.Printf("🚀 Starting operation %d: %s", index+1, operation)
			}

			start := time.Now()
			result, err := c.executeAWSOperation(ctx, operation, params, profile)
			duration := time.Since(start)

			if verbose {
				if err != nil {
					log.Printf("❌ Operation %d failed (%v): %s - %v", index+1, duration, operation, err)
				} else {
					log.Printf("✅ Operation %d completed (%v): %s", index+1, duration, operation)
				}
			}

			resultChan <- LLMOperationResult{
				Operation: operation,
				Result:    result,
				Error:     err,
				Index:     index,
			}
		}(i, op.Operation, op.Parameters)
	}

	// Wait for all operations to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results in order
	results := make([]LLMOperationResult, len(operations))
	for result := range resultChan {
		results[result.Index] = result
	}

	// Build results string in original order
	var awsResults strings.Builder
	for _, result := range results {
		if result.Error != nil {
			awsResults.WriteString(fmt.Sprintf("❌ %s failed: %v\n", result.Operation, result.Error))
		} else {
			awsResults.WriteString(fmt.Sprintf("✅ %s:\n%s\n\n", result.Operation, result.Result))
		}
	}

	return awsResults.String(), nil
}

// ExecuteOperationsConcurrently executes multiple AWS operations concurrently for LLM processing
func (c *Client) ExecuteOperationsConcurrently(ctx context.Context, operations []LLMOperation, aiProfile string) (string, error) {
	if len(operations) == 0 {
		return "", nil
	}

	// Get AI profile configuration
	profile, err := GetAIProfile(aiProfile)
	if err != nil {
		return "", fmt.Errorf("failed to get AI profile: %w", err)
	}

	return c.executeOperationsWithProfile(ctx, operations, profile)
}

// ExecuteOperationsWithAWSProfile executes multiple AWS operations concurrently using a direct AWS profile
func (c *Client) ExecuteOperationsWithAWSProfile(ctx context.Context, operations []LLMOperation, awsProfile, region string) (string, error) {
	if len(operations) == 0 {
		return "", nil
	}

	// Create a temporary AI profile with the specified AWS profile
	profile := &AIProfile{
		Provider:   "bedrock", // Not used for AWS operations
		AWSProfile: awsProfile,
		Region:     region,
	}

	return c.executeOperationsWithProfile(ctx, operations, profile)
}
