package netappnative

import (
	"net/http/httptest"
	"testing"
)

func TestONTAP_Identity(t *testing.T) {
	srv := httptest.NewTLSServer(pathRouter(map[string]string{
		"/api/cluster":       `{"name":"prod-cl01","uuid":"1cd8a442-86d1-11e0-ae1c-123478563412","version":{"full":"NetApp Release 9.15.1P7"}}`,
		"/api/cluster/nodes": `{"records":[{"name":"prod-cl01-01","serial_number":"721234000101","model":"AFF-A400"},{"name":"prod-cl01-02","serial_number":"721234000102","model":"AFF-A400"}]}`,
	}))
	defer srv.Close()

	id, err := NewONTAPCollector().Identity(newTestArray(t, srv))
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if id.ClusterName != "prod-cl01" || id.Version != "NetApp Release 9.15.1P7" {
		t.Errorf("cluster identity wrong: %+v", id)
	}
	if len(id.Nodes) != 2 || id.Nodes[1].Serial != "721234000102" || id.Nodes[0].Model != "AFF-A400" {
		t.Errorf("node identity wrong: %+v", id.Nodes)
	}
}

func TestONTAP_Identity_ClusterUnreachable(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	arr := newTestArray(t, srv)
	srv.Close() // nothing listening any more
	if _, err := NewONTAPCollector().Identity(arr); err == nil {
		t.Error("expected an error when the cluster can't be reached")
	}
}
