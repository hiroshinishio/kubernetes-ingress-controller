/*
Copyright 2023 Kong, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	sdkkonnectgocomp "github.com/Kong/sdk-konnect-go/models/components"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	// TODO(pmalek): this has to be moved to prevent circular imports
	operatorv1alpha1 "github.com/kong/gateway-operator/api/v1alpha1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.host`,description="Host of the service"
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.procol`,description="Protocol of the service"
// +kubebuilder:printcolumn:name="Programmed",description="The Resource is Programmed on Konnect",type=string,JSONPath=`.status.conditions[?(@.type=='Programmed')].status`
// +kubebuilder:printcolumn:name="ID",description="Konnect ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="OrgID",description="Konnect Organization ID this resource belongs to.",type=string,JSONPath=`.status.organizationID`

// Service is the schema for Services API which defines a Kong Service
type Service struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceSpec   `json:"spec"`
	Status ServiceStatus `json:"status,omitempty"`
}

func (c *Service) GetStatus() *operatorv1alpha1.KonnectEntityStatus {
	return &c.Status.KonnectEntityStatus
}

func (c Service) GetTypeName() string {
	return "Service"
}

func (c *Service) SetKonnectLabels(labels map[string]string) {
}

func (c *Service) GetKonnectAPIAuthConfigurationRef() operatorv1alpha1.KonnectAPIAuthConfigurationRef {
	return c.Spec.KonnectAPIAuthConfigurationRef
}

func (c *Service) GetReconciliationWatchOptions(
	cl client.Client,
) []func(*ctrl.Builder) *ctrl.Builder {
	return []func(*ctrl.Builder) *ctrl.Builder{}
}

// ServiceSpec defines specification of a Kong Service.
type ServiceSpec struct {
	ControlPlaneRef                operatorv1alpha1.ControlPlaneRef                `json:"controlPlaneRef,omitempty"`
	KonnectAPIAuthConfigurationRef operatorv1alpha1.KonnectAPIAuthConfigurationRef `json:"konnectAPIAuthConfigurationRef,omitempty"`

	// TODO(pmalek): client certificate implement ref
	// TODO(pmalek): field below are copy pasted from sdkkonnectgocomp.CreateService
	// The reason for this is that Service creation request contains a Konnect ID
	// reference to a client certificate. This is not what we want to expose to the user.
	// Instead we want to expose a namespaced reference to a client certificate.
	//
	// sdkkonnectgocomp.CreateService `json:",inline"`

	// Helper field to set `protocol`, `host`, `port` and `path` using a URL. This field is write-only and is not returned in responses.
	URL *string `json:"url,omitempty"`
	// Array of `CA Certificate` object UUIDs that are used to build the trust store while verifying upstream server's TLS certificate. If set to `null` when Nginx default is respected. If default CA list in Nginx are not specified and TLS verification is enabled, then handshake with upstream server will always fail (because no CA are trusted).
	CaCertificates []string `json:"ca_certificates,omitempty"`

	// TODO(pmalek): implement ref
	// Certificate to be used as client certificate while TLS handshaking to the upstream server.
	// ClientCertificate *ClientCertificate `json:"client_certificate,omitempty"`

	// The timeout in milliseconds for establishing a connection to the upstream server.
	ConnectTimeout *int64 `default:"60000" json:"connect_timeout"`
	// Whether the Service is active. If set to `false`, the proxy behavior will be as if any routes attached to it do not exist (404). Default: `true`.
	Enabled *bool `default:"true" json:"enabled"`
	// The host of the upstream server. Note that the host value is case sensitive.
	// +kubebuilder:validation:Required
	Host string `json:"host"`
	// The Service name.
	Name *string `json:"name,omitempty"`
	// The path to be used in requests to the upstream server.
	Path *string `json:"path,omitempty"`
	// The upstream server port.
	Port *int64 `default:"80" json:"port"`
	// The protocol used to communicate with the upstream.
	Protocol *sdkkonnectgocomp.Protocol `default:"http" json:"protocol"`
	// The timeout in milliseconds between two successive read operations for transmitting a request to the upstream server.
	ReadTimeout *int64 `default:"60000" json:"read_timeout"`
	// The number of retries to execute upon failure to proxy.
	Retries *int64 `default:"5" json:"retries"`
	// An optional set of strings associated with the Service for grouping and filtering.
	Tags []string `json:"tags,omitempty"`
	// Whether to enable verification of upstream server TLS certificate. If set to `null`, then the Nginx default is respected.
	TLSVerify *bool `json:"tls_verify,omitempty"`
	// Maximum depth of chain while verifying Upstream server's TLS certificate. If set to `null`, then the Nginx default is respected.
	TLSVerifyDepth *int64 `json:"tls_verify_depth,omitempty"`
	// The timeout in milliseconds between two successive write operations for transmitting a request to the upstream server.
	WriteTimeout *int64 `default:"60000" json:"write_timeout"`
}

// ServiceStatus represents the current status of the Service resource.
type ServiceStatus struct {
	operatorv1alpha1.KonnectEntityStatus `json:",inline"`
	ControlPlaneID                       string `json:"controlPlaneID,omitempty"`
}

// +kubebuilder:object:root=true

// ServiceList contains a list of Service.
type ServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Service `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Service{}, &ServiceList{})
}
