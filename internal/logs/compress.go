package logs

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	reTimestamp = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?\b`)
	reUUID      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reHex       = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b|\b[0-9a-fA-F]{16,}\b`)
	reNum       = regexp.MustCompile(`\b\d+\b`)
)

// templatize collapses volatile tokens (timestamps, ids, numbers) so repeated
// log lines that differ only in those fields share one template.
func templatize(msg string) string {
	t := reTimestamp.ReplaceAllString(msg, "<ts>")
	t = reUUID.ReplaceAllString(t, "<uuid>")
	t = reHex.ReplaceAllString(t, "<hex>")
	t = reNum.ReplaceAllString(t, "<n>")
	t = strings.Join(strings.Fields(t), " ")
	if len(t) > 200 {
		t = t[:200]
	}
	return t
}

// Cluster is a group of near-identical log lines.
type Cluster struct {
	Template  string
	Level     string
	Count     int
	FirstTs   string
	LastTs    string
	SampleRef string
	Sample    string
}

// Cluster groups entries by templated message, preserving per-cluster counts,
// time span, and one citable sample. Clusters are ordered by severity then
// frequency so the most important patterns survive truncation.
func ClusterEntries(entries []Entry) []Cluster {
	idx := map[string]*Cluster{}
	order := []string{}
	for _, e := range entries {
		key := e.Level + "|" + templatize(e.Message)
		cl, ok := idx[key]
		if !ok {
			cl = &Cluster{
				Template:  templatize(e.Message),
				Level:     e.Level,
				FirstTs:   e.Ts,
				LastTs:    e.Ts,
				SampleRef: e.Ref,
				Sample:    e.Message,
			}
			idx[key] = cl
			order = append(order, key)
		}
		cl.Count++
		if e.Ts < cl.FirstTs {
			cl.FirstTs = e.Ts
		}
		if e.Ts > cl.LastTs {
			cl.LastTs = e.Ts
		}
	}
	out := make([]Cluster, 0, len(order))
	for _, k := range order {
		out = append(out, *idx[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := levelRank[out[i].Level], levelRank[out[j].Level]
		if ri != rj {
			return ri > rj // errors first
		}
		return out[i].Count > out[j].Count
	})
	return out
}

// BuildChatContext produces a compact, citable text block for the LLM from a
// (possibly huge) set of entries. It dedups into clusters and caps the output
// at maxClusters, keeping the highest-severity / most-frequent patterns. Each
// line carries its [ref] so the model can cite real log lines. Returns the
// block and whether the input was truncated.
func BuildChatContext(entries []Entry, maxClusters int) (string, bool) {
	if maxClusters <= 0 {
		maxClusters = 60
	}
	clusters := ClusterEntries(entries)
	truncated := len(clusters) > maxClusters
	if truncated {
		clusters = clusters[:maxClusters]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d log lines summarized into %d distinct patterns", len(entries), len(clusters))
	if truncated {
		b.WriteString(" (showing the top patterns by severity/frequency)")
	}
	b.WriteString(":\n")
	for _, cl := range clusters {
		span := cl.FirstTs
		if cl.LastTs != cl.FirstTs {
			span = cl.FirstTs + "→" + cl.LastTs
		}
		fmt.Fprintf(&b, "- [%s] x%d %s [ref:%s] %s\n",
			strings.ToUpper(cl.Level), cl.Count, span, cl.SampleRef, truncate(cl.Sample, 240))
	}
	return b.String(), truncated
}

// ErrorPatterns counts common failure keywords across entries (cheap signal for
// the chat agent's root-cause hints).
func ErrorPatterns(entries []Entry) map[string]int {
	p := map[string]int{"errors": 0, "timeouts": 0, "connection_failures": 0, "total": len(entries)}
	for _, e := range entries {
		low := strings.ToLower(e.Message)
		if e.Level == LevelError || e.Level == LevelFatal || strings.Contains(low, "error") {
			p["errors"]++
		}
		if strings.Contains(low, "timeout") || strings.Contains(low, "timed out") {
			p["timeouts"]++
		}
		if strings.Contains(low, "connection") && (strings.Contains(low, "refused") || strings.Contains(low, "failed") || strings.Contains(low, "reset")) {
			p["connection_failures"]++
		}
	}
	return p
}
