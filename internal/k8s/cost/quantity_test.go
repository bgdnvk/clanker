package cost

import "testing"

func TestParseCPUQuantity(t *testing.T) {
	cases := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{"", 0, false},
		{"1", 1, false},
		{"2.5", 2.5, false},
		{"100m", 0.1, false},
		{"1500m", 1.5, false},
		{"500M", 0, true}, // ambiguous, not valid CPU
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseCPUQuantity(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseCPUQuantity(%q) want error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCPUQuantity(%q) error = %v", c.in, err)
		}
		if !approxEqual(got, c.want, 1e-9) {
			t.Errorf("parseCPUQuantity(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseMemoryQuantity(t *testing.T) {
	cases := []struct {
		in      string
		want    float64 // MiB
		wantErr bool
	}{
		{"", 0, false},
		{"512Mi", 512, false},
		{"1Gi", 1024, false},
		{"2Gi", 2048, false},
		{"1024Ki", 1, false},
		{"16384Mi", 16384, false},
		{"1G", 1000.0 * 1000.0 * 1000.0 / (1024.0 * 1024.0), false},
		{"1k", 1000.0 / (1024.0 * 1024.0), false},
		// Plain bytes.
		{"1048576", 1, false},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseMemoryQuantity(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseMemoryQuantity(%q) want error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMemoryQuantity(%q) error = %v", c.in, err)
		}
		if !approxEqual(got, c.want, 1e-6) {
			t.Errorf("parseMemoryQuantity(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPriceLookups(t *testing.T) {
	def := DefaultAWSOnDemandPrices()
	if p, ok := def(NodeInfo{InstanceType: "m5.xlarge", Provider: "aws"}); !ok || !approxEqual(p, 0.192, 1e-9) {
		t.Errorf("default m5.xlarge AWS = (%v, %v), want (0.192, true)", p, ok)
	}
	if _, ok := def(NodeInfo{InstanceType: "made-up.huge", Provider: "aws"}); ok {
		t.Error("default lookup should miss on unknown instance type")
	}
	if _, ok := def(NodeInfo{InstanceType: "m5.xlarge", Provider: "gcp"}); ok {
		t.Error("default lookup should not match non-aws providers")
	}

	custom := MapPriceLookup(map[string]float64{"my.kind": 1.50})
	if p, ok := custom(NodeInfo{InstanceType: "my.kind"}); !ok || p != 1.50 {
		t.Errorf("custom my.kind = (%v, %v), want (1.50, true)", p, ok)
	}

	// Composite: custom hit beats default fallback.
	composite := CompositePriceLookup(custom, def)
	if p, ok := composite(NodeInfo{InstanceType: "my.kind"}); !ok || p != 1.50 {
		t.Errorf("composite custom hit = (%v, %v), want (1.50, true)", p, ok)
	}
	// Composite: custom miss falls through to default.
	if p, ok := composite(NodeInfo{InstanceType: "m5.xlarge", Provider: "aws"}); !ok || !approxEqual(p, 0.192, 1e-9) {
		t.Errorf("composite default fallback = (%v, %v), want (0.192, true)", p, ok)
	}
}
