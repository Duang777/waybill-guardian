package solve

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type workItem struct {
	requestID domain.RequestID
	request   domain.TransportRequest
}

func requestWorkItems(
	request domain.TransportRequest,
	units map[domain.FulfillmentUnitID]domain.FulfillmentUnit,
) ([]workItem, error) {
	if request.Split.Mode == domain.SplitForbidden || request.Split.SameTrip ||
		len(request.UnitIDs) <= 1 {
		return []workItem{{requestID: request.ID, request: request}}, nil
	}
	for _, task := range request.Tasks {
		if len(task.UnitIDs) == 0 {
			return []workItem{{requestID: request.ID, request: request}}, nil
		}
	}

	parents := make(map[domain.FulfillmentUnitID]domain.FulfillmentUnitID, len(request.UnitIDs))
	for _, unitID := range request.UnitIDs {
		parents[unitID] = unitID
	}
	var find func(domain.FulfillmentUnitID) domain.FulfillmentUnitID
	find = func(value domain.FulfillmentUnitID) domain.FulfillmentUnitID {
		parent := parents[value]
		if parent != value {
			parents[value] = find(parent)
		}
		return parents[value]
	}
	union := func(left, right domain.FulfillmentUnitID) {
		leftRoot := find(left)
		rightRoot := find(right)
		if leftRoot == rightRoot {
			return
		}
		if leftRoot < rightRoot {
			parents[rightRoot] = leftRoot
		} else {
			parents[leftRoot] = rightRoot
		}
	}
	unionUnits := func(values []domain.FulfillmentUnitID) {
		for index := 1; index < len(values); index++ {
			union(values[0], values[index])
		}
	}

	taskUnits := make(map[domain.TaskID][]domain.FulfillmentUnitID, len(request.Tasks))
	for _, task := range request.Tasks {
		taskUnits[task.ID] = task.UnitIDs
		unionUnits(task.UnitIDs)
	}
	for _, task := range request.Tasks {
		for _, predecessorID := range task.PredecessorIDs {
			combined := append(
				append([]domain.FulfillmentUnitID(nil), task.UnitIDs...),
				taskUnits[predecessorID]...,
			)
			unionUnits(combined)
		}
	}

	atomicGroups := make(map[string][]domain.FulfillmentUnitID)
	for _, unitID := range request.UnitIDs {
		if unitID == "" {
			return nil, fmt.Errorf("request %q has an empty unit id", request.ID)
		}
		if unit := units[unitID]; unit.AtomicGroupID != "" {
			atomicGroups[unit.AtomicGroupID] = append(
				atomicGroups[unit.AtomicGroupID],
				unitID,
			)
		}
	}
	for _, values := range atomicGroups {
		unionUnits(values)
	}
	components := make(map[domain.FulfillmentUnitID][]domain.FulfillmentUnitID)
	for _, unitID := range request.UnitIDs {
		root := find(unitID)
		components[root] = append(components[root], unitID)
	}
	groups := make([][]domain.FulfillmentUnitID, 0, len(components))
	for _, values := range components {
		slices.Sort(values)
		groups = append(groups, values)
	}
	slices.SortFunc(groups, func(left, right []domain.FulfillmentUnitID) int {
		return strings.Compare(string(left[0]), string(right[0]))
	})

	minUnits := int(request.Split.MinUnitsPerSplit)
	if minUnits <= 0 {
		minUnits = 1
	}
	fragments := make([][]domain.FulfillmentUnitID, 0, len(groups))
	current := make([]domain.FulfillmentUnitID, 0)
	for _, group := range groups {
		current = append(current, group...)
		if len(current) >= minUnits {
			fragments = append(fragments, current)
			current = nil
		}
	}
	if len(current) > 0 {
		if len(fragments) == 0 {
			fragments = append(fragments, current)
		} else {
			fragments[len(fragments)-1] =
				append(fragments[len(fragments)-1], current...)
		}
	}
	maxSplits := int(request.Split.MaxSplits)
	for maxSplits > 0 && len(fragments) > maxSplits {
		last := fragments[len(fragments)-1]
		fragments = fragments[:len(fragments)-1]
		fragments[len(fragments)-1] = append(fragments[len(fragments)-1], last...)
	}

	result := make([]workItem, 0, len(fragments))
	for _, unitIDs := range fragments {
		unitSet := make(map[domain.FulfillmentUnitID]struct{}, len(unitIDs))
		for _, unitID := range unitIDs {
			unitSet[unitID] = struct{}{}
		}
		tasks := make([]domain.ServiceTask, 0)
		for _, task := range request.Tasks {
			include := false
			for _, unitID := range task.UnitIDs {
				if _, exists := unitSet[unitID]; exists {
					include = true
					break
				}
			}
			if include {
				tasks = append(tasks, task)
			}
		}
		fragment := request
		fragment.UnitIDs = append([]domain.FulfillmentUnitID(nil), unitIDs...)
		fragment.Tasks = tasks
		result = append(result, workItem{requestID: request.ID, request: fragment})
	}
	return result, nil
}
