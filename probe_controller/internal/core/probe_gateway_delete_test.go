package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeletedGatewayGrantIsWithdrawnAndConsumersNotified(t *testing.T) {
	for _, mode := range []string{"delete", "sync"} {
		t.Run(mode, func(t *testing.T) {
			oldStore, oldRoutes, oldScheduler := ProbeStore, ProbeRouteConfigStore, automaticProbeRouteConfigSyncScheduler
			ProbeStore = &probeConfigStore{path: filepath.Join(t.TempDir(), "probe.json"), data: probeConfigData{
				ProbeNodes: []probeNodeRecord{{NodeNo: 9, NodeName: "source"}, {NodeNo: 23, NodeName: "old gateway"}, {NodeNo: 26, NodeName: "current gateway"}},
			}}
			ProbeRouteConfigStore = nil
			notified, release := make(chan struct{}, 1), make(chan struct{})
			scheduler := newProbeRouteConfigSyncScheduler(func() { notified <- struct{}{}; <-release })
			automaticProbeRouteConfigSyncScheduler = scheduler
			t.Cleanup(func() {
				close(release)
				waitProbeRouteConfigSyncSchedulerIdle(t, scheduler)
				ProbeStore, ProbeRouteConfigStore, automaticProbeRouteConfigSyncScheduler = oldStore, oldRoutes, oldScheduler
			})
			grant := &probeGatewayProxyConfig{Enabled: true, ProxyCIDRs: []string{"192.168.50.0/24"}, AllowedNodeIDs: []string{"9"}}
			runtimes := []probeRuntimeStatus{{NodeID: "23", Online: true, GatewayProxy: grant}, {NodeID: "26", Online: false, GatewayProxy: grant}}
			before := appendProbeGatewayPublishedRules(nil, runtimes, "9")
			if len(before) != 2 {
				t.Fatal("missing initial grants", before)
			}
			recorder := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if mode == "delete" {
					mngProbeNodeDeleteHandler(recorder, httptest.NewRequest(http.MethodPost, "/mng/api/probe/nodes/delete", strings.NewReader(`{"node_no":23}`)))
				} else {
					AdminSyncProbeNodesHandler(recorder, httptest.NewRequest(http.MethodPost, "/admin/probes", strings.NewReader(`{"nodes":[{"node_no":9,"node_name":"source"},{"node_no":26,"node_name":"current gateway"}]}`)))
				}
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("deletion waited for consumer delivery")
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", recorder.Code, recorder.Body.String())
			}
			select {
			case <-notified:
			case <-time.After(2 * time.Second):
				t.Fatal("consumers were not notified")
			}
			// Deliberately retain the stale runtime and the previous derived rules:
			// tombstones must prevent resurrection, even before runtime cleanup.
			assertRoutes := func() {
				t.Helper()
				routes := appendProbeGatewayPublishedRules(before, runtimes, "9")
				if len(routes) != 1 || routes[0].ExitNodeID != "26" {
					t.Fatalf("deleted gateway survived or offline grant revoked: %+v", routes)
				}
			}
			assertRoutes()
			data, err := os.ReadFile(ProbeStore.path)
			if err != nil {
				t.Fatal(err)
			}
			var restored probeConfigData
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			ProbeStore = &probeConfigStore{data: restored}
			assertRoutes()
		})
	}
}
