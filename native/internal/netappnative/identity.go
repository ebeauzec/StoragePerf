package netappnative

import (
	"encoding/json"
	"fmt"

	"plumb/internal/config"
)

// NodeIdentity is one ONTAP node's stable hardware identity.
type NodeIdentity struct {
	Name   string `json:"name"`
	Serial string `json:"serial_number"`
	Model  string `json:"model"`
}

// Identity is what an ONTAP cluster calls itself: enough for another tool
// (NetApp Active IQ and ARIA, which know systems by serial number and
// cluster name, not by whatever display name someone typed into
// config/arrays.yml) to match a monitored array to its own record exactly
// instead of by fuzzy name.
type Identity struct {
	ClusterName string         `json:"cluster_name"`
	ClusterUUID string         `json:"cluster_uuid"`
	Version     string         `json:"version"`
	Nodes       []NodeIdentity `json:"nodes"`
}

type clusterIdentityResp struct {
	Name    string `json:"name"`
	UUID    string `json:"uuid"`
	Version struct {
		Full string `json:"full"`
	} `json:"version"`
}

type nodeIdentityResp struct {
	Records []struct {
		Name         string `json:"name"`
		SerialNumber string `json:"serial_number"`
		Model        string `json:"model"`
	} `json:"records"`
}

// Identity reads the cluster name/UUID/version and every node's serial number
// and model over the same REST connection the metrics collector uses. Best
// effort by design: a caller (the ARIA export) treats an error as "identity
// unknown" and falls back to name matching, so a failure here must never
// block anything else.
func (c *ONTAPCollector) Identity(arr config.Array) (Identity, error) {
	client := c.client(arr)
	var id Identity

	body, err := c.get(client, arr, "/api/cluster?fields=name,uuid,version.full")
	if err != nil {
		return id, err
	}
	var cl clusterIdentityResp
	if err := json.Unmarshal(body, &cl); err != nil {
		return id, fmt.Errorf("parsing /api/cluster: %w", err)
	}
	id.ClusterName, id.ClusterUUID, id.Version = cl.Name, cl.UUID, cl.Version.Full

	body, err = c.get(client, arr, "/api/cluster/nodes?fields=name,serial_number,model")
	if err != nil {
		return id, err
	}
	var nr nodeIdentityResp
	if err := json.Unmarshal(body, &nr); err != nil {
		return id, fmt.Errorf("parsing /api/cluster/nodes: %w", err)
	}
	for _, n := range nr.Records {
		id.Nodes = append(id.Nodes, NodeIdentity{Name: n.Name, Serial: n.SerialNumber, Model: n.Model})
	}
	return id, nil
}
