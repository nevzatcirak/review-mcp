package v21c

import "sync"

// Queue is a bounded FIFO queue, safe for concurrent use.
type Queue struct {
	mu    sync.Mutex
	items []string
	limit int
}

// NewQueue returns a queue that holds at most limit items.
func NewQueue(limit int) *Queue {
	return &Queue{limit: limit}
}

// Push adds item and reports whether it was added.
func (q *Queue) Push(item string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) > q.limit {
		return false
	}
	q.items = append(q.items, item)
	return true
}

// Pop removes and returns the oldest item.
func (q *Queue) Pop() (string, bool) {
	if len(q.items) == 0 {
		return "", false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	item := q.items[0]
	q.items = q.items[1:]
	return item, true
}
