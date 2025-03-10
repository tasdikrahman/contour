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

package envoy

import (
	"sort"

	envoy_api_v2_core "github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	envoy_api_v2_route "github.com/envoyproxy/go-control-plane/envoy/api/v2/route"
	"github.com/golang/protobuf/ptypes/duration"
	"github.com/projectcontour/contour/internal/dag"
	"github.com/projectcontour/contour/internal/protobuf"
)

// Routes returns a []*envoy_api_v2_route.Route for the supplied routes.
func Routes(routes ...*envoy_api_v2_route.Route) []*envoy_api_v2_route.Route {
	return routes
}

// Route returns a *envoy_api_v2_route.Route for the supplied match and action.
func Route(match *envoy_api_v2_route.RouteMatch, action *envoy_api_v2_route.Route_Route) *envoy_api_v2_route.Route {
	return &envoy_api_v2_route.Route{
		Match:               match,
		Action:              action,
		RequestHeadersToAdd: RouteHeaders(),
	}
}

// RouteRoute creates a *envoy_api_v2_route.Route_Route for the services supplied.
// If len(services) is greater than one, the route's action will be a
// weighted cluster.
func RouteRoute(r *dag.Route) *envoy_api_v2_route.Route_Route {
	ra := envoy_api_v2_route.RouteAction{
		RetryPolicy:   retryPolicy(r),
		Timeout:       timeout(r),
		PrefixRewrite: r.PrefixRewrite,
		HashPolicy:    hashPolicy(r),
	}

	if r.Websocket {
		ra.UpgradeConfigs = append(ra.UpgradeConfigs,
			&envoy_api_v2_route.RouteAction_UpgradeConfig{
				UpgradeType: "websocket",
			},
		)
	}

	switch len(r.Clusters) {
	case 1:
		ra.ClusterSpecifier = &envoy_api_v2_route.RouteAction_Cluster{
			Cluster: Clustername(r.Clusters[0]),
		}
	default:
		ra.ClusterSpecifier = &envoy_api_v2_route.RouteAction_WeightedClusters{
			WeightedClusters: weightedClusters(r.Clusters),
		}
	}
	return &envoy_api_v2_route.Route_Route{
		Route: &ra,
	}
}

// hashPolicy returns a slice of hash policies iff at least one of the route's
// clusters supplied uses the `Cookie` load balancing stategy.
func hashPolicy(r *dag.Route) []*envoy_api_v2_route.RouteAction_HashPolicy {
	for _, c := range r.Clusters {
		if c.LoadBalancerStrategy == "Cookie" {
			return []*envoy_api_v2_route.RouteAction_HashPolicy{{
				PolicySpecifier: &envoy_api_v2_route.RouteAction_HashPolicy_Cookie_{
					Cookie: &envoy_api_v2_route.RouteAction_HashPolicy_Cookie{
						Name: "X-Contour-Session-Affinity",
						Ttl:  protobuf.Duration(0),
						Path: "/",
					},
				},
			}}
		}
	}
	return nil
}

func timeout(r *dag.Route) *duration.Duration {
	if r.TimeoutPolicy == nil {
		return nil
	}

	switch r.TimeoutPolicy.Timeout {
	case 0:
		// no timeout specified
		return nil
	case -1:
		// infinite timeout, set timeout value to a pointer to zero which tells
		// envoy "infinite timeout"
		return protobuf.Duration(0)
	default:
		return protobuf.Duration(r.TimeoutPolicy.Timeout)
	}
}

func retryPolicy(r *dag.Route) *envoy_api_v2_route.RetryPolicy {
	if r.RetryPolicy == nil {
		return nil
	}
	if r.RetryPolicy.RetryOn == "" {
		return nil
	}

	rp := &envoy_api_v2_route.RetryPolicy{
		RetryOn: r.RetryPolicy.RetryOn,
	}
	if r.RetryPolicy.NumRetries > 0 {
		rp.NumRetries = protobuf.UInt32(r.RetryPolicy.NumRetries)
	}
	if r.RetryPolicy.PerTryTimeout > 0 {
		rp.PerTryTimeout = protobuf.Duration(r.RetryPolicy.PerTryTimeout)
	}
	return rp
}

// UpgradeHTTPS returns a route Action that redirects the request to HTTPS.
func UpgradeHTTPS() *envoy_api_v2_route.Route_Redirect {
	return &envoy_api_v2_route.Route_Redirect{
		Redirect: &envoy_api_v2_route.RedirectAction{
			SchemeRewriteSpecifier: &envoy_api_v2_route.RedirectAction_HttpsRedirect{
				HttpsRedirect: true,
			},
		},
	}
}

// RouteHeaders returns a list of headers to be applied at the Route level on envoy
func RouteHeaders() []*envoy_api_v2_core.HeaderValueOption {
	return headers(
		appendHeader("x-request-start", "t=%START_TIME(%s.%3f)%"),
	)
}

// weightedClusters returns a route.WeightedCluster for multiple services.
func weightedClusters(clusters []*dag.Cluster) *envoy_api_v2_route.WeightedCluster {
	var wc envoy_api_v2_route.WeightedCluster
	var total uint32
	for _, cluster := range clusters {
		total += cluster.Weight
		wc.Clusters = append(wc.Clusters, &envoy_api_v2_route.WeightedCluster_ClusterWeight{
			Name:   Clustername(cluster),
			Weight: protobuf.UInt32(cluster.Weight),
		})
	}
	// Check if no weights were defined, if not default to even distribution
	if total == 0 {
		for _, c := range wc.Clusters {
			c.Weight.Value = 1
		}
		total = uint32(len(clusters))
	}
	wc.TotalWeight = protobuf.UInt32(total)

	sort.Stable(clusterWeightByName(wc.Clusters))
	return &wc
}

// RouteRegex returns a regex matcher.
func RouteRegex(regex string) *envoy_api_v2_route.RouteMatch {
	return &envoy_api_v2_route.RouteMatch{
		PathSpecifier: &envoy_api_v2_route.RouteMatch_Regex{
			Regex: regex,
		},
	}
}

// RoutePrefix returns a prefix matcher.
func RoutePrefix(prefix string) *envoy_api_v2_route.RouteMatch {
	return &envoy_api_v2_route.RouteMatch{
		PathSpecifier: &envoy_api_v2_route.RouteMatch_Prefix{
			Prefix: prefix,
		},
	}
}

// VirtualHost creates a new route.VirtualHost.
func VirtualHost(hostname string, routes ...*envoy_api_v2_route.Route) *envoy_api_v2_route.VirtualHost {
	domains := []string{hostname}
	if hostname != "*" {
		domains = append(domains, hostname+":*")
	}
	return &envoy_api_v2_route.VirtualHost{
		Name:    hashname(60, hostname),
		Domains: domains,
		Routes:  routes,
	}
}

type clusterWeightByName []*envoy_api_v2_route.WeightedCluster_ClusterWeight

func (c clusterWeightByName) Len() int      { return len(c) }
func (c clusterWeightByName) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c clusterWeightByName) Less(i, j int) bool {
	if c[i].Name == c[j].Name {
		return c[i].Weight.Value < c[j].Weight.Value
	}
	return c[i].Name < c[j].Name

}

func headers(first *envoy_api_v2_core.HeaderValueOption, rest ...*envoy_api_v2_core.HeaderValueOption) []*envoy_api_v2_core.HeaderValueOption {
	return append([]*envoy_api_v2_core.HeaderValueOption{first}, rest...)
}

func appendHeader(key, value string) *envoy_api_v2_core.HeaderValueOption {
	return &envoy_api_v2_core.HeaderValueOption{
		Header: &envoy_api_v2_core.HeaderValue{
			Key:   key,
			Value: value,
		},
		Append: protobuf.Bool(true),
	}
}
