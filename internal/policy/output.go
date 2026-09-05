package policy

// LegacyOutputByteFactor is the pre-H03 bytes-per-token heuristic kept only
// for workloads without an explicit max_output_bytes bound. It must never be
// used to derive a byte limit when an explicit bound is present, and it must
// never be used to infer token counts from bytes.
const LegacyOutputByteFactor = 4

// EffectiveOutputBytes resolves the UTF-8 content byte limit for a workload:
// the explicit bound when the durable policy carries one, otherwise the exact
// legacy fallback. This is the single resolver shared by the worker and any
// future consumer of the bound, so the two semantics cannot drift apart.
func EffectiveOutputBytes(maxOutputTokens, maxOutputBytes int, explicit bool) int64 {
	if explicit {
		return int64(maxOutputBytes)
	}
	return int64(maxOutputTokens) * LegacyOutputByteFactor
}
