package codeview

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeMapsCommonPatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"express":"latest","pg":"latest","jsonwebtoken":"latest","zod":"latest"}}`)
	writeFile(t, dir, "src/server.ts", `
import express from "express"
import { userRouter } from "./routes/users"
const app = express()
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
}

func hasPattern(analysis *Analysis, id string) bool {
	for _, pattern := range analysis.Patterns {
		if pattern.ID == id {
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
