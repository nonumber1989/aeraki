// Copyright Aeraki Authors
//
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

package envoyfilter

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/aeraki-framework/aeraki/pkg/model"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	gogojsonpb "github.com/gogo/protobuf/jsonpb"
	"github.com/gogo/protobuf/types"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	networking "istio.io/api/networking/v1alpha3"
	"istio.io/pkg/log"
)

var generatorLog = log.RegisterScope("aeraki-generator", "aeraki generator", 0)

// GenerateInsertBeforeNetworkFilter generates an EnvoyFilter that inserts a protocol specified filter before the tcp proxy
func GenerateInsertBeforeNetworkFilter(service *model.ServiceEntryWrapper, outboundProxy proto.Message,
	inboundProxy proto.Message, filterName string, filterType string) []*model.EnvoyFilterWrapper {
	return generateNetworkFilter(service, outboundProxy, inboundProxy, filterName, filterType, networking.EnvoyFilter_Patch_INSERT_BEFORE)
}

// GenerateReplaceNetworkFilter generates an EnvoyFilter that replaces the default tcp proxy with a protocol specified proxy
func GenerateReplaceNetworkFilter(service *model.ServiceEntryWrapper, outboundProxy proto.Message,
	inboundProxy proto.Message, filterName string, filterType string) []*model.EnvoyFilterWrapper {
	return generateNetworkFilter(service, outboundProxy, inboundProxy, filterName, filterType, networking.EnvoyFilter_Patch_REPLACE)
}

// GenerateReplaceNetworkFilter generates an EnvoyFilter that replaces the default tcp proxy with a protocol specified proxy
func generateNetworkFilter(service *model.ServiceEntryWrapper, outboundProxy proto.Message,
	inboundProxy proto.Message, filterName string, filterType string, operation networking.EnvoyFilter_Patch_Operation) []*model.EnvoyFilterWrapper {
	var outboundProxyPatch, inboundProxyPatch *networking.EnvoyFilter_EnvoyConfigObjectPatch
	if outboundProxy != nil {
		outboundProxyStruct, err := generateValue(outboundProxy, filterName, filterType)
		if err != nil {
			//This should not happen
			generatorLog.Errorf("Failed to generate outbound EnvoyFilter: %v", err)
			return nil
		} else if len(service.Spec.GetAddresses()) == 0 {
			generatorLog.Infof("Service doesn't have VIP: %v", service)
		} else {
			outboundListenerName := service.Spec.GetAddresses()[0] + "_" + strconv.Itoa(int(service.Spec.Ports[0].
				Number))
			outboundProxyPatch = &networking.EnvoyFilter_EnvoyConfigObjectPatch{
				ApplyTo: networking.EnvoyFilter_NETWORK_FILTER,
				Match: &networking.EnvoyFilter_EnvoyConfigObjectMatch{
					ObjectTypes: &networking.EnvoyFilter_EnvoyConfigObjectMatch_Listener{
						Listener: &networking.EnvoyFilter_ListenerMatch{
							Name: outboundListenerName,
							FilterChain: &networking.EnvoyFilter_ListenerMatch_FilterChainMatch{
								Filter: &networking.EnvoyFilter_ListenerMatch_FilterMatch{
									Name: wellknown.TCPProxy,
								},
							},
						},
					},
				},
				Patch: &networking.EnvoyFilter_Patch{
					Operation: operation,
					Value:     outboundProxyStruct,
				},
			}
		}
	}

	WorkloadSelector := inboudEnvoyFilterWorkloadSelector(service)

	// a workload selector should be set in an inbound envoy filter so wo won't override the inbound config of other
	// services at the same port
	if inboundProxy != nil && hasInboundWorkloadSelector(WorkloadSelector) {
		inboundProxyStruct, err := generateValue(inboundProxy, filterName, filterType)
		if err != nil {
			//This should not happen
			generatorLog.Errorf("Failed to generate inbound EnvoyFilter: %v", err)
		} else {
			inboundProxyPatch = &networking.EnvoyFilter_EnvoyConfigObjectPatch{
				ApplyTo: networking.EnvoyFilter_NETWORK_FILTER,
				Match: &networking.EnvoyFilter_EnvoyConfigObjectMatch{
					ObjectTypes: &networking.EnvoyFilter_EnvoyConfigObjectMatch_Listener{
						Listener: &networking.EnvoyFilter_ListenerMatch{
							Name: "virtualInbound",
							FilterChain: &networking.EnvoyFilter_ListenerMatch_FilterChainMatch{
								DestinationPort: service.Spec.Ports[0].Number,
								Filter: &networking.EnvoyFilter_ListenerMatch_FilterMatch{
									Name: wellknown.TCPProxy,
								},
							},
						},
					},
				},
				Patch: &networking.EnvoyFilter_Patch{
					Operation: operation,
					Value:     inboundProxyStruct,
				},
			}
		}
	}

	if outboundProxyPatch != nil && inboundProxyPatch != nil {
		return []*model.EnvoyFilterWrapper{
			{
				Name: outboundEnvoyFilterName(service.Spec),
				Envoyfilter: &networking.EnvoyFilter{
					ConfigPatches: []*networking.EnvoyFilter_EnvoyConfigObjectPatch{outboundProxyPatch},
				},
			},
			{
				Name: inboundEnvoyFilterName(service.Spec),
				Envoyfilter: &networking.EnvoyFilter{
					WorkloadSelector: WorkloadSelector,
					ConfigPatches:    []*networking.EnvoyFilter_EnvoyConfigObjectPatch{inboundProxyPatch},
				},
			}}
	}
	if outboundProxyPatch != nil {
		return []*model.EnvoyFilterWrapper{
			{
				Name: outboundEnvoyFilterName(service.Spec),
				Envoyfilter: &networking.EnvoyFilter{
					ConfigPatches: []*networking.EnvoyFilter_EnvoyConfigObjectPatch{outboundProxyPatch},
				},
			}}
	}
	if inboundProxyPatch != nil {
		return []*model.EnvoyFilterWrapper{
			{
				Name: inboundEnvoyFilterName(service.Spec),
				Envoyfilter: &networking.EnvoyFilter{
					WorkloadSelector: WorkloadSelector,
					ConfigPatches:    []*networking.EnvoyFilter_EnvoyConfigObjectPatch{inboundProxyPatch},
				},
			}}
	}
	return []*model.EnvoyFilterWrapper{}
}

func hasInboundWorkloadSelector(selector *networking.WorkloadSelector) bool {
	return len(selector.Labels) != 0
}

func inboudEnvoyFilterWorkloadSelector(service *model.ServiceEntryWrapper) *networking.WorkloadSelector {
	selector := service.Spec.WorkloadSelector
	if selector == nil {
		selector = &networking.WorkloadSelector{
			Labels: make(map[string]string),
		}
	}
	if len(selector.Labels) == 0 {
		label := strings.ReplaceAll(service.Annotations["workloadSelector"], " ", "")
		if label != "" {
			selector.Labels["app"] = label
		}
	}
	return selector
}

func outboundEnvoyFilterName(service *networking.ServiceEntry) string {
	return "aeraki" + "-outbound-" + service.Hosts[0]
}

func inboundEnvoyFilterName(service *networking.ServiceEntry) string {
	return "aeraki" + "-inbound-" + service.Hosts[0]
}

func generateValue(proxy proto.Message, filterName string, filterType string) (*types.Struct, error) {
	var buf []byte
	var err error

	if buf, err = protojson.Marshal(proxy); err != nil {
		return nil, err
	}

	var value = &types.Struct{}
	if err = (&gogojsonpb.Unmarshaler{AllowUnknownFields: false}).Unmarshal(bytes.NewBuffer(buf), value); err != nil {
		return nil, err
	}

	var out = &types.Struct{}
	out.Fields = map[string]*types.Value{}
	out.Fields["@type"] = &types.Value{Kind: &types.Value_StringValue{
		StringValue: "type.googleapis.com/udpa.type.v1.TypedStruct",
	}}
	out.Fields["type_url"] = &types.Value{Kind: &types.Value_StringValue{
		StringValue: filterType,
	}}
	out.Fields["value"] = &types.Value{Kind: &types.Value_StructValue{
		StructValue: value,
	}}

	return &types.Struct{
		Fields: map[string]*types.Value{
			"name": {
				Kind: &types.Value_StringValue{
					StringValue: filterName,
				},
			},
			"typed_config": {
				Kind: &types.Value_StructValue{StructValue: out},
			},
		},
	}, nil
}
