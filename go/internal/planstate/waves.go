package planstate

// Waves computes dispatch layers for planName via Kahn topological sort over
// plan_deps, honoring markers:
//   - done tasks are satisfied dependencies and never appear in a wave
//   - blocked tasks never appear and never unblock (their dependents wait)
//   - deps outside the projection (archived/missing ids) are treated as
//     satisfied, matching plans.CheckDoneDependencies leniency
//
// Cyclic tasks never become ready and are simply excluded (no hang, no error).
// Waves are a derived view, never stored as authority (spec.md clause 2).
func (s *Store) Waves(planName string) ([][]Node, error) {
	nodes, err := s.Nodes(planName)
	if err != nil {
		return nil, err
	}
	deps, err := s.Deps(planName)
	if err != nil {
		return nil, err
	}

	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}

	// satisfied = done tasks + unknown external ids
	satisfied := func(id string) bool {
		n, ok := byID[id]
		if !ok {
			return true
		}
		return n.Marker == "done"
	}

	pending := map[string]Node{}
	for _, n := range nodes {
		if n.Marker == "todo" || n.Marker == "wip" {
			pending[n.ID] = n
		}
	}

	emitted := map[string]bool{}
	var waves [][]Node
	for len(pending) > 0 {
		var layer []Node
		for _, n := range nodes { // stable order (Nodes is ORDER BY id)
			p, ok := pending[n.ID]
			if !ok {
				continue
			}
			ready := true
			for _, d := range deps[n.ID] {
				if !satisfied(d) && !emitted[d] {
					ready = false
					break
				}
			}
			if ready {
				layer = append(layer, p)
			}
		}
		if len(layer) == 0 {
			break // remaining tasks are cyclic or blocked-gated; exclude
		}
		for _, n := range layer {
			emitted[n.ID] = true
			delete(pending, n.ID)
		}
		waves = append(waves, layer)
	}
	return waves, nil
}

// Next returns the first unblocked task (first node of the first wave),
// or nil when nothing is ready.
func (s *Store) Next(planName string) (*Node, error) {
	waves, err := s.Waves(planName)
	if err != nil {
		return nil, err
	}
	if len(waves) == 0 || len(waves[0]) == 0 {
		return nil, nil
	}
	n := waves[0][0]
	return &n, nil
}
