// Copyright © 2019 VMware
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package contour

import "sync"

// Cond implements a condition variable, a rendezvous point for goroutines
// waiting for or announcing the ocurence of an event.
//
// Unlike sync.Cond, Cond communciates with waiters via channels registered by
// the waiters. This permits goroutines to wait on Cond events using select.
type Cond struct {
	mu      sync.Mutex
	waiters map[chan int][]string
	last    int
}

// Register registers ch to receive a value when Notify is called.
// The value of last is the count of the times Notify has been called on this Cond.
// It functions of a sequence counter, if the value of last supplied to Register
// is less than the Conds internal counter, then the caller has missed at least
// one notification and will fire immediately.
//
// Sends by the broadcaster to ch must not block, therefore ch must have a capacity
// of at least 1.
func (c *Cond) Register(ch chan int, last int, hints ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if last < c.last {
		// notify this channel immediately
		ch <- c.last
		return
	}
	if c.waiters == nil {
		c.waiters = make(map[chan int][]string)
	}
	c.waiters[ch] = hints
}

// Notify notifies all registered waiters that an event has ocured.
func (c *Cond) Notify(hints ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last++

	m := make(map[string]bool)
	for _, h := range hints {
		m[h] = true
	}

	for ch, hints := range c.waiters {
		if len(hints) == 0 {
			ch <- c.last
			delete(c.waiters, ch)
			continue
		}
		for _, h := range hints {
			if m[h] {
				ch <- c.last
				delete(c.waiters, ch)
				break
			}
		}
	}
}
