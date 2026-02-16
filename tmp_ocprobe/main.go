package main

import (
	"context"
	"fmt"

	"github.com/bgdnvk/clanker/internal/deploy"
)

func main() {
	ctx := context.Background()
	repo := "https://github.com/openclaw/openclaw"
	p, err := deploy.CloneAndAnalyze(ctx, repo)
	if err != nil {
		panic(err)
	}
	d := deploy.AnalyzeDockerAgent(p)
	fmt.Printf("repo=%s\n", p.RepoURL)
	fmt.Printf("hasCompose=%v services=%v\n", d.HasCompose, d.ComposeServices)
	fmt.Printf("publishedPorts=%v exposedPorts=%v primaryPort=%d\n", d.PublishedPorts, d.ExposedPorts, d.PrimaryPort)
	fmt.Printf("volumeMounts=%v\n", d.VolumeMounts)
}
