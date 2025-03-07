package pkg

import (
	"github.com/google/uuid"
)

// GenerateWorkerID creates a unique identifier for a worker
func GenerateWorkerID() string {
	return uuid.New().String()
}
