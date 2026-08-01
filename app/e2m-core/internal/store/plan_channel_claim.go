package store

import (
	"sort"

	"e2m.local/contracts"
)

type planChannelAllocationView struct {
	owners     map[string]int64
	userSource map[string]struct{}
}

// selectClaimablePlanChannels mirrors publish's permanent-key selection:
// retain the owner's active key for a source, otherwise choose the best active
// unallocated key. A source already owned elsewhere is never assigned again.
func selectClaimablePlanChannels(channels []contracts.UpstreamChannel, maxChannels int, userID int64, allocations planChannelAllocationView) []contracts.UpstreamChannel {
	active := make([]contracts.UpstreamChannel, 0, len(channels))
	for _, channel := range channels {
		if channel.Status != contracts.UpstreamChannelActive {
			continue
		}
		// Permanent owner allocations remain usable when supply admission later
		// changes (for example quarantine while an incident is investigated).
		// Only a new allocation requires ready platform-managed stock.
		if allocations.owners[channel.ID] == userID ||
			(channel.IsInventoryReady() && channel.AccountOwnership.Normalize() == contracts.GatewayAccountPlatformManaged) {
			active = append(active, channel)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		left, right := effectiveChannelPriority(active[i].Priority), effectiveChannelPriority(active[j].Priority)
		if left != right {
			return left < right
		}
		if active[i].Weight != active[j].Weight {
			return active[i].Weight > active[j].Weight
		}
		return active[i].ID < active[j].ID
	})

	selected := make([]contracts.UpstreamChannel, 0, len(active))
	seenSources := make(map[string]struct{}, len(active))
	for _, channel := range active {
		if allocations.owners[channel.ID] != userID {
			continue
		}
		sourceID := channel.SourceIdentity()
		if _, exists := seenSources[sourceID]; exists {
			continue
		}
		seenSources[sourceID] = struct{}{}
		selected = append(selected, channel)
	}
	for _, channel := range active {
		if _, allocated := allocations.owners[channel.ID]; allocated {
			continue
		}
		sourceID := channel.SourceIdentity()
		if _, owned := allocations.userSource[sourceID]; owned {
			continue
		}
		if _, exists := seenSources[sourceID]; exists {
			continue
		}
		seenSources[sourceID] = struct{}{}
		selected = append(selected, channel)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		left, right := effectiveChannelPriority(selected[i].Priority), effectiveChannelPriority(selected[j].Priority)
		if left != right {
			return left < right
		}
		if selected[i].Weight != selected[j].Weight {
			return selected[i].Weight > selected[j].Weight
		}
		return selected[i].ID < selected[j].ID
	})
	if maxChannels > 0 && len(selected) > maxChannels {
		selected = selected[:maxChannels]
	}
	return selected
}

func effectiveChannelPriority(priority int) int {
	if priority <= 0 {
		return 1 << 30
	}
	return priority
}
