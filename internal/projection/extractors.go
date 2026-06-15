package projection

// MachineSpec is the projected spec for the "Machine" Kind. Extracted from a TF
// resource instance's attributes — the fields common to the cloud compute
// resources (oci_core_instance, openstack_compute_instance_v2) and the metal
// apply resource (talos_machine_configuration_apply).
//
// Only spec-plane fields live here. Status fields (last-applied, health,
// observed generation) are ephemeral and filled by the Collector (Phase 4
// sibling issue), NEVER by the projection — projection is spec-only.
type MachineSpec struct {
	// ProviderID is the cloud provider's resource ID. For OCI this is the OCID;
	// for OpenStack the instance UUID; for metal the node IP (the talos
	// provider's address). Absent for metal apply resources.
	ProviderID string `json:"providerId,omitempty"`
	// Hostname is the instance display name / hostname. For OCI
	// display_name; OpenStack name; metal the talos config hostname.
	Hostname string `json:"hostname,omitempty"`
	// Address is the primary network address (IP). OCI: the primary VNIC IP;
	// OpenStack: the access IPv4/v6; metal: the node target IP.
	Address string `json:"address,omitempty"`
	// Shape is the instance shape/flavor. OCI: shape; OpenStack: flavor_name;
	// metal: absent (bare metal has no flavor).
	Shape string `json:"shape,omitempty"`
	// Region/zone where present (cloud only).
	Region string `json:"region,omitempty"`
}

// extractMachine pulls Machine spec fields from a TF instance's attributes,
// handling the three provider shapes (oci / openstack / metal). The tfType arg
// selects which fields to read — each provider writes a different schema.
func extractMachine(tfType string, attrs map[string]interface{}) map[string]interface{} {
	if attrs == nil {
		return nil
	}
	spec := MachineSpec{}
	switch tfType {
	case "oci_core_instance":
		spec.ProviderID = stringAttr(attrs, "id")
		spec.Hostname = stringAttr(attrs, "display_name")
		spec.Shape = stringAttr(attrs, "shape")
		spec.Region = stringAttr(attrs, "region")
		// Primary VNIC IP: OCI exposes the first primary_vnic's public_ip, or
		// the instance's primary public IP at the top level when available.
		spec.Address = firstNonEmpty(
			stringAttr(attrs, "public_ip"),
			stringAttr(attrs, "primary_public_ip"),
			nestedString(attrs, "create_vnic_details", "assign_public_ip"), // bool, not IP — last-resort placeholder
		)
	case "openstack_compute_instance_v2":
		spec.ProviderID = stringAttr(attrs, "id")
		spec.Hostname = stringAttr(attrs, "name")
		spec.Shape = stringAttr(attrs, "flavor_name")
		spec.Address = firstNonEmpty(
			stringAttr(attrs, "access_ip_v4"),
			stringAttr(attrs, "access_ip_v6"),
			networkV4(attrs),
		)
	case "talos_machine_configuration_apply":
		// Metal: no provider ID, no shape. The node IP is the `node` arg.
		spec.Address = stringAttr(attrs, "node")
		spec.Hostname = stringAttr(attrs, "hostname")
	default:
		// Unknown compute type — return the raw attributes so the API renders
		// *something* rather than hiding the instance.
		return attrs
	}
	return machineSpecToMap(spec)
}

// --- attr helpers ---

func stringAttr(attrs map[string]interface{}, key string) string {
	v, ok := attrs[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return formatFloat(x)
	default:
		b, _ := jsonMarshal(v)
		return string(b)
	}
}

// nestedString reads attrs[a][b] as a string.
func nestedString(attrs map[string]interface{}, a, b string) string {
	inner, ok := attrs[a].(map[string]interface{})
	if !ok {
		return ""
	}
	return stringAttr(inner, b)
}

// networkV4 reads the first network block's fixed_ip_v4 (OpenStack shape).
func networkV4(attrs map[string]interface{}) string {
	nets, ok := attrs["network"].([]interface{})
	if !ok || len(nets) == 0 {
		return ""
	}
	first, ok := nets[0].(map[string]interface{})
	if !ok {
		return ""
	}
	return stringAttr(first, "fixed_ip_v4")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// machineSpecToMap serializes a MachineSpec to a generic map (the Resource.Spec
// shape). Uses JSON round-trip for stable, camelCase keys matching the struct
// json tags.
func machineSpecToMap(spec MachineSpec) map[string]interface{} {
	if spec == (MachineSpec{}) {
		return nil
	}
	b, err := jsonMarshal(spec)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if err := jsonUnmarshal(b, &m); err != nil {
		return nil
	}
	// Drop empty fields so the API doesn't render "" for cloud-only fields on
	// metal resources (and vice versa).
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}
