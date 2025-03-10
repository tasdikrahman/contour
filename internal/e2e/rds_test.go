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

// End to ends tests for translator to grpc operations.
package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	v2 "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	envoy_api_v2_route "github.com/envoyproxy/go-control-plane/envoy/api/v2/route"
	ingressroutev1 "github.com/projectcontour/contour/apis/contour/v1beta1"
	projcontour "github.com/projectcontour/contour/apis/projectcontour/v1alpha1"
	"github.com/projectcontour/contour/internal/contour"
	"github.com/projectcontour/contour/internal/envoy"
	"github.com/projectcontour/contour/internal/protobuf"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// heptio/contour#172. Updating an object from
//
// apiVersion: extensions/v1beta1
// kind: Ingress
// metadata:
//   name: kuard
// spec:
//   backend:
//     serviceName: kuard
//     servicePort: 80
//
// to
//
// apiVersion: extensions/v1beta1
// kind: Ingress
// metadata:
//   name: kuard
// spec:
//   rules:
//   - http:
//       paths:
//       - path: /testing
//         backend:
//           serviceName: kuard
//           servicePort: 80
//
// fails to update the virtualhost cache.
func TestEditIngress(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	meta := metav1.ObjectMeta{Name: "kuard", Namespace: "default"}

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	// add default/kuard to translator.
	old := &v1beta1.Ingress{
		ObjectMeta: meta,
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(80),
			},
		},
	}
	rh.OnAdd(old)

	// check that it's been translated correctly.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("*", envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/80/da39a3ee5e"))),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc))

	// update old to new
	rh.OnUpdate(old, &v1beta1.Ingress{
		ObjectMeta: meta,
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/testing",
							Backend: v1beta1.IngressBackend{
								ServiceName: "kuard",
								ServicePort: intstr.FromInt(80),
							},
						}},
					},
				},
			}},
		},
	})

	// check that ingress_http has been updated.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "2",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("*", envoy.Route(envoy.RoutePrefix("/testing"), routecluster("default/kuard/80/da39a3ee5e"))),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
			},
		),
		TypeUrl: routeType,
		Nonce:   "2",
	}, streamRDS(t, cc))
}

// heptio/contour#101
// The path /hello should point to default/hello/80 on "*"
//
// apiVersion: extensions/v1beta1
// kind: Ingress
// metadata:
//   name: hello
// spec:
//   rules:
//   - http:
// 	 paths:
//       - path: /hello
//         backend:
//           serviceName: hello
//           servicePort: 80
func TestIngressPathRouteWithoutHost(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	// add default/hello to translator.
	rh.OnAdd(&v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/hello",
							Backend: v1beta1.IngressBackend{
								ServiceName: "hello",
								ServicePort: intstr.FromInt(80),
							},
						}},
					},
				},
			}},
		},
	})

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	// check that it's been translated correctly.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "2",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("*",
						envoy.Route(envoy.RoutePrefix("/hello"), routecluster("default/hello/80/da39a3ee5e")),
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
			},
		),
		TypeUrl: routeType,
		Nonce:   "2",
	}, streamRDS(t, cc))
}

func TestEditIngressInPlace(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	i1 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				Host: "hello.example.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "wowie",
								ServicePort: intstr.FromString("http"),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnAdd(i1)

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wowie",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	s2 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kerpow",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       9000,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s2)

	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "2",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("hello.example.com",
						envoy.Route(envoy.RoutePrefix("/"), routecluster("default/wowie/80/da39a3ee5e")),
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
			},
		),
		TypeUrl: routeType,
		Nonce:   "2",
	}, streamRDS(t, cc))

	// i2 is like i1 but adds a second route
	i2 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				Host: "hello.example.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "wowie",
								ServicePort: intstr.FromInt(80),
							},
						}, {
							Path: "/whoop",
							Backend: v1beta1.IngressBackend{
								ServiceName: "kerpow",
								ServicePort: intstr.FromInt(9000),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnUpdate(i1, i2)
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "3",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("hello.example.com",
						envoy.Route(envoy.RoutePrefix("/whoop"), routecluster("default/kerpow/9000/da39a3ee5e")),
						envoy.Route(envoy.RoutePrefix("/"), routecluster("default/wowie/80/da39a3ee5e")),
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
			},
		),
		TypeUrl: routeType,
		Nonce:   "3",
	}, streamRDS(t, cc))

	// i3 is like i2, but adds the ingress.kubernetes.io/force-ssl-redirect: "true" annotation
	i3 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello",
			Namespace: "default",
			Annotations: map[string]string{
				"ingress.kubernetes.io/force-ssl-redirect": "true"},
		},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				Host: "hello.example.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "wowie",
								ServicePort: intstr.FromInt(80),
							},
						}, {
							Path: "/whoop",
							Backend: v1beta1.IngressBackend{
								ServiceName: "kerpow",
								ServicePort: intstr.FromInt(9000),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnUpdate(i2, i3)
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "4",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("hello.example.com",
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/whoop"),
							Action: envoy.UpgradeHTTPS(),
						},
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/"),
							Action: envoy.UpgradeHTTPS(),
						},
					),
				),
			},
			&v2.RouteConfiguration{Name: "ingress_https"},
		),
		TypeUrl: routeType,
		Nonce:   "4",
	}, streamRDS(t, cc))

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello-kitty",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	// i4 is the same as i3, and includes a TLS spec object to enable ingress_https routes
	// i3 is like i2, but adds the ingress.kubernetes.io/force-ssl-redirect: "true" annotation
	i4 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello",
			Namespace: "default",
			Annotations: map[string]string{
				"ingress.kubernetes.io/force-ssl-redirect": "true"},
		},
		Spec: v1beta1.IngressSpec{
			TLS: []v1beta1.IngressTLS{{
				Hosts:      []string{"hello.example.com"},
				SecretName: "hello-kitty",
			}},
			Rules: []v1beta1.IngressRule{{
				Host: "hello.example.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "wowie",
								ServicePort: intstr.FromInt(80),
							},
						}, {
							Path: "/whoop",
							Backend: v1beta1.IngressBackend{
								ServiceName: "kerpow",
								ServicePort: intstr.FromInt(9000),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnUpdate(i3, i4)
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "5",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("hello.example.com",
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/whoop"),
							Action: envoy.UpgradeHTTPS(),
						},
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/"),
							Action: envoy.UpgradeHTTPS(),
						},
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("hello.example.com",
						envoy.Route(envoy.RoutePrefix("/whoop"), routecluster("default/kerpow/9000/da39a3ee5e")),
						envoy.Route(envoy.RoutePrefix("/"), routecluster("default/wowie/80/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "5",
	}, streamRDS(t, cc))
}

// contour#164: backend request timeout support
func TestRequestTimeout(t *testing.T) {
	const (
		durationInfinite  = time.Duration(0)
		duration10Minutes = 10 * time.Minute
	)

	rh, cc, done := setup(t)
	defer done()

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	// i1 is a simple ingress bound to the default vhost.
	i1 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: v1beta1.IngressSpec{
			Backend: backend("backend", intstr.FromInt(80)),
		},
	}
	rh.OnAdd(i1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/backend/80/da39a3ee5e")),
		),
	), nil)

	// i2 adds an _invalid_ timeout, which we interpret as _infinite_.
	i2 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default",
			Annotations: map[string]string{
				"contour.heptio.com/request-timeout": "600", // not valid
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: backend("backend", intstr.FromInt(80)),
		},
	}
	rh.OnUpdate(i1, i2)
	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", durationInfinite)),
		),
	), nil)

	// i3 corrects i2 to use a proper duration
	i3 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default",
			Annotations: map[string]string{
				"contour.heptio.com/request-timeout": "600s", // 10 * time.Minute
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: backend("backend", intstr.FromInt(80)),
		},
	}
	rh.OnUpdate(i2, i3)
	assertRDS(t, cc, "3", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", duration10Minutes)),
		),
	), nil)

	// i4 updates i3 to explicitly request infinite timeout
	i4 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default",
			Annotations: map[string]string{
				"contour.heptio.com/request-timeout": "infinity",
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: backend("backend", intstr.FromInt(80)),
		},
	}
	rh.OnUpdate(i3, i4)
	assertRDS(t, cc, "4", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", durationInfinite)),
		),
	), nil)
}

// contour#250 ingress.kubernetes.io/force-ssl-redirect: "true" should apply
// per route, not per vhost.
func TestSSLRedirectOverlay(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	// i1 is a stock ingress with force-ssl-redirect on the / route
	i1 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app",
			Namespace: "default",
			Annotations: map[string]string{
				"ingress.kubernetes.io/force-ssl-redirect": "true",
			},
		},
		Spec: v1beta1.IngressSpec{
			TLS: []v1beta1.IngressTLS{{
				Hosts:      []string{"example.com"},
				SecretName: "example-tls",
			}},
			Rules: []v1beta1.IngressRule{{
				Host: "example.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "app-service",
								ServicePort: intstr.FromInt(8080),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnAdd(i1)

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-service",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	// i2 is an overlay to add the let's encrypt handler.
	i2 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "challenge", Namespace: "nginx-ingress"},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				Host: "example.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/.well-known/acme-challenge/gVJl5NWL2owUqZekjHkt_bo3OHYC2XNDURRRgLI5JTk",
							Backend: v1beta1.IngressBackend{
								ServiceName: "challenge-service",
								ServicePort: intstr.FromInt(8009),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnAdd(i2)

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "challenge-service",
			Namespace: "nginx-ingress",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       8009,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	assertRDS(t, cc, "5", virtualhosts(
		envoy.VirtualHost("example.com",
			envoy.Route(
				envoy.RoutePrefix("/.well-known/acme-challenge/gVJl5NWL2owUqZekjHkt_bo3OHYC2XNDURRRgLI5JTk"),
				routecluster("nginx-ingress/challenge-service/8009/da39a3ee5e"),
			),
			&envoy_api_v2_route.Route{
				Match:  envoy.RoutePrefix("/"), // match all
				Action: envoy.UpgradeHTTPS(),
			},
		),
	), virtualhosts(
		envoy.VirtualHost("example.com",
			envoy.Route(
				envoy.RoutePrefix("/.well-known/acme-challenge/gVJl5NWL2owUqZekjHkt_bo3OHYC2XNDURRRgLI5JTk"),
				routecluster("nginx-ingress/challenge-service/8009/da39a3ee5e"),
			),
			envoy.Route(
				envoy.RoutePrefix("/"), // match all
				routecluster("default/app-service/8080/da39a3ee5e"),
			),
		),
	))
}

func TestInvalidCertInIngress(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	// Create an invalid TLS secret
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       nil,
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	}
	rh.OnAdd(secret)

	// Create a service
	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	// Create an ingress that uses the invalid secret
	rh.OnAdd(&v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "kuard-ing", Namespace: "default"},
		Spec: v1beta1.IngressSpec{
			TLS: []v1beta1.IngressTLS{{
				Hosts:      []string{"kuard.io"},
				SecretName: "example-tls",
			}},
			Rules: []v1beta1.IngressRule{{
				Host: "kuard.io",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Backend: v1beta1.IngressBackend{
								ServiceName: "kuard",
								ServicePort: intstr.FromInt(80),
							},
						}},
					},
				},
			}},
		},
	})

	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("kuard.io",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/80/da39a3ee5e")),
		),
	), nil)

	// Correct the secret
	rh.OnUpdate(secret, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("cert"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("kuard.io",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/80/da39a3ee5e")),
		),
	), virtualhosts(
		envoy.VirtualHost("kuard.io",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/80/da39a3ee5e")),
		),
	))
}

// issue #257: editing default ingress did not remove original default route
func TestIssue257(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	// apiVersion: extensions/v1beta1
	// kind: Ingress
	// metadata:
	//   name: kuard-ing
	//   labels:
	//     app: kuard
	//   annotations:
	//     kubernetes.io/ingress.class: contour
	// spec:
	//   backend:
	//     serviceName: kuard
	//     servicePort: 80
	i1 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard-ing",
			Namespace: "default",
			Annotations: map[string]string{
				"kubernetes.io/ingress.class": "contour",
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(80),
			},
		},
	}
	rh.OnAdd(i1)

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/80/da39a3ee5e")),
		),
	), nil)

	// apiVersion: extensions/v1beta1
	// kind: Ingress
	// metadata:
	//   name: kuard-ing
	//   labhls:
	//     app: kuard
	//   annotations:
	//     kubernetes.io/ingress.class: contour
	// spec:
	//  rules:
	//  - host: kuard.db.gd-ms.com
	//    http:
	//      paths:
	//      - backend:
	//         serviceName: kuard
	//         servicePort: 80
	//        path: /
	i2 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard-ing",
			Namespace: "default",
			Annotations: map[string]string{
				"kubernetes.io/ingress.class": "contour",
			},
		},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				Host: "kuard.db.gd-ms.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "kuard",
								ServicePort: intstr.FromInt(80),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnUpdate(i1, i2)

	assertRDS(t, cc, "3", virtualhosts(
		envoy.VirtualHost("kuard.db.gd-ms.com",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/80/da39a3ee5e")),
		),
	), nil)
}

func TestRDSFilter(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	// i1 is a stock ingress with force-ssl-redirect on the / route
	i1 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app",
			Namespace: "default",
			Annotations: map[string]string{
				"ingress.kubernetes.io/force-ssl-redirect": "true",
			},
		},
		Spec: v1beta1.IngressSpec{
			TLS: []v1beta1.IngressTLS{{
				Hosts:      []string{"example.com"},
				SecretName: "example-tls",
			}},
			Rules: []v1beta1.IngressRule{{
				Host: "example.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "app-service",
								ServicePort: intstr.FromInt(8080),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnAdd(i1)

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-service",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	// i2 is an overlay to add the let's encrypt handler.
	i2 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "challenge", Namespace: "nginx-ingress"},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				Host: "example.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/.well-known/acme-challenge/gVJl5NWL2owUqZekjHkt_bo3OHYC2XNDURRRgLI5JTk",
							Backend: v1beta1.IngressBackend{
								ServiceName: "challenge-service",
								ServicePort: intstr.FromInt(8009),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnAdd(i2)

	s2 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "challenge-service",
			Namespace: "nginx-ingress",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       8009,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s2)

	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "5",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("example.com",
						envoy.Route(
							envoy.RoutePrefix("/.well-known/acme-challenge/gVJl5NWL2owUqZekjHkt_bo3OHYC2XNDURRRgLI5JTk"),
							routecluster("nginx-ingress/challenge-service/8009/da39a3ee5e"),
						),
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/"), // match all
							Action: envoy.UpgradeHTTPS(),
						},
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "5",
	}, streamRDS(t, cc, "ingress_http"))

	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "5",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_https",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("example.com",
						envoy.Route(
							envoy.RoutePrefix("/.well-known/acme-challenge/gVJl5NWL2owUqZekjHkt_bo3OHYC2XNDURRRgLI5JTk"),
							routecluster("nginx-ingress/challenge-service/8009/da39a3ee5e"),
						),
						envoy.Route(envoy.RoutePrefix("/"), routecluster("default/app-service/8080/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "5",
	}, streamRDS(t, cc, "ingress_https"))
}

func TestWebsocketIngress(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
			Annotations: map[string]string{
				"contour.heptio.com/websocket-routes": "/",
			},
		},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				Host: "websocket.hello.world",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "ws",
								ServicePort: intstr.FromInt(80),
							},
						}},
					},
				},
			}},
		},
	})

	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("websocket.hello.world",
			envoy.Route(
				envoy.RoutePrefix("/"),
				websocketroute("default/ws/80/da39a3ee5e"),
			),
		),
	), nil)
}

func TestWebsocketIngressRoute(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "websocket.hello.world"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Match:            "/ws-1",
				EnableWebsockets: true,
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Match:            "/ws-2",
				EnableWebsockets: true,
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				}},
			}},
		},
	})

	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("websocket.hello.world",
			envoy.Route(envoy.RoutePrefix("/ws-2"), websocketroute("default/ws/80/da39a3ee5e")),
			envoy.Route(envoy.RoutePrefix("/ws-1"), websocketroute("default/ws/80/da39a3ee5e")),
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/ws/80/da39a3ee5e")),
		),
	), nil)
}

func TestWebsocketIngressRoute_MultipleUpstreams(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws2",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "websocket.hello.world"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Match:            "/ws-1",
				EnableWebsockets: true,
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				},
					{
						Name: "ws2",
						Port: 80,
					}},
			}, {
				Match:            "/ws-2",
				EnableWebsockets: true,
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				}},
			}},
		},
	})

	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("websocket.hello.world",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/ws/80/da39a3ee5e")),
		),
	), nil)
}

func TestPrefixRewriteIngressRoute(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "prefixrewrite.hello.world"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Match:         "/ws-1",
				PrefixRewrite: "/",
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Match:         "/ws-2",
				PrefixRewrite: "/",
				Services: []ingressroutev1.Service{{
					Name: "ws",
					Port: 80,
				}},
			}},
		},
	})

	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("prefixrewrite.hello.world",
			envoy.Route(envoy.RoutePrefix("/ws-2"), prefixrewriteroute("default/ws/80/da39a3ee5e")),
			envoy.Route(envoy.RoutePrefix("/ws-1"), prefixrewriteroute("default/ws/80/da39a3ee5e")),
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/ws/80/da39a3ee5e")),
		),
	), nil)
}

// issue 404
func TestDefaultBackendDoesNotOverwriteNamedHost(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}, {
				Name:       "alt",
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gui",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello",
			Namespace: "default",
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(80),
			},

			Rules: []v1beta1.IngressRule{{
				Host: "test-gui",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "test-gui",
								ServicePort: intstr.FromInt(80),
							},
						}},
					},
				},
			}, {
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/kuard",
							Backend: v1beta1.IngressBackend{
								ServiceName: "kuard",
								ServicePort: intstr.FromInt(8080),
							},
						}},
					},
				},
			}},
		},
	})

	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("*",
						envoy.Route(envoy.RoutePrefix("/kuard"), routecluster("default/kuard/8080/da39a3ee5e")),
						envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/80/da39a3ee5e")),
					),
					envoy.VirtualHost("test-gui",
						envoy.Route(envoy.RoutePrefix("/"), routecluster("default/test-gui/80/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc, "ingress_http"))
}

func TestRDSIngressRouteInsideRootNamespaces(t *testing.T) {
	rh, cc, done := setup(t, func(reh *contour.EventHandler) {
		reh.Builder.Source.RootNamespaces = []string{"roots"}
	})
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "roots",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	// ir1 is an ingressroute that is in the root namespaces
	ir1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "roots",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "example.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				Services: []ingressroutev1.Service{{
					Name: "kuard",
					Port: 8080,
				}},
			}},
		},
	}

	// add ingressroute
	rh.OnAdd(ir1)

	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("example.com",
						envoy.Route(envoy.RoutePrefix("/"), routecluster("roots/kuard/8080/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc, "ingress_http"))
}

func TestRDSIngressRouteOutsideRootNamespaces(t *testing.T) {
	rh, cc, done := setup(t, func(reh *contour.EventHandler) {
		reh.Builder.Source.RootNamespaces = []string{"roots"}
	})
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	// ir1 is an ingressroute that is not in the root namespaces
	ir1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "example.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				Services: []ingressroutev1.Service{{
					Name: "kuard",
					Port: 8080,
				}},
			}},
		},
	}

	// add ingressroute
	rh.OnAdd(ir1)

	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc, "ingress_http"))
}

// Test DAGAdapter.IngressClass setting works, this could be done
// in LDS or RDS, or even CDS, but this test mirrors the place it's
// tested in internal/contour/route_test.go
func TestRDSIngressClassAnnotation(t *testing.T) {
	rh, cc, done := setup(t, func(reh *contour.EventHandler) {
		reh.Builder.Source.IngressClass = "linkerd"
	})
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	i1 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard-ing",
			Namespace: "default",
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(8080),
			},
		},
	}
	rh.OnAdd(i1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/8080/da39a3ee5e")),
		),
	), nil)

	i2 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard-ing",
			Namespace: "default",
			Annotations: map[string]string{
				"kubernetes.io/ingress.class": "contour",
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(8080),
			},
		},
	}
	rh.OnUpdate(i1, i2)
	assertRDS(t, cc, "2", nil, nil)

	i3 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard-ing",
			Namespace: "default",
			Annotations: map[string]string{
				"contour.heptio.com/ingress.class": "contour",
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(8080),
			},
		},
	}
	rh.OnUpdate(i2, i3)
	assertRDS(t, cc, "2", nil, nil)

	i4 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard-ing",
			Namespace: "default",
			Annotations: map[string]string{
				"kubernetes.io/ingress.class": "linkerd",
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(8080),
			},
		},
	}
	rh.OnUpdate(i3, i4)
	assertRDS(t, cc, "3", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/8080/da39a3ee5e")),
		),
	), nil)

	i5 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard-ing",
			Namespace: "default",
			Annotations: map[string]string{
				"contour.heptio.com/ingress.class": "linkerd",
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(8080),
			},
		},
	}
	rh.OnUpdate(i4, i5)
	assertRDS(t, cc, "4", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/8080/da39a3ee5e")),
		),
	), nil)

	rh.OnUpdate(i5, i3)
	assertRDS(t, cc, "5", nil, nil)
}

// issue 523, check for data races caused by accidentally
// sorting the contents of an RDS entry's virtualhost list.
func TestRDSAssertNoDataRaceDuringInsertAndStream(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	stop := make(chan struct{})

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	go func() {
		for i := 0; i < 100; i++ {
			rh.OnAdd(&ingressroutev1.IngressRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("simple-%d", i),
					Namespace: "default",
				},
				Spec: ingressroutev1.IngressRouteSpec{
					VirtualHost: &projcontour.VirtualHost{Fqdn: fmt.Sprintf("example-%d.com", i)},
					Routes: []ingressroutev1.Route{{
						Match: "/",
						Services: []ingressroutev1.Service{{
							Name: "kuard",
							Port: 80,
						}},
					}},
				},
			})
		}
		close(stop)
	}()

	for {
		select {
		case <-stop:
			return
		default:
			streamRDS(t, cc)
		}
	}
}

// issue 606: spec.rules.host without a http key causes panic.
// apiVersion: extensions/v1beta1
// kind: Ingress
// metadata:
//   name: test-ingress3
// spec:
//   rules:
//   - host: test1.test.com
//   - host: test2.test.com
//     http:
//       paths:
//       - backend:
//           serviceName: network-test
//           servicePort: 9001
//         path: /
//
// note: this test caused a panic in dag.Builder, but testing the
// context of RDS is a good place to start.
func TestRDSIngressSpecMissingHTTPKey(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	i1 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ingress3",
			Namespace: "default",
		},
		Spec: v1beta1.IngressSpec{
			Rules: []v1beta1.IngressRule{{
				Host: "test1.test.com",
			}, {
				Host: "test2.test.com",
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/",
							Backend: v1beta1.IngressBackend{
								ServiceName: "network-test",
								ServicePort: intstr.FromInt(9001),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnAdd(i1)

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "network-test",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       9001,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/network-test/9001/da39a3ee5e")),
		),
	), nil)
}

func TestRouteWithAServiceWeight(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	ir1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/a",
				Services: []ingressroutev1.Service{{
					Name:   "kuard",
					Port:   80,
					Weight: 90, // ignored
				}},
			}},
		},
	}

	rh.OnAdd(ir1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/a"), routecluster("default/kuard/80/da39a3ee5e")),
		),
	), nil)

	ir2 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/a",
				Services: []ingressroutev1.Service{{
					Name:   "kuard",
					Port:   80,
					Weight: 90,
				}, {
					Name:   "kuard",
					Port:   80,
					Weight: 60,
				}},
			}},
		},
	}

	rh.OnUpdate(ir1, ir2)
	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/a"), routeweightedcluster(
				weightedcluster{"default/kuard/80/da39a3ee5e", 60},
				weightedcluster{"default/kuard/80/da39a3ee5e", 90}),
			),
		),
	), nil)
}

func TestRouteWithTLS(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	ir1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "test2.test.com",
				TLS: &projcontour.TLS{
					SecretName: "example-tls",
				},
			},
			Routes: []ingressroutev1.Route{{
				Match: "/a",
				Services: []ingressroutev1.Service{{
					Name: "kuard",
					Port: 80,
				}},
			}},
		},
	}

	rh.OnAdd(ir1)

	// check that ingress_http has been updated.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/a"),
							Action: envoy.UpgradeHTTPS(),
						},
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						envoy.Route(envoy.RoutePrefix("/a"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc))
}
func TestRouteWithTLS_InsecurePaths(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc2",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	ir1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "test2.test.com",
				TLS: &projcontour.TLS{
					SecretName: "example-tls",
				},
			},
			Routes: []ingressroutev1.Route{{
				Match:          "/insecure",
				PermitInsecure: true,
				Services: []ingressroutev1.Service{{Name: "kuard",
					Port: 80,
				}},
			}, {
				Match: "/secure",
				Services: []ingressroutev1.Service{{
					Name: "svc2",
					Port: 80,
				}},
			}},
		},
	}

	rh.OnAdd(ir1)

	// check that ingress_http has been updated.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/secure"),
							Action: envoy.UpgradeHTTPS(),
						},
						envoy.Route(envoy.RoutePrefix("/insecure"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						envoy.Route(envoy.RoutePrefix("/secure"), routecluster("default/svc2/80/da39a3ee5e")),
						envoy.Route(envoy.RoutePrefix("/insecure"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc))
}

func TestRouteWithTLS_InsecurePaths_DisablePermitInsecureTrue(t *testing.T) {
	rh, cc, done := setup(t, func(reh *contour.EventHandler) {
		reh.DisablePermitInsecure = true
	})

	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc2",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	ir1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "test2.test.com",
				TLS: &projcontour.TLS{
					SecretName: "example-tls",
				},
			},
			Routes: []ingressroutev1.Route{{
				Match:          "/insecure",
				PermitInsecure: true,
				Services: []ingressroutev1.Service{{
					Name: "kuard",
					Port: 80,
				}},
			}, {
				Match: "/secure",
				Services: []ingressroutev1.Service{{
					Name: "svc2",
					Port: 80,
				}},
			}},
		},
	}

	rh.OnAdd(ir1)

	// check that ingress_http has been updated.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/secure"),
							Action: envoy.UpgradeHTTPS(),
						},
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/insecure"),
							Action: envoy.UpgradeHTTPS(),
						},
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						envoy.Route(envoy.RoutePrefix("/secure"), routecluster("default/svc2/80/da39a3ee5e")),
						envoy.Route(envoy.RoutePrefix("/insecure"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc))
}

// issue 665, support for retry-on, num-retries, and per-try-timeout annotations.
func TestRouteRetryAnnotations(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	i1 := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hello", Namespace: "default",
			Annotations: map[string]string{
				"contour.heptio.com/retry-on":        "5xx,gateway-error",
				"contour.heptio.com/num-retries":     "7",
				"contour.heptio.com/per-try-timeout": "120ms",
			},
		},
		Spec: v1beta1.IngressSpec{
			Backend: backend("backend", intstr.FromInt(80)),
		},
	}
	rh.OnAdd(i1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("*",
			envoy.Route(envoy.RoutePrefix("/"), routeretry("default/backend/80/da39a3ee5e", "5xx,gateway-error", 7, 120*time.Millisecond)),
		),
	), nil)
}

// issue 815, support for retry-on, num-retries, and per-try-timeout in IngressRoute
func TestRouteRetryIngressRoute(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	i1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				RetryPolicy: &projcontour.RetryPolicy{
					NumRetries:    7,
					PerTryTimeout: "120ms",
				},
				Services: []ingressroutev1.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}

	rh.OnAdd(i1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), routeretry("default/backend/80/da39a3ee5e", "5xx", 7, 120*time.Millisecond)),
		),
	), nil)
}

// issue 815, support for timeoutpolicy in IngressRoute
func TestRouteTimeoutPolicyIngressRoute(t *testing.T) {
	const (
		durationInfinite  = time.Duration(0)
		duration10Minutes = 10 * time.Minute
	)

	rh, cc, done := setup(t)
	defer done()

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	// i1 is an _invalid_ timeout, which we interpret as _infinite_.
	i1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				Services: []ingressroutev1.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnAdd(i1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/backend/80/da39a3ee5e")),
		),
	), nil)

	// i2 adds an _invalid_ timeout, which we interpret as _infinite_.
	i2 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				TimeoutPolicy: &projcontour.TimeoutPolicy{
					Request: "600",
				},
				Services: []ingressroutev1.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnUpdate(i1, i2)
	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", durationInfinite)),
		),
	), nil)
	// i3 corrects i2 to use a proper duration
	i3 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				TimeoutPolicy: &projcontour.TimeoutPolicy{
					Request: "600s", // 10 * time.Minute
				},
				Services: []ingressroutev1.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnUpdate(i2, i3)
	assertRDS(t, cc, "3", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", duration10Minutes)),
		),
	), nil)
	// i4 updates i3 to explicitly request infinite timeout
	i4 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				TimeoutPolicy: &projcontour.TimeoutPolicy{
					Request: "infinity",
				},
				Services: []ingressroutev1.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnUpdate(i3, i4)
	assertRDS(t, cc, "4", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", durationInfinite)),
		),
	), nil)
}

func TestRouteWithSessionAffinity(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}, {
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	// simple single service
	ir1 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "www.example.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/cart",
				Services: []ingressroutev1.Service{{
					Name:     "app",
					Port:     80,
					Strategy: "Cookie",
				}},
			}},
		},
	}

	rh.OnAdd(ir1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("www.example.com",
			envoy.Route(envoy.RoutePrefix("/cart"), withSessionAffinity(routecluster("default/app/80/e4f81994fe"))),
		),
	), nil)

	// two backends
	ir2 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "www.example.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/cart",
				Services: []ingressroutev1.Service{{
					Name:     "app",
					Port:     80,
					Strategy: "Cookie",
				}, {
					Name:     "app",
					Port:     8080,
					Strategy: "Cookie",
				}},
			}},
		},
	}
	rh.OnUpdate(ir1, ir2)
	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("www.example.com",
			envoy.Route(envoy.RoutePrefix("/cart"), withSessionAffinity(
				routeweightedcluster(
					weightedcluster{"default/app/80/e4f81994fe", 1},
					weightedcluster{"default/app/8080/e4f81994fe", 1}),
			),
			),
		),
	), nil)

	// two mixed backends
	ir3 := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "www.example.com"},
			Routes: []ingressroutev1.Route{{
				Match: "/cart",
				Services: []ingressroutev1.Service{{
					Name:     "app",
					Port:     80,
					Strategy: "Cookie",
				}, {
					Name: "app",
					Port: 8080,
				}},
			}, {
				Match: "/",
				Services: []ingressroutev1.Service{{
					Name: "app",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnUpdate(ir2, ir3)
	assertRDS(t, cc, "3", virtualhosts(
		envoy.VirtualHost("www.example.com",
			envoy.Route(envoy.RoutePrefix("/cart"), withSessionAffinity(
				routeweightedcluster(
					weightedcluster{"default/app/80/e4f81994fe", 1},
					weightedcluster{"default/app/8080/da39a3ee5e", 1},
				),
			),
			),
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/app/80/da39a3ee5e")),
		),
	), nil)

}

// issue 681 Increase the e2e coverage of lb strategies
func TestLoadBalancingStrategies(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	st := v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}

	services := []struct {
		name       string
		lbHash     string
		lbStrategy string
		lbDesc     string
	}{
		{"s1", "f3b72af6a9", "RoundRobin", "RoundRobin lb algorithm"},
		{"s2", "8bf87fefba", "WeightedLeastRequest", "WeightedLeastRequest lb algorithm"},
		{"s5", "58d888c08a", "Random", "Random lb algorithm"},
		{"s6", "da39a3ee5e", "", "Default lb algorithm"},
	}
	ss := make([]ingressroutev1.Service, len(services))
	wc := make([]weightedcluster, len(services))
	for i, x := range services {
		s := st
		s.ObjectMeta.Name = x.name
		rh.OnAdd(&s)
		ss[i] = ingressroutev1.Service{
			Name:     x.name,
			Port:     80,
			Strategy: x.lbStrategy,
		}
		wc[i] = weightedcluster{fmt.Sprintf("default/%s/80/%s", x.name, x.lbHash), 1}
	}

	ir := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []ingressroutev1.Route{{
				Match:    "/a",
				Services: ss,
			}},
		},
	}

	rh.OnAdd(ir)
	want := virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/a"), routeweightedcluster(wc...)),
		),
	)
	assertRDS(t, cc, "1", want, nil)
}

// issue 1234, assert that RoutePrefix and RouteRegex work as expected
func TestRoutePrefixRouteRegex(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	meta := metav1.ObjectMeta{Name: "kuard", Namespace: "default"}

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	// add default/kuard to translator.
	old := &v1beta1.Ingress{
		ObjectMeta: meta,
		Spec: v1beta1.IngressSpec{
			Backend: &v1beta1.IngressBackend{
				ServiceName: "kuard",
				ServicePort: intstr.FromInt(80),
			},
			Rules: []v1beta1.IngressRule{{
				IngressRuleValue: v1beta1.IngressRuleValue{
					HTTP: &v1beta1.HTTPIngressRuleValue{
						Paths: []v1beta1.HTTPIngressPath{{
							Path: "/[^/]+/invoices(/.*|/?)", // issue 1243
							Backend: v1beta1.IngressBackend{
								ServiceName: "kuard",
								ServicePort: intstr.FromInt(80),
							},
						}},
					},
				},
			}},
		},
	}
	rh.OnAdd(old)

	// check that it's been translated correctly.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("*",
						envoy.Route(envoy.RouteRegex("/[^/]+/invoices(/.*|/?)"), routecluster("default/kuard/80/da39a3ee5e")),
						envoy.Route(envoy.RoutePrefix("/"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc))
}

func TestRDSIngressRouteRootCannotDelegateToAnotherRoot(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	svc1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "green",
			Namespace: "marketing",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:     "http",
				Protocol: "TCP",
				Port:     80,
			}},
		},
	}
	rh.OnAdd(svc1)

	child := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "blog",
			Namespace: "marketing",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "www.containersteve.com",
			},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				Services: []ingressroutev1.Service{{
					Name: svc1.Name,
					Port: 80,
				}},
			}},
		},
	}
	rh.OnAdd(child)

	root := &ingressroutev1.IngressRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "root-blog",
			Namespace: "default",
		},
		Spec: ingressroutev1.IngressRouteSpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "blog.containersteve.com",
			},
			Routes: []ingressroutev1.Route{{
				Match: "/",
				Delegate: &ingressroutev1.Delegate{
					Name:      child.Name,
					Namespace: child.Namespace,
				},
			}},
		},
	}
	rh.OnAdd(root)

	// verify that child's route is present because while it is not possible to
	// delegate to it, it can host www.containersteve.com.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "2",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("www.containersteve.com",
						envoy.Route(envoy.RoutePrefix("/"), routecluster("marketing/green/80/da39a3ee5e")),
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
			},
		),
		TypeUrl: routeType,
		Nonce:   "2",
	}, streamRDS(t, cc))

}

func assertRDS(t *testing.T, cc *grpc.ClientConn, versioninfo string, ingress_http, ingress_https []*envoy_api_v2_route.VirtualHost) {
	t.Helper()
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: versioninfo,
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name:         "ingress_http",
				VirtualHosts: ingress_http,
			},
			&v2.RouteConfiguration{
				Name:         "ingress_https",
				VirtualHosts: ingress_https,
			},
		),
		TypeUrl: routeType,
		Nonce:   versioninfo,
	}, streamRDS(t, cc))
}

func streamRDS(t *testing.T, cc *grpc.ClientConn, rn ...string) *v2.DiscoveryResponse {
	t.Helper()
	rds := v2.NewRouteDiscoveryServiceClient(cc)
	st, err := rds.StreamRoutes(context.TODO())
	check(t, err)
	return stream(t, st, &v2.DiscoveryRequest{
		TypeUrl:       routeType,
		ResourceNames: rn,
	})
}

type weightedcluster struct {
	name   string
	weight uint32
}

func withSessionAffinity(r *envoy_api_v2_route.Route_Route) *envoy_api_v2_route.Route_Route {
	r.Route.HashPolicy = append(r.Route.HashPolicy, &envoy_api_v2_route.RouteAction_HashPolicy{
		PolicySpecifier: &envoy_api_v2_route.RouteAction_HashPolicy_Cookie_{
			Cookie: &envoy_api_v2_route.RouteAction_HashPolicy_Cookie{
				Name: "X-Contour-Session-Affinity",
				Ttl:  protobuf.Duration(0),
				Path: "/",
			},
		},
	})
	return r
}

func routecluster(cluster string) *envoy_api_v2_route.Route_Route {
	return &envoy_api_v2_route.Route_Route{
		Route: &envoy_api_v2_route.RouteAction{
			ClusterSpecifier: &envoy_api_v2_route.RouteAction_Cluster{
				Cluster: cluster,
			},
		},
	}
}

func routeweightedcluster(clusters ...weightedcluster) *envoy_api_v2_route.Route_Route {
	return &envoy_api_v2_route.Route_Route{
		Route: &envoy_api_v2_route.RouteAction{
			ClusterSpecifier: &envoy_api_v2_route.RouteAction_WeightedClusters{
				WeightedClusters: weightedclusters(clusters),
			},
		},
	}
}

func weightedclusters(clusters []weightedcluster) *envoy_api_v2_route.WeightedCluster {
	var wc envoy_api_v2_route.WeightedCluster
	var total uint32
	for _, c := range clusters {
		total += c.weight
		wc.Clusters = append(wc.Clusters, &envoy_api_v2_route.WeightedCluster_ClusterWeight{
			Name:   c.name,
			Weight: protobuf.UInt32(c.weight),
		})
	}
	wc.TotalWeight = protobuf.UInt32(total)
	return &wc
}

func websocketroute(c string) *envoy_api_v2_route.Route_Route {
	cl := routecluster(c)
	cl.Route.UpgradeConfigs = append(cl.Route.UpgradeConfigs,
		&envoy_api_v2_route.RouteAction_UpgradeConfig{
			UpgradeType: "websocket",
		},
	)
	return cl
}

func prefixrewriteroute(c string) *envoy_api_v2_route.Route_Route {
	cl := routecluster(c)
	cl.Route.PrefixRewrite = "/"
	return cl
}

func clustertimeout(c string, timeout time.Duration) *envoy_api_v2_route.Route_Route {
	cl := routecluster(c)
	cl.Route.Timeout = protobuf.Duration(timeout)
	return cl
}

func service(ns, name string, ports ...v1.ServicePort) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: v1.ServiceSpec{
			Ports: ports,
		},
	}
}

func externalnameservice(ns, name, externalname string, ports ...v1.ServicePort) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: v1.ServiceSpec{
			Ports:        ports,
			ExternalName: externalname,
			Type:         v1.ServiceTypeExternalName,
		},
	}
}

func routeretry(cluster string, retryOn string, numRetries uint32, perTryTimeout time.Duration) *envoy_api_v2_route.Route_Route {
	r := routecluster(cluster)
	r.Route.RetryPolicy = &envoy_api_v2_route.RetryPolicy{
		RetryOn: retryOn,
	}
	if numRetries > 0 {
		r.Route.RetryPolicy.NumRetries = protobuf.UInt32(numRetries)
	}
	if perTryTimeout > 0 {
		r.Route.RetryPolicy.PerTryTimeout = protobuf.Duration(perTryTimeout)
	}
	return r
}

func TestRDSHTTPProxyOutsideRootNamespaces(t *testing.T) {
	rh, cc, done := setup(t, func(reh *contour.EventHandler) {
		reh.Builder.Source.RootNamespaces = []string{"roots"}
	})
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	// proxy1 is an httpproxy that is not in the root namespaces
	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "example.com"},
			Routes: []projcontour.Route{{
				Services: []projcontour.Service{{
					Name: "kuard",
					Port: 8080,
				}},
			}},
		},
	}

	// add proxy
	rh.OnAdd(proxy1)

	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc, "ingress_http"))
}

func TestHTTPProxyRouteWithAServiceWeight(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/a",
				},
				Services: []projcontour.Service{{
					Name:   "kuard",
					Port:   80,
					Weight: 90, // ignored
				}},
			}},
		},
	}

	rh.OnAdd(proxy1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/a"), routecluster("default/kuard/80/da39a3ee5e")),
		),
	), nil)

	proxy2 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/a",
				},
				Services: []projcontour.Service{{
					Name:   "kuard",
					Port:   80,
					Weight: 90,
				}, {
					Name:   "kuard",
					Port:   80,
					Weight: 60,
				}},
			}},
		},
	}

	rh.OnUpdate(proxy1, proxy2)
	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/a"), routeweightedcluster(
				weightedcluster{"default/kuard/80/da39a3ee5e", 60},
				weightedcluster{"default/kuard/80/da39a3ee5e", 90}),
			),
		),
	), nil)
}

func TestWebsocketHTTProxy(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "websocket.hello.world"},
			Routes: []projcontour.Route{{
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Condition: &projcontour.Condition{
					Prefix: "/ws-1",
				},
				EnableWebsockets: true,
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Condition: &projcontour.Condition{
					Prefix: "/ws-2",
				},
				EnableWebsockets: true,
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				}},
			}},
		},
	})

	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("websocket.hello.world",
			envoy.Route(envoy.RoutePrefix("/ws-2"), websocketroute("default/ws/80/da39a3ee5e")),
			envoy.Route(envoy.RoutePrefix("/ws-1"), websocketroute("default/ws/80/da39a3ee5e")),
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/ws/80/da39a3ee5e")),
		),
	), nil)
}

func TestWebsocketHTTPProxy_MultipleUpstreams(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws2",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "websocket.hello.world"},
			Routes: []projcontour.Route{{
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Condition: &projcontour.Condition{
					Prefix: "/ws-1",
				},
				EnableWebsockets: true,
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				},
					{
						Name: "ws2",
						Port: 80,
					}},
			}, {
				Condition: &projcontour.Condition{
					Prefix: "/ws-2",
				},
				EnableWebsockets: true,
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				}},
			}},
		},
	})

	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("websocket.hello.world",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/ws/80/da39a3ee5e")),
		),
	), nil)
}

func TestPrefixRewriteHTTPProxy(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ws",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "prefixrewrite.hello.world"},
			Routes: []projcontour.Route{{
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Condition: &projcontour.Condition{
					Prefix: "/ws-1",
				},
				PrefixRewrite: "/",
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				}},
			}, {
				Condition: &projcontour.Condition{
					Prefix: "/ws-2",
				},
				PrefixRewrite: "/",
				Services: []projcontour.Service{{
					Name: "ws",
					Port: 80,
				}},
			}},
		},
	})

	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("prefixrewrite.hello.world",
			envoy.Route(envoy.RoutePrefix("/ws-2"), prefixrewriteroute("default/ws/80/da39a3ee5e")),
			envoy.Route(envoy.RoutePrefix("/ws-1"), prefixrewriteroute("default/ws/80/da39a3ee5e")),
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/ws/80/da39a3ee5e")),
		),
	), nil)
}

func TestHTTPProxyRouteWithTLS(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "test2.test.com",
				TLS: &projcontour.TLS{
					SecretName: "example-tls",
				},
			},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/a",
				},
				Services: []projcontour.Service{{
					Name: "kuard",
					Port: 80,
				}},
			}},
		},
	}

	rh.OnAdd(proxy1)

	// check that ingress_http has been updated.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/a"),
							Action: envoy.UpgradeHTTPS(),
						},
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						envoy.Route(envoy.RoutePrefix("/a"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc))
}

func TestHTTPProxyRouteWithTLS_InsecurePaths(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc2",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "test2.test.com",
				TLS: &projcontour.TLS{
					SecretName: "example-tls",
				},
			},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/insecure",
				},
				PermitInsecure: true,
				Services: []projcontour.Service{{Name: "kuard",
					Port: 80,
				}},
			}, {
				Condition: &projcontour.Condition{
					Prefix: "/secure",
				},
				Services: []projcontour.Service{{
					Name: "svc2",
					Port: 80,
				}},
			}},
		},
	}

	rh.OnAdd(proxy1)

	// check that ingress_http has been updated.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/secure"),
							Action: envoy.UpgradeHTTPS(),
						},
						envoy.Route(envoy.RoutePrefix("/insecure"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						envoy.Route(envoy.RoutePrefix("/secure"), routecluster("default/svc2/80/da39a3ee5e")),
						envoy.Route(envoy.RoutePrefix("/insecure"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc))
}

func TestHTTPProxyRouteWithTLS_InsecurePaths_DisablePermitInsecureTrue(t *testing.T) {
	rh, cc, done := setup(t, func(reh *contour.EventHandler) {
		reh.DisablePermitInsecure = true
	})

	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kuard",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc2",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	rh.OnAdd(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-tls",
			Namespace: "default",
		},
		Type: "kubernetes.io/tls",
		Data: map[string][]byte{
			v1.TLSCertKey:       []byte("certificate"),
			v1.TLSPrivateKeyKey: []byte("key"),
		},
	})

	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "test2.test.com",
				TLS: &projcontour.TLS{
					SecretName: "example-tls",
				},
			},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/insecure",
				},
				PermitInsecure: true,
				Services: []projcontour.Service{{
					Name: "kuard",
					Port: 80,
				}},
			}, {
				Condition: &projcontour.Condition{
					Prefix: "/secure",
				},
				Services: []projcontour.Service{{
					Name: "svc2",
					Port: 80,
				}},
			}},
		},
	}

	rh.OnAdd(proxy1)

	// check that ingress_http has been updated.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "1",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/secure"),
							Action: envoy.UpgradeHTTPS(),
						},
						&envoy_api_v2_route.Route{
							Match:  envoy.RoutePrefix("/insecure"),
							Action: envoy.UpgradeHTTPS(),
						},
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("test2.test.com",
						envoy.Route(envoy.RoutePrefix("/secure"), routecluster("default/svc2/80/da39a3ee5e")),
						envoy.Route(envoy.RoutePrefix("/insecure"), routecluster("default/kuard/80/da39a3ee5e")),
					),
				),
			},
		),
		TypeUrl: routeType,
		Nonce:   "1",
	}, streamRDS(t, cc))
}

func TestRDSHTTPProxyRootCannotDelegateToAnotherRoot(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	svc1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "green",
			Namespace: "marketing",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Name:     "http",
				Protocol: "TCP",
				Port:     80,
			}},
		},
	}
	rh.OnAdd(svc1)

	child := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "blog",
			Namespace: svc1.Namespace,
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "www.containersteve.com",
			},
			Routes: []projcontour.Route{{
				Services: []projcontour.Service{{
					Name: svc1.Name,
					Port: 80,
				}},
			}},
		},
	}
	rh.OnAdd(child)

	root := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "root-blog",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{
				Fqdn: "blog.containersteve.com",
			},
			Includes: []projcontour.Include{{
				Name:      child.Name,
				Namespace: child.Namespace,
			}},
		},
	}
	rh.OnAdd(root)

	// verify that child's route is present because while it is not possible to
	// delegate to it, it can host www.containersteve.com.
	assertEqual(t, &v2.DiscoveryResponse{
		VersionInfo: "2",
		Resources: resources(t,
			&v2.RouteConfiguration{
				Name: "ingress_http",
				VirtualHosts: virtualhosts(
					envoy.VirtualHost("www.containersteve.com",
						envoy.Route(envoy.RoutePrefix("/"), routecluster("marketing/green/80/da39a3ee5e")),
					),
				),
			},
			&v2.RouteConfiguration{
				Name: "ingress_https",
			},
		),
		TypeUrl: routeType,
		Nonce:   "2",
	}, streamRDS(t, cc))

}

// issue 681 Increase the e2e coverage of lb strategies
func TestHTTPProxyLoadBalancingStrategies(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	st := v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}

	services := []struct {
		name       string
		lbHash     string
		lbStrategy string
		lbDesc     string
	}{
		{"s1", "f3b72af6a9", "RoundRobin", "RoundRobin lb algorithm"},
		{"s2", "8bf87fefba", "WeightedLeastRequest", "WeightedLeastRequest lb algorithm"},
		{"s5", "58d888c08a", "Random", "Random lb algorithm"},
		{"s6", "da39a3ee5e", "", "Default lb algorithm"},
	}
	ss := make([]projcontour.Service, len(services))
	wc := make([]weightedcluster, len(services))
	for i, x := range services {
		s := st
		s.ObjectMeta.Name = x.name
		rh.OnAdd(&s)
		ss[i] = projcontour.Service{
			Name:     x.name,
			Port:     80,
			Strategy: x.lbStrategy,
		}
		wc[i] = weightedcluster{fmt.Sprintf("default/%s/80/%s", x.name, x.lbHash), 1}
	}

	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/a",
				},
				Services: ss,
			}},
		},
	}

	rh.OnAdd(proxy1)
	want := virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/a"), routeweightedcluster(wc...)),
		),
	)
	assertRDS(t, cc, "1", want, nil)
}

func TestHTTPProxyRouteWithSessionAffinity(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	rh.OnAdd(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}, {
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	})

	// simple single service
	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "www.example.com"},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/cart",
				},
				Services: []projcontour.Service{{
					Name:     "app",
					Port:     80,
					Strategy: "Cookie",
				}},
			}},
		},
	}

	rh.OnAdd(proxy1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("www.example.com",
			envoy.Route(envoy.RoutePrefix("/cart"), withSessionAffinity(routecluster("default/app/80/e4f81994fe"))),
		),
	), nil)

	// two backends
	proxy2 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "www.example.com"},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/cart",
				},
				Services: []projcontour.Service{{
					Name:     "app",
					Port:     80,
					Strategy: "Cookie",
				}, {
					Name:     "app",
					Port:     8080,
					Strategy: "Cookie",
				}},
			}},
		},
	}
	rh.OnUpdate(proxy1, proxy2)
	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("www.example.com",
			envoy.Route(envoy.RoutePrefix("/cart"), withSessionAffinity(
				routeweightedcluster(
					weightedcluster{"default/app/80/e4f81994fe", 1},
					weightedcluster{"default/app/8080/e4f81994fe", 1}),
			),
			),
		),
	), nil)

	// two mixed backends
	proxy3 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "www.example.com"},
			Routes: []projcontour.Route{{
				Condition: &projcontour.Condition{
					Prefix: "/cart",
				},
				Services: []projcontour.Service{{
					Name:     "app",
					Port:     80,
					Strategy: "Cookie",
				}, {
					Name: "app",
					Port: 8080,
				}},
			}, {
				Services: []projcontour.Service{{
					Name: "app",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnUpdate(proxy2, proxy3)
	assertRDS(t, cc, "3", virtualhosts(
		envoy.VirtualHost("www.example.com",
			envoy.Route(envoy.RoutePrefix("/cart"), withSessionAffinity(
				routeweightedcluster(
					weightedcluster{"default/app/80/e4f81994fe", 1},
					weightedcluster{"default/app/8080/da39a3ee5e", 1},
				),
			),
			),
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/app/80/da39a3ee5e")),
		),
	), nil)

}

// issue 815, support for timeoutpolicy in HTTPProxy
func TestTimeoutPolicyHTTProxyRoute(t *testing.T) {
	const (
		durationInfinite  = time.Duration(0)
		duration10Minutes = 10 * time.Minute
	)

	rh, cc, done := setup(t)
	defer done()

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	// proxy1 is an _invalid_ timeout, which we interpret as _infinite_.
	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []projcontour.Route{{
				Services: []projcontour.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnAdd(proxy1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), routecluster("default/backend/80/da39a3ee5e")),
		),
	), nil)

	// proxy2 adds an _invalid_ timeout, which we interpret as _infinite_.
	proxy2 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []projcontour.Route{{
				TimeoutPolicy: &projcontour.TimeoutPolicy{
					Request: "600",
				},
				Services: []projcontour.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnUpdate(proxy1, proxy2)
	assertRDS(t, cc, "2", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", durationInfinite)),
		),
	), nil)

	// proxy3 corrects proxy2 to use a proper duration
	proxy3 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []projcontour.Route{{
				TimeoutPolicy: &projcontour.TimeoutPolicy{
					Request: "600s", // 10 * time.Minute
				},
				Services: []projcontour.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnUpdate(proxy2, proxy3)
	assertRDS(t, cc, "3", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", duration10Minutes)),
		),
	), nil)

	// proxy4 updates proxy3 to explicitly request infinite timeout
	proxy4 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []projcontour.Route{{
				TimeoutPolicy: &projcontour.TimeoutPolicy{
					Request: "infinity",
				},
				Services: []projcontour.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}
	rh.OnUpdate(proxy3, proxy4)
	assertRDS(t, cc, "4", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), clustertimeout("default/backend/80/da39a3ee5e", durationInfinite)),
		),
	), nil)
}

// issue 815, support for retry-on, num-retries, and per-try-timeout in HTTPProxy
func TestRouteRetryHTTPProxy(t *testing.T) {
	rh, cc, done := setup(t)
	defer done()

	s1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "backend",
			Namespace: "default",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	rh.OnAdd(s1)

	proxy1 := &projcontour.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "simple",
			Namespace: "default",
		},
		Spec: projcontour.HTTPProxySpec{
			VirtualHost: &projcontour.VirtualHost{Fqdn: "test2.test.com"},
			Routes: []projcontour.Route{{
				RetryPolicy: &projcontour.RetryPolicy{
					NumRetries:    7,
					PerTryTimeout: "120ms",
				},
				Services: []projcontour.Service{{
					Name: "backend",
					Port: 80,
				}},
			}},
		},
	}

	rh.OnAdd(proxy1)
	assertRDS(t, cc, "1", virtualhosts(
		envoy.VirtualHost("test2.test.com",
			envoy.Route(envoy.RoutePrefix("/"), routeretry("default/backend/80/da39a3ee5e", "5xx", 7, 120*time.Millisecond)),
		),
	), nil)
}

func virtualhosts(v ...*envoy_api_v2_route.VirtualHost) []*envoy_api_v2_route.VirtualHost { return v }
