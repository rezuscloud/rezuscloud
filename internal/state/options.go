package state

// ListOptions controls list query behavior.
type ListOptions struct {
	Offset        int
	Limit         int
	LabelSelector string
}

// ListOption is a functional option for list queries.
type ListOption func(*ListOptions)

func newListOptions(opts ...ListOption) ListOptions {
	o := ListOptions{Limit: 50}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithOffset sets the pagination offset.
func WithOffset(offset int) ListOption {
	return func(o *ListOptions) { o.Offset = offset }
}

// WithLimit sets the pagination limit.
func WithLimit(limit int) ListOption {
	return func(o *ListOptions) { o.Limit = limit }
}

// WithLabelSelector sets the label filter.
func WithLabelSelector(selector string) ListOption {
	return func(o *ListOptions) { o.LabelSelector = selector }
}
