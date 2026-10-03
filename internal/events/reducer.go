package events

import (
	"encoding/json"
	"sort"
)

type TransportState string

const (
	TransportActive            TransportState = "active"
	TransportRetracted         TransportState = "retracted"
	TransportPendingCorrection TransportState = "pending_correction"
	TransportConflicted        TransportState = "conflicted"
)

type ReviewCode string

const (
	ReviewNone                     ReviewCode = ""
	ReviewCorrectionTargetMissing  ReviewCode = "correction_target_missing"
	ReviewCorrectionTargetInvalid  ReviewCode = "correction_target_invalid"
	ReviewSourceVersionConflict    ReviewCode = "source_version_conflict"
	ReviewIncidentIdentityConflict ReviewCode = "incident_identity_conflict"
)

const maxConflictRefs = 16

type Projection struct {
	State                TransportState
	Current              *DelayDetails
	CurrentRef           *EventRef
	CurrentSourceVersion SourceVersion
	ReviewCode           ReviewCode
	ConflictCount        int
	ConflictRefs         []EventRef
	Fingerprint          Digest
}

func (p Projection) Clone() Projection {
	cloned := p
	if p.Current != nil {
		current := *p.Current
		cloned.Current = &current
	}
	if p.CurrentRef != nil {
		currentRef := *p.CurrentRef
		cloned.CurrentRef = &currentRef
	}
	cloned.ConflictRefs = append([]EventRef(nil), p.ConflictRefs...)
	return cloned
}

type EventSet struct {
	Episode           Episode
	Records           []Record
	CorrectionTargets map[EventRef]Record
}

func Reduce(set EventSet) Projection {
	records := cloneAndSortRecords(set.Records)
	if len(records) == 0 {
		return finalizeProjection(Projection{
			State:         TransportConflicted,
			ReviewCode:    ReviewIncidentIdentityConflict,
			ConflictCount: 1,
		})
	}

	var identityRefs []EventRef
	for _, record := range records {
		if record.Episode() != set.Episode {
			identityRefs = append(identityRefs, record.Ref)
		}
	}
	if len(identityRefs) != 0 {
		return reviewProjection(ReviewIncidentIdentityConflict, identityRefs)
	}

	records, versionConflictRefs := collapseEquivalentVersions(records)
	if len(versionConflictRefs) != 0 {
		return reviewProjection(ReviewSourceVersionConflict, versionConflictRefs)
	}

	targets := make(map[EventRef]Record, len(set.CorrectionTargets)+len(records))
	for ref, record := range set.CorrectionTargets {
		targets[ref] = record.Clone()
	}
	for _, record := range records {
		targets[record.Ref] = record
	}

	detections := make(map[EventRef]Record)
	corrections := make(map[EventRef][]Record)
	var missingRefs []EventRef
	var invalidRefs []EventRef
	for _, record := range records {
		switch record.Type {
		case DelayDetectedType:
			if record.Detected == nil || record.Correction != nil {
				identityRefs = append(identityRefs, record.Ref)
				continue
			}
			detections[record.Ref] = record
		case DelayCorrectedType:
			if record.Correction == nil || record.Detected != nil {
				identityRefs = append(identityRefs, record.Ref)
				continue
			}
			target, found := targets[record.Correction.Corrects]
			if !found {
				missingRefs = append(missingRefs, record.Ref, record.Correction.Corrects)
				continue
			}
			if target.Type != DelayDetectedType ||
				target.Detected == nil ||
				target.Episode() != set.Episode ||
				target.SourceVersion >= record.SourceVersion {
				invalidRefs = append(invalidRefs, record.Ref, target.Ref)
				continue
			}
			corrections[target.Ref] = append(corrections[target.Ref], record)
		default:
			identityRefs = append(identityRefs, record.Ref)
		}
	}
	if len(identityRefs) != 0 {
		return reviewProjection(ReviewIncidentIdentityConflict, identityRefs)
	}
	if len(invalidRefs) != 0 {
		return reviewProjection(ReviewCorrectionTargetInvalid, invalidRefs)
	}

	type candidate struct {
		ref     EventRef
		version SourceVersion
		details DelayDetails
	}
	candidates := make([]candidate, 0, len(detections))
	retracted := false
	for ref, detection := range detections {
		effective := candidate{
			ref:     ref,
			version: detection.SourceVersion,
			details: *detection.Detected,
		}
		targetCorrections := corrections[ref]
		sort.Slice(targetCorrections, func(i, j int) bool {
			if targetCorrections[i].SourceVersion != targetCorrections[j].SourceVersion {
				return targetCorrections[i].SourceVersion > targetCorrections[j].SourceVersion
			}
			return lessRef(targetCorrections[i].Ref, targetCorrections[j].Ref)
		})
		if len(targetCorrections) != 0 {
			correctionRecord := targetCorrections[0]
			correction := correctionRecord.Correction
			switch correction.Operation {
			case CorrectionReplace:
				if correction.Replacement == nil {
					invalidRefs = append(invalidRefs, correctionRecord.Ref)
					continue
				}
				effective = candidate{
					ref:     correctionRecord.Ref,
					version: correctionRecord.SourceVersion,
					details: DelayDetails{
						EventTime:   correction.EventTime,
						DelayFields: *correction.Replacement,
					},
				}
			case CorrectionRetract:
				retracted = true
				continue
			default:
				invalidRefs = append(invalidRefs, correctionRecord.Ref)
				continue
			}
		}
		candidates = append(candidates, effective)
	}
	if len(invalidRefs) != 0 {
		return reviewProjection(ReviewCorrectionTargetInvalid, invalidRefs)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].version != candidates[j].version {
			return candidates[i].version > candidates[j].version
		}
		return lessRef(candidates[i].ref, candidates[j].ref)
	})
	projection := Projection{}
	if len(candidates) != 0 {
		current := candidates[0]
		projection.State = TransportActive
		projection.Current = &current.details
		projection.CurrentRef = &current.ref
		projection.CurrentSourceVersion = current.version
	} else if retracted {
		projection.State = TransportRetracted
	} else {
		projection.State = TransportConflicted
		projection.ReviewCode = ReviewIncidentIdentityConflict
		projection.ConflictCount = 1
	}
	if len(missingRefs) != 0 {
		projection.State = TransportPendingCorrection
		projection.ReviewCode = ReviewCorrectionTargetMissing
		projection.ConflictRefs, projection.ConflictCount = boundedRefs(missingRefs)
	}
	return finalizeProjection(projection)
}

func Classify(before *Projection, after Projection, incoming Record) Disposition {
	if before != nil && before.Fingerprint == after.Fingerprint {
		return DispositionStale
	}
	switch after.State {
	case TransportPendingCorrection:
		return DispositionCorrectionPending
	case TransportConflicted:
		return DispositionManualReview
	case TransportRetracted:
		return DispositionRetracted
	case TransportActive:
		if incoming.Type == DelayCorrectedType {
			return DispositionCorrected
		}
		return DispositionApplied
	default:
		return DispositionManualReview
	}
}

func (p Projection) CanonicalJSON() []byte {
	type canonicalDetails struct {
		EventTime    string   `json:"event_time"`
		Location     Location `json:"location"`
		BusinessStep string   `json:"business_step"`
		ReasonCode   string   `json:"reason_code"`
		StopMinutes  int      `json:"stop_minutes"`
	}
	var current *canonicalDetails
	if p.Current != nil {
		current = &canonicalDetails{
			EventTime:    formatTime(p.Current.EventTime),
			Location:     p.Current.Location,
			BusinessStep: p.Current.BusinessStep,
			ReasonCode:   p.Current.ReasonCode,
			StopMinutes:  p.Current.StopMinutes,
		}
	}
	value, err := json.Marshal(struct {
		State                TransportState    `json:"state"`
		Current              *canonicalDetails `json:"current,omitempty"`
		CurrentRef           *EventRef         `json:"current_ref,omitempty"`
		CurrentSourceVersion SourceVersion     `json:"current_source_version,omitempty"`
		ReviewCode           ReviewCode        `json:"review_code,omitempty"`
		ConflictCount        int               `json:"conflict_count,omitempty"`
		ConflictRefs         []EventRef        `json:"conflict_refs,omitempty"`
	}{
		State:                p.State,
		Current:              current,
		CurrentRef:           p.CurrentRef,
		CurrentSourceVersion: p.CurrentSourceVersion,
		ReviewCode:           p.ReviewCode,
		ConflictCount:        p.ConflictCount,
		ConflictRefs:         p.ConflictRefs,
	})
	if err != nil {
		panic(err)
	}
	return value
}

func cloneAndSortRecords(values []Record) []Record {
	records := make([]Record, len(values))
	for index, record := range values {
		records[index] = record.Clone()
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].SourceVersion != records[j].SourceVersion {
			return records[i].SourceVersion < records[j].SourceVersion
		}
		return lessRef(records[i].Ref, records[j].Ref)
	})
	return records
}

func collapseEquivalentVersions(records []Record) ([]Record, []EventRef) {
	collapsed := make([]Record, 0, len(records))
	var conflicts []EventRef
	for start := 0; start < len(records); {
		end := start + 1
		for end < len(records) && records[end].SourceVersion == records[start].SourceVersion {
			end++
		}
		byDigest := make(map[Digest][]Record)
		for _, record := range records[start:end] {
			recordDigest := record.DataHash
			if recordDigest == "" {
				recordDigest = semanticDigest(record)
			}
			byDigest[recordDigest] = append(byDigest[recordDigest], record)
		}
		if len(byDigest) > 1 {
			for _, record := range records[start:end] {
				conflicts = append(conflicts, record.Ref)
			}
		} else {
			collapsed = append(collapsed, records[start])
		}
		start = end
	}
	return collapsed, conflicts
}

func semanticDigest(record Record) Digest {
	type canonicalCorrection struct {
		Corrects    EventRef            `json:"corrects"`
		Operation   CorrectionOperation `json:"operation"`
		EventTime   string              `json:"event_time"`
		Replacement *DelayFields        `json:"replacement,omitempty"`
		Reason      string              `json:"reason"`
	}
	var detected *struct {
		EventTime string `json:"event_time"`
		DelayFields
	}
	if record.Detected != nil {
		detected = &struct {
			EventTime string `json:"event_time"`
			DelayFields
		}{
			EventTime:   formatTime(record.Detected.EventTime),
			DelayFields: record.Detected.DelayFields,
		}
	}
	var correction *canonicalCorrection
	if record.Correction != nil {
		correction = &canonicalCorrection{
			Corrects:    record.Correction.Corrects,
			Operation:   record.Correction.Operation,
			EventTime:   formatTime(record.Correction.EventTime),
			Replacement: record.Correction.Replacement,
			Reason:      record.Correction.Reason,
		}
	}
	value, err := json.Marshal(struct {
		Type          EventType            `json:"type"`
		WaybillID     string               `json:"waybill_id"`
		IncidentKey   string               `json:"incident_key"`
		SourceVersion SourceVersion        `json:"source_version"`
		Detected      any                  `json:"detected,omitempty"`
		Correction    *canonicalCorrection `json:"correction,omitempty"`
	}{
		Type:          record.Type,
		WaybillID:     string(record.WaybillID),
		IncidentKey:   record.IncidentKey,
		SourceVersion: record.SourceVersion,
		Detected:      detected,
		Correction:    correction,
	})
	if err != nil {
		panic(err)
	}
	return digest("waybill-event-semantic-v1", value)
}

func reviewProjection(code ReviewCode, refs []EventRef) Projection {
	bounded, count := boundedRefs(refs)
	return finalizeProjection(Projection{
		State:         TransportConflicted,
		ReviewCode:    code,
		ConflictCount: count,
		ConflictRefs:  bounded,
	})
}

func boundedRefs(refs []EventRef) ([]EventRef, int) {
	seen := make(map[EventRef]struct{}, len(refs))
	unique := make([]EventRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Source == "" && ref.ID == "" {
			continue
		}
		if _, exists := seen[ref]; exists {
			continue
		}
		seen[ref] = struct{}{}
		unique = append(unique, ref)
	}
	sort.Slice(unique, func(i, j int) bool {
		return lessRef(unique[i], unique[j])
	})
	count := len(unique)
	if len(unique) > maxConflictRefs {
		unique = unique[:maxConflictRefs]
	}
	return unique, count
}

func lessRef(left, right EventRef) bool {
	if left.Source != right.Source {
		return left.Source < right.Source
	}
	return left.ID < right.ID
}

func finalizeProjection(projection Projection) Projection {
	bounded, count := boundedRefs(projection.ConflictRefs)
	projection.ConflictRefs = bounded
	if count > projection.ConflictCount {
		projection.ConflictCount = count
	}
	projection.Fingerprint = digest("waybill-incident-projection-v1", projection.CanonicalJSON())
	return projection
}
