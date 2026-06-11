package codeview

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeMapsCommonPatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"express":"latest","pg":"latest","jsonwebtoken":"latest","zod":"latest","@opentelemetry/api":"latest"}}`)
	writeFile(t, dir, "src/server.ts", `
import express from "express"
import { userRouter } from "./routes/users"
const app = express()
process.env.OTEL_SERVICE_NAME = "users-api"
app.use("/users", userRouter)
app.listen(process.env.PORT || 3000)
`)
	writeFile(t, dir, "src/routes/users.ts", `
import { Router } from "express"
import { requireAuth } from "../middleware/auth"
import { UserSchema } from "../schemas/user"
import { userService } from "../services/userService"
export const userRouter = Router()
userRouter.post("/", requireAuth, (req, res) => {
  // ACME-123 tracks this onboarding route.
  const input = UserSchema.parse(req.body)
  return res.json(userService.create(input))
})
`)
	writeFile(t, dir, "src/middleware/auth.ts", `
import jwt from "jsonwebtoken"
export function requireAuth(req, res, next) {
  const token = req.headers.Authorization?.replace("Bearer ", "")
  jwt.verify(token, process.env.JWT_SECRET)
  next()
}
`)
	writeFile(t, dir, "src/services/userService.ts", `
import { userRepository } from "../repositories/userRepository"
export const userService = { create(input) { return userRepository.create(input) } }
`)
	writeFile(t, dir, "src/repositories/userRepository.ts", `
import pg from "pg"
export const userRepository = { create(input) { return pg.query("INSERT INTO users(email) VALUES($1)", [input.email]) } }
`)
	writeFile(t, dir, "src/schemas/user.ts", `
import { z } from "zod"
export const UserSchema = z.object({ email: z.string().email() })
`)
	writeFile(t, dir, "Dockerfile", `
FROM node:22-alpine
`)
	writeFile(t, dir, ".github/workflows/deploy.yml", `
name: deploy
on: [push]
`)
	writeFile(t, dir, "infra/main.tf", `
resource "aws_s3_bucket" "uploads" {}
`)
	writeFile(t, dir, "k8s/deployment.yaml", `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: users-api
`)

	analysis, err := Analyze(dir, "https://github.com/acme/app")
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	if analysis.Summary.PrimaryLanguage != "TypeScript" && analysis.Summary.PrimaryLanguage != "JavaScript" {
		t.Fatalf("expected TS/JS primary language, got %q", analysis.Summary.PrimaryLanguage)
	}
	for _, id := range []string{"entry_point", "routes_handlers", "auth", "middleware", "validation", "services", "database"} {
		if !hasPattern(analysis, id) {
			t.Fatalf("expected pattern %q in %#v", id, analysis.Patterns)
		}
	}
	if !analysis.Summary.HasAuth || !analysis.Summary.HasDatabase || !analysis.Summary.HasMiddleware {
		t.Fatalf("expected auth/db/middleware summary flags, got %#v", analysis.Summary)
	}
	if len(analysis.Graph.Nodes) == 0 || len(analysis.Graph.Edges) == 0 {
		t.Fatalf("expected graph nodes and edges, got nodes=%d edges=%d", len(analysis.Graph.Nodes), len(analysis.Graph.Edges))
	}
	for _, kind := range []string{"work_item", "service", "infra_resource", "deployment", "dependency"} {
		if !hasCorrelation(analysis, kind) {
			t.Fatalf("expected correlation kind %q in %#v", kind, analysis.Correlations)
		}
	}
	if analysis.Summary.CorrelationCount == 0 {
		t.Fatalf("expected correlation count in summary")
	}
	if !hasGraphNodeType(analysis, "correlation") {
		t.Fatalf("expected correlation graph nodes in %#v", analysis.Graph.Nodes)
	}
}

func hasPattern(analysis *Analysis, id string) bool {
	for _, pattern := range analysis.Patterns {
		if pattern.ID == id {
			return true
		}
	}
	return false
}

func hasCorrelation(analysis *Analysis, kind string) bool {
	for _, corr := range analysis.Correlations {
		if corr.Type == kind {
			return true
		}
	}
	return false
}

func hasGraphNodeType(analysis *Analysis, typ string) bool {
	for _, node := range analysis.Graph.Nodes {
		if node.Type == typ {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
