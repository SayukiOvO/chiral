package threexui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

const openAPIEndpoint = "panel/api/openapi.json"

// Capability is one 3x-ui operation Chiral needs from the node-local panel.
type Capability string

const (
	CapabilityStatus         Capability = "status"
	CapabilityInboundsList   Capability = "inbounds_list"
	CapabilityInboundsAdd    Capability = "inbounds_add"
	CapabilityInboundsUpdate Capability = "inbounds_update"
	CapabilityInboundsDelete Capability = "inbounds_delete"
	CapabilityClientsList    Capability = "clients_list"
	CapabilityClientsAdd     Capability = "clients_add"
	CapabilityClientsUpdate  Capability = "clients_update"
	CapabilityClientsDelete  Capability = "clients_delete"
	CapabilityClientTraffic  Capability = "client_traffic"
	CapabilityClientsOnline  Capability = "clients_online"
	CapabilityClientIPs      Capability = "client_ips"
	CapabilityXrayUpdate     Capability = "xray_update"
	CapabilityXrayRestart    Capability = "xray_restart"
	CapabilityGetConfig      Capability = "get_config"
	CapabilityInstallXray    Capability = "install_xray"
)

type operation struct {
	method string
	path   string
}

var requiredOperations = []struct {
	capability Capability
	operation  operation
}{
	{CapabilityStatus, operation{method: "get", path: "/panel/api/server/status"}},
	{CapabilityInboundsList, operation{method: "get", path: "/panel/api/inbounds/list"}},
	{CapabilityInboundsAdd, operation{method: "post", path: "/panel/api/inbounds/add"}},
	{CapabilityInboundsUpdate, operation{method: "post", path: "/panel/api/inbounds/update/{id}"}},
	{CapabilityInboundsDelete, operation{method: "post", path: "/panel/api/inbounds/del/{id}"}},
	{CapabilityClientsList, operation{method: "get", path: "/panel/api/clients/list"}},
	{CapabilityClientsAdd, operation{method: "post", path: "/panel/api/clients/add"}},
	{CapabilityClientsUpdate, operation{method: "post", path: "/panel/api/clients/update/{email}"}},
	{CapabilityClientsDelete, operation{method: "post", path: "/panel/api/clients/del/{email}"}},
	{CapabilityClientTraffic, operation{method: "get", path: "/panel/api/clients/traffic/{email}"}},
	{CapabilityClientsOnline, operation{method: "post", path: "/panel/api/clients/onlines"}},
	{CapabilityClientIPs, operation{method: "post", path: "/panel/api/clients/ips/{email}"}},
	{CapabilityXrayUpdate, operation{method: "post", path: "/panel/api/xray/update"}},
	{CapabilityXrayRestart, operation{method: "post", path: "/panel/api/server/restartXrayService"}},
	{CapabilityGetConfig, operation{method: "get", path: "/panel/api/server/getConfigJson"}},
	{CapabilityInstallXray, operation{method: "post", path: "/panel/api/server/installXray/{version}"}},
}

// Capabilities is a snapshot derived from the panel's own OpenAPI document.
// Callers should rediscover it after a 3x-ui upgrade instead of assuming that a
// version string implies a particular route layout.
type Capabilities struct {
	OpenAPIVersion string
	// DocumentSHA256 is the lowercase SHA-256 of the exact OpenAPI response
	// bytes. It lets Core distinguish contract changes even when the panel's
	// advertised version stays the same.
	DocumentSHA256 string
	supported      map[Capability]bool
}

// Supports reports whether the discovered document exposes the required HTTP
// method and path for capability.
func (c Capabilities) Supports(capability Capability) bool {
	return c.supported[capability]
}

// Missing returns required Chiral capabilities absent from the document in a
// stable order.
func (c Capabilities) Missing() []Capability {
	missing := make([]Capability, 0)
	for _, required := range requiredOperations {
		if !c.Supports(required.capability) {
			missing = append(missing, required.capability)
		}
	}
	return missing
}

// RequiredCapabilities returns the capability set this adapter understands.
func RequiredCapabilities() []Capability {
	result := make([]Capability, 0, len(requiredOperations))
	for _, required := range requiredOperations {
		result = append(result, required.capability)
	}
	return result
}

// DiscoverCapabilities reads the authenticated OpenAPI document served by
// 3x-ui and matches exact methods and paths. It does not probe mutating routes.
func (c *Client) DiscoverCapabilities(ctx context.Context) (Capabilities, error) {
	body, err := c.get(ctx, openAPIEndpoint)
	if err != nil {
		return Capabilities{}, err
	}
	var document struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return Capabilities{}, &ContractError{Message: c.safeDiagnostic("decoding the 3x-ui OpenAPI document: " + err.Error())}
	}
	if !strings.HasPrefix(document.OpenAPI, "3.") {
		return Capabilities{}, &ContractError{Message: "3x-ui returned an unsupported OpenAPI version"}
	}
	if document.Paths == nil {
		return Capabilities{}, &ContractError{Message: "3x-ui OpenAPI document has no paths"}
	}
	digest := sha256.Sum256(body)

	result := Capabilities{
		OpenAPIVersion: document.OpenAPI,
		DocumentSHA256: fmt.Sprintf("%x", digest),
		supported:      make(map[Capability]bool, len(requiredOperations)),
	}
	for _, required := range requiredOperations {
		methods := document.Paths[required.operation.path]
		operationBody, ok := methods[required.operation.method]
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(string(operationBody))
		// An OpenAPI operation is an object. Mere key presence with null or a
		// scalar is a malformed contract, not evidence that the route exists.
		if strings.HasPrefix(trimmed, "{") {
			result.supported[required.capability] = true
		}
	}
	return result, nil
}
