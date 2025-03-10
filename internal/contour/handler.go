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

// Package contour contains the translation business logic that listens
// to Kubernetes ResourceEventHandler events and translates those into
// additions/deletions in caches connected to the Envoy xDS gRPC API server.
package contour

import (
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	ingressroutev1 "github.com/projectcontour/contour/apis/contour/v1beta1"
	projcontour "github.com/projectcontour/contour/apis/projectcontour/v1alpha1"
	"github.com/projectcontour/contour/internal/dag"
	"github.com/projectcontour/contour/internal/k8s"
	"github.com/projectcontour/contour/internal/metrics"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EventHandler implements cache.ResourceEventHandler, filters k8s events towards
// a dag.Builder and calls through to the CacheHandler to notify it that a new DAG
// is available.
type EventHandler struct {
	dag.Builder

	*CacheHandler

	HoldoffDelay, HoldoffMaxDelay time.Duration

	CRDStatus *k8s.CRDStatus

	*metrics.Metrics

	logrus.FieldLogger

	update chan interface{}

	// last holds the last time CacheHandler.OnUpdate was called.
	last time.Time

	// Sequence is a channel that receives a incrementing sequence number
	// for each update processed. The updates may be processed immediately, or
	// delayed by a holdoff timer. In each case a non blocking send to Sequence
	// will be made once CacheHandler.OnUpdate has been called.
	Sequence chan int

	// seq is the sequence counter of the number of times
	// an event has been received.
	seq int
}

type opAdd struct {
	obj interface{}
}

type opUpdate struct {
	oldObj, newObj interface{}
}

type opDelete struct {
	obj interface{}
}

func (e *EventHandler) OnAdd(obj interface{}) {
	e.update <- opAdd{obj: obj}
}

func (e *EventHandler) OnUpdate(oldObj, newObj interface{}) {
	e.update <- opUpdate{oldObj: oldObj, newObj: newObj}
}

func (e *EventHandler) OnDelete(obj interface{}) {
	e.update <- opDelete{obj: obj}
}

// Start initializes the EventHandler and returns a function suitable
// for registration with a workgroup.Group.
func (e *EventHandler) Start() func(<-chan struct{}) error {
	e.update = make(chan interface{})
	e.last = time.Now()
	return e.run
}

// run is the main event handling loop.
func (e *EventHandler) run(stop <-chan struct{}) error {
	e.Info("started")
	defer e.Info("stopped")

	var (
		// outstanding counts the number of events received but not
		// yet send to the CacheHandler.
		outstanding int

		// timer holds the timer that will send on C.
		timer *time.Timer

		// pending is a reference to the current timer's channel.
		pending <-chan time.Time
	)

	inc := func() { outstanding++ }
	reset := func() (v int) {
		v, outstanding = outstanding, 0
		return
	}

	// enqueue starts the holdoff timer
	enqueue := func() {
		inc()

		// If there is already a timer running, stop it and clear C.
		if timer != nil {
			timer.Stop()

			// nil out C in the case that the timer had already expired.
			// This effectively clears the notification.
			pending = nil
		}

		since := time.Since(e.last)
		if since > e.HoldoffMaxDelay {
			// the time since the last update has exceeded the max holdoff delay
			// so we must update immediately.
			e.WithField("last_update", since).WithField("outstanding", reset()).Info("forcing update")
			e.updateDAG() // rebuild dag and send to CacheHandler.
			e.incSequence()
			return
		}

		// If we get here then there is still time remaining before max holdoff so
		// start a new timer for the holdoff delay.
		timer = time.NewTimer(e.HoldoffDelay)
		pending = timer.C
	}

	for {
		// In the main loop one of four things can happen.
		// 1. We're waiting for an event on op, stop, or pending, noting that
		//    C may be nil if there are no pending events.
		// 2. We're processing an event.
		// 3. The holdoff timer from a previous event has fired and we're
		//    building a new DAG and sending to the CacheHandler.
		// 4. We're stopping.
		//
		// Only one of these things can happen at a time.
		select {
		case op := <-e.update:
			if e.onUpdate(op) {
				enqueue()
			} else {
				// notify any watchers that we received the event but chose
				// not to process it.
				e.incSequence()
			}
		case <-pending:
			e.WithField("last_update", time.Since(e.last)).WithField("outstanding", reset()).Info("performing delayed update")
			e.updateDAG()
			e.incSequence()
		case <-stop:
			// shutdown
			return nil
		}
	}
}

// onUpdate processes the event received. onUpdate returns
// true if the event changed the cache in a way that requires
// notifying the CacheHandler.
func (e *EventHandler) onUpdate(op interface{}) bool {
	switch op := op.(type) {
	case opAdd:
		return e.Builder.Source.Insert(op.obj)
	case opUpdate:
		if cmp.Equal(op.oldObj, op.newObj,
			cmpopts.IgnoreFields(ingressroutev1.IngressRoute{}, "Status"),
			cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")) {
			e.WithField("op", "update").Debugf("%T skipping update, only status has changed", op.newObj)
			return false
		}
		remove := e.Builder.Source.Remove(op.oldObj)
		insert := e.Builder.Source.Insert(op.newObj)
		return remove || insert
	case opDelete:
		return e.Builder.Source.Remove(op.obj)
	default:
		return false
	}
}

// incSequence bumps the sequence counter and sends it to e.Sequence.
func (e *EventHandler) incSequence() {
	e.seq++
	select {
	case e.Sequence <- e.seq:
		// This is a non blocking send so if this field is nil, or the
		// receiver is not ready this send does not block incSequence's caller.
	default:
	}
}

// updateDAG builds a new DAG and sends it to the CacheHandler
// the updates the status on objects and updates the metrics.
func (e *EventHandler) updateDAG() {
	dag := e.Builder.Build()
	e.CacheHandler.OnChange(dag)
	statuses := dag.Statuses()
	e.setStatus(statuses)

	metrics := calculateIngressRouteMetric(statuses)
	e.Metrics.SetIngressRouteMetric(metrics)

	e.last = time.Now()
}

// setStatus updates the status of objects.
func (e *EventHandler) setStatus(statuses map[dag.Meta]dag.Status) {
	for _, st := range statuses {
		switch obj := st.Object.(type) {
		case *ingressroutev1.IngressRoute:
			err := e.CRDStatus.SetStatus(st.Status, st.Description, obj)
			if err != nil {
				e.WithError(err).
					WithField("status", st.Status).
					WithField("desc", st.Description).
					WithField("name", obj.Name).
					WithField("namespace", obj.Namespace).
					Error("failed to set status")
			}
		case *projcontour.HTTPProxy:
			err := e.CRDStatus.SetStatus(st.Status, st.Description, obj)
			if err != nil {
				e.WithError(err).
					WithField("status", st.Status).
					WithField("desc", st.Description).
					WithField("name", obj.Name).
					WithField("namespace", obj.Namespace).
					Error("failed to set status")
			}
		default:
			e.WithField("namespace", obj.GetObjectMeta().GetNamespace()).
				WithField("name", obj.GetObjectMeta().GetName()).
				Error("set status: unknown object type")
		}
	}
}
