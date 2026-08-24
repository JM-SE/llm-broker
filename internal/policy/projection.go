package policy

// Workload is the canonical desired projection consumed by the provisioner.
// It contains only validated values; configuration decoding remains in
// internal/config.
type Workload struct {
	Name                     string
	PriorityClass            string
	MaxActiveTasks           int
	MaxAttempts              int
	RetryDelaysSeconds       []int
	MaxOutputTokens          int
	AllowedCapabilities      []string
	AllowedGeneration        []byte
	RequiredFeatures         []string
	WorkloadTimeoutSeconds   int
	PayloadRetentionSeconds  int64
	ResultRetentionSeconds   int64
	MetadataRetentionSeconds int64
	TopologyDigest           [32]byte
}

// SemanticallyEqual compares the policy fields which are effective in the
// durable row. Slice order is already canonicalized by the caller.
func (w Workload) SemanticallyEqual(other Workload) bool {
	return w.Name == other.Name &&
		w.PriorityClass == other.PriorityClass &&
		w.MaxActiveTasks == other.MaxActiveTasks &&
		w.MaxAttempts == other.MaxAttempts &&
		intsEqual(w.RetryDelaysSeconds, other.RetryDelaysSeconds) &&
		w.MaxOutputTokens == other.MaxOutputTokens &&
		stringsEqual(w.AllowedCapabilities, other.AllowedCapabilities) &&
		JSONSemanticallyEqual(w.AllowedGeneration, other.AllowedGeneration) &&
		stringsEqual(w.RequiredFeatures, other.RequiredFeatures) &&
		w.WorkloadTimeoutSeconds == other.WorkloadTimeoutSeconds &&
		w.PayloadRetentionSeconds == other.PayloadRetentionSeconds &&
		w.ResultRetentionSeconds == other.ResultRetentionSeconds &&
		w.MetadataRetentionSeconds == other.MetadataRetentionSeconds &&
		w.TopologyDigest == other.TopologyDigest
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
