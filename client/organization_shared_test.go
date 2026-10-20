package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The space-scoped endpoints embed the resource (knowledge_base / agent), which
// is what makes the share addressable as a --kb / --agent value. If the SDK
// drops that field the CLI silently lists id-less rows, so pin the decode here.
func TestListOrganizationSharedKnowledgeBases_DecodesEmbeddedKB(t *testing.T) {
	const body = `{"success":true,"total":2,"data":[
		{"knowledge_base":{"id":"kb-1","name":"UVDoc 产品手册","knowledge_count":12,"chunk_count":340},
		 "share_id":"sh-1","organization_id":"org-1","org_name":"UVDoc",
		 "permission":"viewer","source_tenant_id":7,"is_mine":false},
		{"knowledge_base":{"id":"kb-2","name":"本空间共享出去的"},
		 "share_id":"sh-2","organization_id":"org-1","org_name":"UVDoc",
		 "permission":"editor","source_tenant_id":42,"is_mine":true}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/organizations/org-1/shared-knowledge-bases" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	shares, err := NewClient(srv.URL).ListOrganizationSharedKnowledgeBases(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("ListOrganizationSharedKnowledgeBases: %v", err)
	}
	if len(shares) != 2 {
		t.Fatalf("want 2 shares, got %d", len(shares))
	}
	first := shares[0]
	if first.KnowledgeBase == nil {
		t.Fatal("embedded knowledge_base was dropped")
	}
	if first.KnowledgeBase.ID != "kb-1" || first.KnowledgeBase.Name != "UVDoc 产品手册" {
		t.Errorf("kb = %+v, want kb-1 / UVDoc 产品手册", first.KnowledgeBase)
	}
	if first.Permission != "viewer" || first.OrgName != "UVDoc" || first.IsMine {
		t.Errorf("share metadata = %+v", first)
	}
	if second := shares[1]; !second.IsMine {
		t.Error("is_mine must survive decoding so the CLI can mark own shares")
	}
}

func TestListOrganizationSharedAgents_DecodesEmbeddedAgent(t *testing.T) {
	const body = `{"success":true,"total":1,"data":[
		{"agent":{"id":"ag-1","name":"UVDoc 问答助手","is_builtin":false},
		 "share_id":"sh-9","organization_id":"org-1","org_name":"UVDoc",
		 "permission":"viewer","source_tenant_id":7,"is_mine":false}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/organizations/org-1/shared-agents" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	agents, err := NewClient(srv.URL).ListOrganizationSharedAgents(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("ListOrganizationSharedAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Agent == nil {
		t.Fatalf("want one share with an embedded agent, got %+v", agents)
	}
	if agents[0].Agent.ID != "ag-1" || agents[0].Agent.Name != "UVDoc 问答助手" {
		t.Errorf("agent = %+v", agents[0].Agent)
	}
}
