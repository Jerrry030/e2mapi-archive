package upstreamcost

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"e2m.local/contracts"
)

var ErrInvalidUsageObservation = errors.New("upstream cost: invalid usage observation")

// CostFactAppender is the smallest persistence capability required by the
// attribution bridge. Implementations must append the complete four-dimension
// batch atomically; callers never submit one dimension at a time.
type CostFactAppender interface {
	AppendUpstreamCostFacts(context.Context, []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error)
}

// AttributionInput contains identities proved by Core before financial
// attribution. Owner, instance and channel come from the authenticated
// Connector and published binding; they are never accepted from Connector
// telemetry. Link and offer evidence are provided explicitly for auditability.
type AttributionInput struct {
	OwnerID            int64
	InstanceID         string
	ChannelID          string
	UsageObservationID string
	Observation        contracts.ConnectorChannelObservation
	Links              []contracts.UpstreamIntelligenceLink
	Offers             []contracts.UpstreamOfferObservation
	CalculationVersion string
}

// AttributeObservation converts a presence-safe Connector usage observation,
// resolves explicit active channel links, and builds immutable historical cost
// facts. It never falls back to legacy scalar token fields or EstimatedCost.
func AttributeObservation(input AttributionInput) ([]contracts.UpstreamCostFact, error) {
	usage, err := UsageFromConnectorObservation(input.OwnerID, input.InstanceID, input.ChannelID, input.UsageObservationID, input.Observation)
	if err != nil {
		return nil, err
	}
	prices := ResolvePriceEvidence(input.OwnerID, input.ChannelID, input.Links, input.Offers)
	version := strings.TrimSpace(input.CalculationVersion)
	if version == "" {
		version = contracts.UpstreamCostCalculationVersionV1
	}
	return BuildFacts(usage, prices, version)
}

// AttributeAndAppend converts one Core-scoped usage observation and commits
// all four cost dimensions in one store call. Unknown dimensions remain part
// of the same batch so coverage accounting cannot silently lose them.
func AttributeAndAppend(ctx context.Context, appender CostFactAppender, input AttributionInput) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	if appender == nil {
		return nil, contracts.UpstreamCostFactVersion{}, ErrInvalidUsageObservation
	}
	facts, err := AttributeObservation(input)
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	return appender.AppendUpstreamCostFacts(ctx, facts)
}

// UsageFromConnectorObservation trusts only the nullable CostUsage sub-object.
// A nil sub-object therefore produces nil quantities and a missing group even
// when legacy quality-only token scalars are non-zero.
func UsageFromConnectorObservation(ownerID int64, instanceID, channelID, usageObservationID string, observation contracts.ConnectorChannelObservation) (contracts.UpstreamCostUsage, error) {
	instanceID, channelID = strings.TrimSpace(instanceID), strings.TrimSpace(channelID)
	usageObservationID, model := strings.TrimSpace(usageObservationID), strings.TrimSpace(observation.Model)
	if ownerID <= 0 || instanceID == "" || channelID == "" || usageObservationID == "" || model == "" || observation.ObservedAt.IsZero() {
		return contracts.UpstreamCostUsage{}, ErrInvalidUsageObservation
	}
	usage := contracts.UpstreamCostUsage{
		ObservationID: usageObservationID, UserID: ownerID, InstanceID: instanceID,
		ChannelID: channelID, ModelKey: model, OccurredAt: observation.ObservedAt.UTC(),
	}
	if observation.CostUsage == nil {
		return usage, nil
	}
	cost := observation.CostUsage
	if invalidUsageQuantity(cost.InputTokens) || invalidUsageQuantity(cost.OutputTokens) || invalidUsageQuantity(cost.CachedInputTokens) || invalidUsageQuantity(cost.RequestCount) {
		return contracts.UpstreamCostUsage{}, ErrInvalidUsageObservation
	}
	usage.InputTokens = copyUsageQuantity(cost.InputTokens)
	usage.OutputTokens = copyUsageQuantity(cost.OutputTokens)
	usage.CachedInputTokens = copyUsageQuantity(cost.CachedInputTokens)
	usage.RequestCount = copyUsageQuantity(cost.RequestCount)
	if cost.GroupKey != nil {
		group := strings.TrimSpace(*cost.GroupKey)
		if !validUsageGroupKey(group) {
			return contracts.UpstreamCostUsage{}, ErrInvalidUsageObservation
		}
		usage.GroupKey = group
	}
	return usage, nil
}

// ResolvePriceEvidence returns only active, owner-scoped, explicitly verified
// channel links. Dimension-specific links authorize that dimension only; a
// dimensionless channel link authorizes all offer dimensions for its source.
func ResolvePriceEvidence(ownerID int64, channelID string, links []contracts.UpstreamIntelligenceLink, offers []contracts.UpstreamOfferObservation) []PriceEvidence {
	channelID = strings.TrimSpace(channelID)
	if ownerID <= 0 || channelID == "" {
		return nil
	}
	type linkKey struct {
		source    string
		dimension contracts.UpstreamPriceDimension
	}
	verified := make(map[linkKey]struct{})
	for _, link := range links {
		if link.UserID != ownerID || link.Scope != contracts.UpstreamLinkChannel || link.Status != contracts.UpstreamLinkActive ||
			strings.TrimSpace(link.ChannelID) != channelID || strings.TrimSpace(link.IntelligenceSourceID) == "" || link.VerifiedAt == nil {
			continue
		}
		verified[linkKey{source: link.IntelligenceSourceID, dimension: link.PriceDimension}] = struct{}{}
	}
	out := make([]PriceEvidence, 0, len(offers))
	for _, offer := range offers {
		if offer.UserID != ownerID {
			continue
		}
		_, exact := verified[linkKey{source: offer.SourceID, dimension: offer.PriceDimension}]
		_, allDimensions := verified[linkKey{source: offer.SourceID}]
		if !exact && !allDimensions {
			continue
		}
		out = append(out, PriceEvidence{
			OwnerID: ownerID, ChannelID: channelID, IntelligenceSourceID: offer.SourceID,
			TargetVerified: true, Offer: offer,
		})
	}
	return out
}

func invalidUsageQuantity(value *int64) bool { return value != nil && *value < 0 }

func copyUsageQuantity(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validUsageGroupKey(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) || contracts.LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}
