package workerpool

import "context"

type Worker[T any] interface {
	Process(ctx context.Context, workload *T) error
}
