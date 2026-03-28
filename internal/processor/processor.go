// Package processor defines the interface for processor, which can be workers for a pool, or consumers of a message broker
// Implementations live in the sub packages
package processor

import "context"

// Processor handles a single unit of work. Implementations are used as
// workers in a Pool or as consumers of a message broker.
type Processor[T any] interface {
	Process(ctx context.Context, workload *T) error
}
