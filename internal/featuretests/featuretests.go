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

// Package featuretests provides end to end tests of specific features.
package featuretests

import (
	"context"
	"math/rand"
	"net"
	"testing"
	"time"

	v2 "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"
	envoy "github.com/envoyproxy/go-control-plane/pkg/cache"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/any"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/projectcontour/contour/apis/generated/clientset/versioned/fake"
	"github.com/projectcontour/contour/internal/contour"
	cgrpc "github.com/projectcontour/contour/internal/grpc"
	"github.com/projectcontour/contour/internal/k8s"
	"github.com/projectcontour/contour/internal/metrics"
	"github.com/projectcontour/contour/internal/workgroup"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (
	endpointType = envoy.EndpointType
	clusterType  = envoy.ClusterType
	routeType    = envoy.RouteType
	listenerType = envoy.ListenerType
	secretType   = envoy.SecretType
	statsAddress = "0.0.0.0"
	statsPort    = 8002
)

type discardWriter struct{}

func (d *discardWriter) Write(buf []byte) (int, error) {
	return len(buf), nil
}

func setup(t *testing.T, opts ...func(*contour.EventHandler)) (cache.ResourceEventHandler, *Contour, func()) {
	t.Parallel()

	log := logrus.New()
	log.Out = new(discardWriter)

	et := &contour.EndpointsTranslator{
		FieldLogger: log,
	}

	r := prometheus.NewRegistry()
	ch := &contour.CacheHandler{
		Metrics:       metrics.NewMetrics(r),
		ListenerCache: contour.NewListenerCache(statsAddress, statsPort),
		FieldLogger:   log,
	}

	rand.Seed(time.Now().Unix())

	eh := &contour.EventHandler{
		CacheHandler: ch,
		CRDStatus: &k8s.CRDStatus{
			Client: fake.NewSimpleClientset(),
		},
		Metrics:         ch.Metrics,
		FieldLogger:     log,
		Sequence:        make(chan int, 1),
		HoldoffDelay:    time.Duration(rand.Intn(100)) * time.Millisecond,
		HoldoffMaxDelay: time.Duration(rand.Intn(500)) * time.Millisecond,
	}

	for _, opt := range opts {
		opt(eh)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	check(t, err)
	discard := logrus.New()
	discard.Out = new(discardWriter)
	// Resource types in xDS v2.
	srv := cgrpc.NewAPI(discard, map[string]cgrpc.Resource{
		ch.ClusterCache.TypeURL():  &ch.ClusterCache,
		ch.RouteCache.TypeURL():    &ch.RouteCache,
		ch.ListenerCache.TypeURL(): &ch.ListenerCache,
		ch.SecretCache.TypeURL():   &ch.SecretCache,
		et.TypeURL():               et,
	})

	var g workgroup.Group

	g.Add(func(stop <-chan struct{}) error {
		done := make(chan error)
		go func() {
			done <- srv.Serve(l) // srv now owns l and will close l before returning
		}()
		<-stop
		srv.Stop()
		return <-done
	})
	g.Add(eh.Start())

	cc, err := grpc.Dial(l.Addr().String(), grpc.WithInsecure())
	check(t, err)

	rh := &resourceEventHandler{
		EventHandler:        eh,
		EndpointsTranslator: et,
	}

	stop := make(chan struct{})
	g.Add(func(_ <-chan struct{}) error {
		<-stop
		return nil
	})

	done := make(chan error)
	go func() {
		done <- g.Run()
	}()

	return rh, &Contour{T: t, ClientConn: cc}, func() {
		// close client connection
		cc.Close()

		// stop server
		close(stop)

		<-done
	}
}

// resourceEventHandler composes a contour.Translator and a contour.EndpointsTranslator
// into a single ResourceEventHandler type.
type resourceEventHandler struct {
	*contour.EventHandler
	*contour.EndpointsTranslator
}

func (r *resourceEventHandler) OnAdd(obj interface{}) {
	switch obj.(type) {
	case *v1.Endpoints:
		r.EndpointsTranslator.OnAdd(obj)
	default:
		r.EventHandler.OnAdd(obj)
		<-r.EventHandler.Sequence
	}
}

func (r *resourceEventHandler) OnUpdate(oldObj, newObj interface{}) {
	switch newObj.(type) {
	case *v1.Endpoints:
		r.EndpointsTranslator.OnUpdate(oldObj, newObj)
	default:
		r.EventHandler.OnUpdate(oldObj, newObj)
		<-r.EventHandler.Sequence
	}
}

func (r *resourceEventHandler) OnDelete(obj interface{}) {
	switch obj.(type) {
	case *v1.Endpoints:
		r.EndpointsTranslator.OnDelete(obj)
	default:
		r.EventHandler.OnDelete(obj)
		<-r.EventHandler.Sequence
	}
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func resources(t *testing.T, protos ...proto.Message) []*any.Any {
	t.Helper()
	anys := make([]*any.Any, 0, len(protos))
	for _, pb := range protos {
		a, err := ptypes.MarshalAny(pb)
		check(t, err)
		anys = append(anys, a)
	}
	return anys
}

type grpcStream interface {
	Send(*v2.DiscoveryRequest) error
	Recv() (*v2.DiscoveryResponse, error)
}

type Contour struct {
	*grpc.ClientConn
	*testing.T
}

func (c *Contour) Request(typeurl string, names ...string) *Response {
	c.Helper()
	var st grpcStream
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	switch typeurl {
	case secretType:
		sds := discovery.NewSecretDiscoveryServiceClient(c.ClientConn)
		sts, err := sds.StreamSecrets(ctx)
		c.check(err)
		st = sts
	case routeType:
		rds := v2.NewRouteDiscoveryServiceClient(c.ClientConn)
		str, err := rds.StreamRoutes(ctx)
		c.check(err)
		st = str
	default:
		c.Fatal("unknown typeURL:", typeurl)
	}
	resp := c.sendRequest(st, &v2.DiscoveryRequest{
		TypeUrl:       typeurl,
		ResourceNames: names,
	})
	return &Response{
		Contour:           c,
		DiscoveryResponse: resp,
	}
}

func (c *Contour) sendRequest(stream grpcStream, req *v2.DiscoveryRequest) *v2.DiscoveryResponse {
	err := stream.Send(req)
	c.check(err)
	resp, err := stream.Recv()
	c.check(err)
	return resp
}

func (c *Contour) check(err error) {
	if err != nil {
		c.Fatal(err)
	}
}

type Response struct {
	*Contour
	*v2.DiscoveryResponse
}

func (r *Response) Equals(want *v2.DiscoveryResponse) {
	r.Helper()
	opts := []cmp.Option{
		cmpopts.IgnoreFields(v2.DiscoveryResponse{}, "VersionInfo", "Nonce"),
		cmpopts.AcyclicTransformer("UnmarshalAny", unmarshalAny),
	}
	diff := cmp.Diff(want, r.DiscoveryResponse, opts...)
	if diff != "" {
		r.Fatal(diff)
	}
}

func unmarshalAny(a *any.Any) proto.Message {
	pb, err := ptypes.Empty(a)
	if err != nil {
		panic(err.Error())
	}
	err = ptypes.UnmarshalAny(a, pb)
	if err != nil {
		panic(err.Error())
	}
	return pb
}
