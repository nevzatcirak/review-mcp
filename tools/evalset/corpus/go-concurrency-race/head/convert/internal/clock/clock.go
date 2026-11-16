// Package clock lets tests set the time.
package clock

import "time"

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

type system struct{}

func (system) Now() time.Time { return time.Now() }

// Or returns c, or the system clock when c is nil.
func Or(c Clock) Clock {
	if c == nil {
		return system{}
	}
	return c
}

// Fixed is a clock for tests that tells the time it is set to.
type Fixed struct{ T time.Time }

func (f *Fixed) Now() time.Time { return f.T }
