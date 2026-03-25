package metrics

import "sync/atomic"

type Counter struct {
	v atomic.Int64
}

func (c *Counter) Inc() {
	c.v.Add(1)
}

func (c *Counter) Add(n int64) {
	c.v.Add(n)
}

func (c *Counter) Value() int64 {
	return c.v.Load()
}
