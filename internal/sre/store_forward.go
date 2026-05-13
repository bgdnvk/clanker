package sre

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	heartbeatQueueFileName = "heartbeat-queue.json"
	maxQueuedHeartbeats    = 500
	maxFlushPerTick        = 40
)

type queuedHeartbeat struct {
	QueuedAt string         `json:"queuedAt"`
	Payload  map[string]any `json:"payload"`
}

func heartbeatQueuePath() (string, error) {
	stateDir, err := DefaultStateDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(stateDir, heartbeatQueueFileName), nil
}

func loadQueuedHeartbeats() ([]queuedHeartbeat, error) {
	path, err := heartbeatQueuePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []queuedHeartbeat{}, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return []queuedHeartbeat{}, nil
	}
	var queue []queuedHeartbeat
	if err := json.Unmarshal(raw, &queue); err != nil {
		return nil, err
	}
	if queue == nil {
		return []queuedHeartbeat{}, nil
	}
	return queue, nil
}

func saveQueuedHeartbeats(queue []queuedHeartbeat) error {
	path, err := heartbeatQueuePath()
	if err != nil {
		return err
	}
	if len(queue) > maxQueuedHeartbeats {
		queue = queue[len(queue)-maxQueuedHeartbeats:]
	}
	body, err := json.Marshal(queue)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, body, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func enqueueHeartbeat(payload map[string]any) error {
	queue, err := loadQueuedHeartbeats()
	if err != nil {
		return err
	}
	queue = append(queue, queuedHeartbeat{QueuedAt: time.Now().UTC().Format(time.RFC3339Nano), Payload: payload})
	return saveQueuedHeartbeats(queue)
}

func flushQueuedHeartbeats(ctx context.Context, baseURL string, token string) (sent int, remaining int, err error) {
	queue, err := loadQueuedHeartbeats()
	if err != nil {
		return 0, 0, err
	}
	if len(queue) == 0 {
		return 0, 0, nil
	}

	flushLimit := len(queue)
	if flushLimit > maxFlushPerTick {
		flushLimit = maxFlushPerTick
	}

	index := 0
	for index < flushLimit {
		item := queue[index]
		if postErr := postHeartbeatPayload(ctx, baseURL, token, item.Payload); postErr != nil {
			remaining = len(queue) - index
			_ = saveQueuedHeartbeats(queue[index:])
			return sent, remaining, postErr
		}
		sent++
		index++
	}

	if index >= len(queue) {
		remaining = 0
		if err := saveQueuedHeartbeats([]queuedHeartbeat{}); err != nil {
			return sent, 0, err
		}
		return sent, remaining, nil
	}

	rest := queue[index:]
	remaining = len(rest)
	if err := saveQueuedHeartbeats(rest); err != nil {
		return sent, remaining, err
	}
	return sent, remaining, nil
}
