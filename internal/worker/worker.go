package worker

import "context"

type Processor[T any] interface {
	Process(ctx context.Context, workload T) error
	Close()
}
