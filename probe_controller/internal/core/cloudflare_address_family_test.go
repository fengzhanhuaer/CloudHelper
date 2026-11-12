package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudflareAddressFamilyNames(t *testing.T) {
	old := ProbeStore
	t.Cleanup(func() { ProbeStore = old })
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: []probeNodeRecord{{NodeNo: 1, DDNS: "home"}}}}
	for _, ipv4 := range [][]string{{"8.8.8.8", "1.1.1.1", "192.168.1.1"}, nil} {
		desired := buildCloudflareDesiredRecords(probeNodeStatusRecord{NodeNo: 1, Runtime: probeRuntimeStatus{
			IPv4: ipv4, IPv6: []string{"2606:4700:4700::1111", "2606:4700:4700::1001", "fd00::1"},
		}}, "example.com")
		want6 := map[string]bool{"api6.codex.mq.example.com": false, "api6.codex.mq_2.example.com": false,
			"ddns6_go_home.example.com": false, "ddns6_go_home_2.example.com": false, "local6_go_home.example.com": false}
		want4 := map[string]bool{"api.codex.mq.example.com": false, "api.codex.mq_2.example.com": false,
			"ddns_go_home.example.com": false, "ddns_go_home_2.example.com": false, "local_go_home.example.com": false}
		seen := map[string]string{}
		for _, d := range desired {
			if typ, ok := seen[d.RecordName]; ok && typ != d.RecordType {
				t.Fatalf("address families share a name: %+v", d)
			}
			seen[d.RecordName] = d.RecordType
			want := want4
			if d.RecordType == "AAAA" {
				want = want6
			}
			if _, ok := want[d.RecordName]; !ok {
				t.Fatalf("unexpected name: %+v", d)
			}
			want[d.RecordName] = true
			if !isCloudflareManagedDDNSRecordName(d.RecordName) || normalizeCloudflareRecordClass("", d.RecordName) != d.RecordClass {
				t.Fatalf("new name lost its managed class: %+v", d)
			}
		}
		for name, found := range want6 {
			if !found {
				t.Fatalf("missing IPv6 name %s", name)
			}
		}
		if len(ipv4) > 0 {
			for name, found := range want4 {
				if !found {
					t.Fatalf("missing IPv4 name %s", name)
				}
			}
		}
	}
	// Default tags and existing certificate identity remain stable.
	ProbeStore.data.ProbeNodes[0].DDNS = ""
	d := buildCloudflareDesiredRecords(probeNodeStatusRecord{NodeNo: 1, Runtime: probeRuntimeStatus{IPv6: []string{"2606:4700:4700::1111"}}}, "example.com")
	if len(d) != 2 || d[0].RecordName != "ddns6_go_mq.example.com" || buildCloudflareBusinessRecordBase(1) != "api.codex.mq" {
		t.Fatalf("default naming or certificate identity changed: %+v", d)
	}
}

func TestCloudflareAddressFamilyMigration(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, failure := range []string{"", "create", "delete"} {
			t.Run(fmt.Sprintf("automatic=%v/fail=%s", automatic, failure), func(t *testing.T) {
				oldProbe, oldCF, oldTransport := ProbeStore, CloudflareStore, http.DefaultTransport
				probeRuntimeStore.mu.Lock()
				oldRuntime := probeRuntimeStore.data
				probeRuntimeStore.data = map[string]probeRuntimeStatus{"1": {NodeID: "1", IPv4: []string{"8.8.8.8"}, IPv6: []string{"2606:4700:4700::1111"}}}
				probeRuntimeStore.mu.Unlock()
				t.Cleanup(func() {
					ProbeStore, CloudflareStore, http.DefaultTransport = oldProbe, oldCF, oldTransport
					probeRuntimeStore.mu.Lock()
					probeRuntimeStore.data = oldRuntime
					probeRuntimeStore.mu.Unlock()
				})
				tmp := t.TempDir()
				ProbeStore = &probeConfigStore{path: filepath.Join(tmp, "probe.json"), data: probeConfigData{
					ProbeNodes: []probeNodeRecord{{NodeNo: 1, NodeName: "one", CloudflareDDNSRecords: []cloudflareDDNSRecord{{
						ZoneName: "example.com", ZoneID: "zone", RecordClass: "business", RecordName: "api.codex.mq.example.com",
						RecordType: "AAAA", RecordID: "old", Sequence: 1, ContentIP: "2606:4700:4700::1111",
					}}}}, ProbeSecrets: map[string]string{},
				}}
				CloudflareStore = &cloudflareStore{path: filepath.Join(tmp, "cf.json"), data: cloudflareStoreData{APIToken: "test-only", ZoneName: "example.com"}}
				records := map[string]cloudflareDNSRecord{"old": {ID: "old", Type: "AAAA", Name: "api.codex.mq.example.com", Content: "2606:4700:4700::1111"}}
				events := []string{}
				nextID := 0
				fail := failure
				http.DefaultTransport = probeProxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Host != "api.cloudflare.com" {
						t.Fatalf("unexpected outbound request: %s", req.URL)
					}
					status := http.StatusOK
					var result interface{}
					path := req.URL.Path
					switch {
					case path == "/client/v4/zones":
						result = []map[string]string{{"id": "zone", "name": "example.com"}}
					case strings.HasSuffix(path, "/dns_records") && req.Method == http.MethodGet:
						rows := []cloudflareDNSRecord{}
						for _, r := range records {
							if name := req.URL.Query().Get("name"); name != "" && name != r.Name {
								continue
							}
							if typ := req.URL.Query().Get("type"); typ != "" && typ != r.Type {
								continue
							}
							rows = append(rows, r)
						}
						result = rows
					case strings.HasSuffix(path, "/dns_records") && req.Method == http.MethodPost:
						var r cloudflareDNSRecord
						if err := json.NewDecoder(req.Body).Decode(&r); err != nil {
							t.Fatal(err)
						}
						if fail == "create" && strings.HasPrefix(r.Name, "api6.") {
							status = 503
							break
						}
						nextID++
						r.ID = fmt.Sprintf("new-%d", nextID)
						records[r.ID] = r
						result = r
						events = append(events, "create:"+r.Name)
					case strings.Contains(path, "/dns_records/"):
						id := path[strings.LastIndex(path, "/")+1:]
						if req.Method == http.MethodDelete {
							if fail == "delete" && id == "old" {
								status = 503
								break
							}
							delete(records, id)
							events = append(events, "delete:"+id)
							result = map[string]string{"id": id}
						} else if req.Method == http.MethodGet {
							r, ok := records[id]
							if !ok {
								status = 404
							}
							result = r
						} else {
							t.Fatalf("unexpected method: %s %s", req.Method, path)
						}
					default:
						t.Fatalf("unexpected API: %s %s", req.Method, path)
					}
					body, _ := json.Marshal(map[string]interface{}{"success": status == 200, "result": result})
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
				})
				sync := func() bool {
					if automatic {
						return applyCloudflareAutoDDNSForNode("1") != nil
					}
					result, err := applyCloudflareDDNS(cloudflareDDNSApplyRequest{ZoneName: "example.com"})
					if err != nil {
						t.Fatal(err)
					}
					for _, r := range result.Items {
						if r.Status == "failed" {
							return true
						}
					}
					return false
				}
				if failed := sync(); failed != (failure != "") {
					t.Fatalf("failure=%v, expected %s", failed, failure)
				}
				if failure != "" {
					if _, ok := records["old"]; !ok {
						t.Fatal("failure deleted working AAAA")
					}
					found := false
					for _, r := range listCloudflareRecords() {
						if r.RecordID == "old" {
							found = true
						}
					}
					if !found {
						t.Fatal("failure discarded retry state")
					}
					fail = ""
					if sync() {
						t.Fatal("retry failed")
					}
				}
				if _, ok := records["old"]; ok {
					t.Fatal("legacy AAAA retained after successful migration")
				}
				created, deleted := -1, -1
				for i, event := range events {
					if event == "create:api6.codex.mq.example.com" {
						created = i
					}
					if event == "delete:old" {
						deleted = i
					}
				}
				if created < 0 || deleted <= created {
					t.Fatalf("unsafe migration order: %v", events)
				}
				for _, r := range listCloudflareRecords() {
					if r.RecordType == "AAAA" && !strings.Contains(r.RecordName, "6") {
						t.Fatalf("legacy record saved: %+v", r)
					}
				}
				numRecords := len(records)
				if sync() || len(records) != numRecords {
					t.Fatal("sync not idempotent")
				}
			})
		}
	}
}
