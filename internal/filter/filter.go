// Package filter is responsible for the filtering logic.
// It implements all the filters for content, urls and relevant jobs.
package filter

type CheckFn[T any] func(T) error

// Chain reduces multiple checks to one check. When the returned check is called,
// all the checks in the chain will get called in the same order as provided.
// If one check fails, the chain will stop checking
func Chain[T any](checks ...CheckFn[T]) CheckFn[T] {
	return func(item T) error {
		for _, fn := range checks {
			err := fn(item)
			if err != nil {
				return err
			}
		}

		return nil
	}
}
