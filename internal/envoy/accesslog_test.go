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

package envoy

import (
	"encoding/json"
	"fmt"
	"testing"

	accesslog_v2 "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v2"
	envoy_accesslog "github.com/envoyproxy/go-control-plane/envoy/config/filter/accesslog/v2"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	any "github.com/golang/protobuf/ptypes/any"
	_struct "github.com/golang/protobuf/ptypes/struct"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestFileAccessLog(t *testing.T) {
	tests := map[string]struct {
		path string
		want []*envoy_accesslog.AccessLog
	}{
		"stdout": {
			path: "/dev/stdout",
			want: []*envoy_accesslog.AccessLog{{
				Name: wellknown.FileAccessLog,
				ConfigType: &envoy_accesslog.AccessLog_TypedConfig{
					TypedConfig: toAny(&accesslog_v2.FileAccessLog{
						Path: "/dev/stdout",
					}),
				},
			}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := FileAccessLog(tc.path)
			if diff := cmp.Diff(tc.want, got, cmpopts.AcyclicTransformer("unmarshalAny", unmarshalAny)); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestJSONFileAccessLog(t *testing.T) {
	tests := map[string]struct {
		path    string
		headers []string
		want    []*envoy_accesslog.AccessLog
	}{
		"only timestamp": {
			path:    "/dev/stdout",
			headers: []string{"@timestamp"},
			want: []*envoy_accesslog.AccessLog{{
				Name: wellknown.FileAccessLog,
				ConfigType: &envoy_accesslog.AccessLog_TypedConfig{
					TypedConfig: toAny(&accesslog_v2.FileAccessLog{
						Path: "/dev/stdout",
						AccessLogFormat: &accesslog_v2.FileAccessLog_JsonFormat{
							JsonFormat: &_struct.Struct{
								Fields: map[string]*_struct.Value{
									"@timestamp": sv("%START_TIME%"),
								},
							},
						},
					}),
				},
			},
			},
		},
		"invalid header should disappear": {
			path: "/dev/stdout",
			headers: []string{
				"@timestamp",
				"invalid",
				"method",
			},
			want: []*envoy_accesslog.AccessLog{{
				Name: wellknown.FileAccessLog,
				ConfigType: &envoy_accesslog.AccessLog_TypedConfig{
					TypedConfig: toAny(&accesslog_v2.FileAccessLog{
						Path: "/dev/stdout",
						AccessLogFormat: &accesslog_v2.FileAccessLog_JsonFormat{
							JsonFormat: &_struct.Struct{
								Fields: map[string]*_struct.Value{
									"@timestamp": sv(JSONFields["@timestamp"]),
									"method":     sv(JSONFields["method"]),
								},
							},
						},
					}),
				},
			},
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := FileAccessLogJSON(tc.path, tc.headers)
			output, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Printf("%s\n", output)
			if diff := cmp.Diff(tc.want, got, cmpopts.AcyclicTransformer("unmarshalAny", unmarshalAny)); diff != "" {
				t.Fatal(diff)
			}
		})
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
