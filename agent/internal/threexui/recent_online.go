package threexui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"sort"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

const (
	recentOnlineByGUIDEndpoint = "panel/api/clients/onlinesByGuid"
	recentIPsByGUIDEndpoint    = "panel/api/clients/clientIpsByGuid"
)

// ReadRecentOnline reads 3x-ui's recent online cache and stored recent source
// IPs for one verified local panel identity. The GUID-keyed endpoints are used
// deliberately: their response also contains descendant-node observations,
// which must not be attributed to this Agent's node.
//
// emails is the complete caller-authorized credential whitelist. Every input
// email gets one result, even when neither endpoint contains an observation;
// ObservedOnline=false means "not observed in this recent cache", not proof
// that the credential is offline. Provider rows outside the whitelist are
// never returned. The upstream IP endpoint has already applied its own freshness
// window. Timestamps are checked for wire-shape validity but are neither exposed,
// rewritten, nor independently filtered here, so this remains a recent/cached
// report rather than a current OnlineUsers snapshot.
func (c *Client) ReadRecentOnline(ctx context.Context, expectedGUID string, emails []string) ([]runtimeprovider.RecentOnlineUser, error) {
	requested, err := validateRecentOnlineRequest(c, expectedGUID, emails)
	if err != nil {
		return nil, err
	}

	capabilities, err := c.DiscoverCapabilities(ctx)
	if err != nil {
		return nil, recentOnlineFailure("capability_discovery_failed", err)
	}
	for _, capability := range []Capability{CapabilityStatus, CapabilityClientsOnlineByGUID, CapabilityClientIPsByGUID} {
		if !capabilities.Supports(capability) {
			return nil, recentOnlineFailure("capability_missing", nil)
		}
	}

	before, err := c.Status(ctx)
	if err != nil {
		return nil, recentOnlineFailure("identity_unavailable", err)
	}
	if before.PanelGUID != expectedGUID {
		return nil, recentOnlineFailure("provider_identity_changed", nil)
	}

	onlineObject, err := c.readRecentOnlineObject(ctx, recentOnlineByGUIDEndpoint)
	if err != nil {
		return nil, err
	}
	ipObject, err := c.readRecentOnlineObject(ctx, recentIPsByGUIDEndpoint)
	if err != nil {
		return nil, err
	}

	after, err := c.Status(ctx)
	if err != nil {
		return nil, recentOnlineFailure("identity_unavailable", err)
	}
	if after.PanelGUID != expectedGUID || after.PanelVersion != before.PanelVersion {
		return nil, recentOnlineFailure("provider_identity_changed", nil)
	}

	online, err := recentOnlineSet(onlineObject, expectedGUID)
	if err != nil {
		return nil, err
	}
	ips, err := recentIPSet(ipObject, expectedGUID, requested)
	if err != nil {
		return nil, err
	}

	result := make([]runtimeprovider.RecentOnlineUser, 0, len(emails))
	for _, email := range emails {
		userIPs := ips[email]
		if userIPs == nil {
			userIPs = []string{}
		}
		result = append(result, runtimeprovider.RecentOnlineUser{
			Email:          email,
			ObservedOnline: online[email],
			IPs:            userIPs,
		})
	}
	return result, nil
}

func validateRecentOnlineRequest(c *Client, expectedGUID string, emails []string) (map[string]bool, error) {
	if c == nil || c.base == nil || !validUpdateID(expectedGUID) || len(emails) > chiralv1.MaxRecentOnlineUsers {
		return nil, recentOnlineFailure("invalid_request", nil)
	}
	requested := make(map[string]bool, len(emails))
	for _, email := range emails {
		if len(email) > chiralv1.MaxRecentOnlineEmailBytes || validClientEmail(email) != nil || requested[email] {
			return nil, recentOnlineFailure("invalid_request", nil)
		}
		requested[email] = true
	}
	return requested, nil
}

func (c *Client) readRecentOnlineObject(ctx context.Context, endpoint string) (map[string]any, error) {
	u, err := c.endpointURL(endpoint)
	if err != nil {
		return nil, recentOnlineFailure("request_failed", err)
	}
	body, err := c.request(ctx, http.MethodPost, u, nil, "", true)
	if err != nil {
		return nil, recentOnlineFailure("request_failed", err)
	}
	response, err := decodeComparisonConfig(body, "")
	if err != nil {
		return nil, recentOnlineFailure("invalid_response", nil)
	}
	success, ok := response["success"].(bool)
	if !ok {
		return nil, recentOnlineFailure("invalid_response", nil)
	}
	if !success {
		return nil, recentOnlineFailure("provider_rejected_request", nil)
	}
	object, ok := response["obj"].(map[string]any)
	if !ok {
		return nil, recentOnlineFailure("invalid_response", nil)
	}
	return object, nil
}

func recentOnlineSet(object map[string]any, expectedGUID string) (map[string]bool, error) {
	result := make(map[string]bool)
	value, present := object[expectedGUID]
	if !present {
		return result, nil
	}
	emails, ok := value.([]any)
	if !ok || len(emails) > chiralv1.MaxRecentOnlineUsers {
		return nil, recentOnlineFailure("invalid_response", nil)
	}
	for _, value := range emails {
		email, ok := value.(string)
		if !ok || len(email) > chiralv1.MaxRecentOnlineEmailBytes || validClientEmail(email) != nil || result[email] {
			return nil, recentOnlineFailure("invalid_response", nil)
		}
		result[email] = true
	}
	return result, nil
}

func recentIPSet(object map[string]any, expectedGUID string, requested map[string]bool) (map[string][]string, error) {
	result := make(map[string][]string, len(requested))
	value, present := object[expectedGUID]
	if !present {
		return result, nil
	}
	perEmail, ok := value.(map[string]any)
	if !ok {
		return nil, recentOnlineFailure("invalid_response", nil)
	}
	total := 0
	for email := range requested {
		value, present := perEmail[email]
		if !present {
			continue
		}
		entries, ok := value.([]any)
		if !ok {
			return nil, recentOnlineFailure("invalid_response", nil)
		}
		seen := make(map[string]bool, len(entries))
		for _, value := range entries {
			entry, ok := value.(map[string]any)
			if !ok {
				return nil, recentOnlineFailure("invalid_response", nil)
			}
			ip, ok := entry["ip"].(string)
			timestamp, timestampOK := entry["timestamp"].(json.Number)
			_, timestampErr := timestamp.Int64()
			address, addressErr := netip.ParseAddr(ip)
			if !ok || ip == "" || addressErr != nil || address.Zone() != "" || !timestampOK || timestampErr != nil {
				return nil, recentOnlineFailure("invalid_response", nil)
			}
			total++
			if total > chiralv1.MaxRecentOnlineIPs {
				return nil, recentOnlineFailure("observation_limit_exceeded", nil)
			}
			canonical := address.Unmap().String()
			if seen[canonical] {
				continue
			}
			seen[canonical] = true
			result[email] = append(result[email], canonical)
		}
		sort.Strings(result[email])
	}
	return result, nil
}

func recentOnlineFailure(reason string, cause error) error {
	message := "3x-ui recent-online read failed: " + reason
	if cause != nil {
		return &safeWrappedError{message: message, cause: cause}
	}
	return &ContractError{Message: message}
}
