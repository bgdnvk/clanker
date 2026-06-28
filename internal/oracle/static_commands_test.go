package oracle

import "testing"

func TestLookupResourceDefinitionAliases(t *testing.T) {
	def, ok := lookupResourceDefinition("oke")
	if !ok {
		t.Fatal("expected OKE alias to resolve")
	}
	if def.Name != "oke-clusters" {
		t.Fatalf("alias resolved to %q, want oke-clusters", def.Name)
	}

	bucket, ok := lookupResourceDefinition("object-storage")
	if !ok {
		t.Fatal("expected object-storage alias to resolve")
	}
	if !bucket.NeedsNamespace {
		t.Fatal("object storage buckets must require namespace lookup")
	}
}

func TestResourcesForQuestion(t *testing.T) {
	got := resourcesForQuestion("show oracle oke clusters and object storage buckets")
	want := map[string]bool{"oke-clusters": true, "buckets": true}
	for _, name := range got {
		delete(want, name)
	}
	if len(want) > 0 {
		t.Fatalf("missing resource matches: %+v in %v", want, got)
	}
}
