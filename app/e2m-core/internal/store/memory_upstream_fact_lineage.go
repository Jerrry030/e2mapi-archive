package store

// memoryQualityOnlyFactAdvanceProof returns an owner-scoped copy of the exact
// mutation interval requested by rollout revalidation. Complete means the
// MemoryStore can account for every version in (baseline,current]; callers
// still validate owner, ordering, kind and evidence identities independently.
//
// A baseline older than the watermark predates managed lineage and therefore
// fails closed. Likewise any untracked version jump, duplicate or reordered
// row makes the interval incomplete; this helper never fabricates repair rows.
func (s *MemoryStore) memoryQualityOnlyFactAdvanceProof(userID, baseline, current int64) QualityOnlyFactAdvanceProof {
	proof := QualityOnlyFactAdvanceProof{
		UserID:              userID,
		BaselineFactVersion: baseline,
		CurrentFactVersion:  current,
		LineageWatermark:    s.upstreamIntelLineageWatermarks[userID],
	}
	if userID <= 0 || baseline < proof.LineageWatermark || current <= baseline {
		return proof
	}

	mutations := s.upstreamIntelFactMutations[userID]
	proof.Mutations = make([]UpstreamIntelligenceFactMutation, 0, current-baseline)
	expected := baseline + 1
	for _, mutation := range mutations {
		if mutation.FactVersion <= baseline {
			continue
		}
		if mutation.FactVersion > current || mutation.UserID != userID || mutation.FactVersion != expected {
			return proof
		}
		proof.Mutations = append(proof.Mutations, mutation)
		expected++
	}
	proof.Complete = expected == current+1 && int64(len(proof.Mutations)) == current-baseline
	return proof
}
