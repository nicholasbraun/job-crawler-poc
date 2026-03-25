// Package processor defines the interface for processor, which can be workers for a pool, or consumers of a message broker
// Implementations live in the sub packages
package processor

import "context"

type Processor[T any] interface {
	Process(ctx context.Context, workload *T) error
}
