package state

import "fmt"

// Result is the outcome of comparing the desired, deployed, and runtime
// states (docs/ACCORDA.md §5). The three outcomes correspond to the three
// situations Accorda must distinguish:
//
//   - SYNCED: Git, Accorda's record, and the runtime all agree. Nothing to do.
//   - OUT_OF_SYNC: Git has moved ahead of what Accorda has deployed. A new
//     deployment is needed.
//   - DRIFTED: Git and Accorda agree, but the runtime has diverged (for
//     example a service was stopped manually). Drift repair may be needed.
type Result string

const (
	// ResultSynced means desired == deployed == runtime.
	ResultSynced Result = "SYNCED"
	// ResultOutOfSync means desired != deployed: Accorda has not yet
	// converged on the desired commit.
	ResultOutOfSync Result = "OUT_OF_SYNC"
	// ResultDrifted means desired == deployed but runtime differs from the
	// desired/deployed services. Git has not changed; the target drifted.
	ResultDrifted Result = "DRIFTED"
)

// String returns the result as a stable uppercase label.
func (r Result) String() string { return string(r) }

// Comparison is the structured outcome of Compare. It carries the aggregate
// Result plus the per-service and per-attribute reasons that produced it, so
// callers (CLI, events, history) can report exactly what diverged.
//
// Comparison is a value type: its slices and maps are fresh copies that do
// not alias the input states.
type Comparison struct {
	// Result is the aggregate outcome.
	Result Result
	// Reasons is the set of human-readable differences that drove Result.
	// It is empty when Result is SYNCED.
	Reasons []string
	// Services breaks the comparison down per service name, keyed by the
	// union of desired, deployed, and runtime service names.
	Services map[string]ServiceComparison
}

// ServiceComparison is the per-service breakdown of a Comparison.
type ServiceComparison struct {
	// Result is the per-service outcome.
	Result Result
	// Reasons is the set of differences for this service, empty when the
	// service is SYNCED.
	Reasons []string
}

// String returns a compact, human-readable summary suitable for CLI output.
func (c Comparison) String() string {
	return fmt.Sprintf("sync=%s reasons=%d services=%d", c.Result, len(c.Reasons), len(c.Services))
}

// Compare evaluates desired against deployed and runtime and returns the
// aggregate result plus a per-service breakdown. Any of the inputs may be
// nil, in which case it is treated as the zero value for that state.
//
// The comparison rules, derived from docs/ACCORDA.md §5:
//
//   - A service is SYNCED when it is present in desired and deployed with a
//     matching commit/image and is running at runtime with a matching image.
//   - A service is OUT_OF_SYNC when desired and deployed disagree (missing
//     from deployed, or deployed image differs from desired). Runtime is not
//     consulted for this determination because Accorda must deploy before it
//     can verify runtime.
//   - A service is DRIFTED when desired and deployed agree but the runtime
//     differs: the service is not running, its runtime image differs, or it
//     is an orphan running outside the desired set.
//
// The aggregate Result is the most severe outcome present, with OUT_OF_SYNC
// taking precedence over DRIFTED (a pending deploy supersedes drift repair).
// When there are no differences the aggregate is SYNCED.
func Compare(desired *DesiredState, deployed *DeployedState, runtime *RuntimeState) Comparison {
	if desired == nil {
		desired = &DesiredState{}
	}
	if deployed == nil {
		deployed = &DeployedState{}
	}
	if runtime == nil {
		runtime = &RuntimeState{}
	}

	cmp := Comparison{Services: make(map[string]ServiceComparison)}

	// Commit-level comparison: if the deployed commit differs from the
	// desired commit, every desired service is OUT_OF_SYNC at the commit
	// level even if individual images happen to match, because Accorda has
	// not converged on the declared commit.
	commitOutOfSync := desired.Commit != "" && deployed.Commit != "" && desired.Commit != deployed.Commit
	desiredDeployed := deployed != nil && desired.Commit == deployed.Commit

	// Gather the union of service names across the three states, preserving
	// deterministic handling. Maps are unordered; the per-service Result
	// carries the detail so order does not affect the aggregate.
	names := make(map[string]struct{}, len(desired.Services)+len(deployed.Services)+len(runtime.Services))
	for name := range desired.Services {
		names[name] = struct{}{}
	}
	for name := range deployed.Services {
		names[name] = struct{}{}
	}
	for name := range runtime.Services {
		names[name] = struct{}{}
	}

	anyOutOfSync := false
	anyDrifted := false

	for name := range names {
		sc := compareService(name, desired, deployed, runtime, commitOutOfSync, desiredDeployed)
		cmp.Services[name] = sc
		switch sc.Result {
		case ResultOutOfSync:
			anyOutOfSync = true
			cmp.Reasons = append(cmp.Reasons, sc.Reasons...)
		case ResultDrifted:
			anyDrifted = true
			cmp.Reasons = append(cmp.Reasons, sc.Reasons...)
		}
	}

	switch {
	case anyOutOfSync:
		cmp.Result = ResultOutOfSync
	case anyDrifted:
		cmp.Result = ResultDrifted
	default:
		cmp.Result = ResultSynced
	}
	return cmp
}

// compareService computes the per-service comparison. commitOutOfSync marks
// the case where the deployed commit differs from desired; desiredDeployed
// marks the case where the deployed commit matches desired, which is the
// precondition for drift detection.
func compareService(
	name string,
	desired *DesiredState,
	deployed *DeployedState,
	runtime *RuntimeState,
	commitOutOfSync, desiredDeployed bool,
) ServiceComparison {
	dsvc, dHas := desired.Services[name]
	psvc, pHas := deployed.Services[name]
	rsvc, rHas := runtime.Services[name]

	sc := ServiceComparison{Result: ResultSynced}

	// Orphan: running at runtime but neither desired nor deployed. This is
	// drift regardless of commit agreement, because Git does not declare it.
	if !dHas && !pHas && rHas {
		sc.Result = ResultDrifted
		sc.Reasons = append(sc.Reasons, fmt.Sprintf(
			"service %q: orphan running at runtime but not desired", name))
		return sc
	}

	// OUT_OF_SYNC: desired and deployed disagree on presence or image, or
	// the deployed commit does not match the desired commit.
	if commitOutOfSync && dHas {
		sc.Result = ResultOutOfSync
		sc.Reasons = append(sc.Reasons, fmt.Sprintf(
			"service %q: deployed commit %q != desired commit %q",
			name, deployed.Commit, desired.Commit))
		return sc
	}

	switch {
	case dHas && !pHas:
		sc.Result = ResultOutOfSync
		sc.Reasons = append(sc.Reasons, fmt.Sprintf(
			"service %q: desired but not deployed", name))
		return sc
	case !dHas && pHas:
		// Deployed but no longer desired. This is an out-of-sync removal
		// (the deployed record lags the desired set) unless the runtime
		// has already removed it, in which case it is converged.
		if !rHas {
			return sc
		}
		sc.Result = ResultOutOfSync
		sc.Reasons = append(sc.Reasons, fmt.Sprintf(
			"service %q: deployed but no longer desired and still running", name))
		return sc
	case dHas && pHas && dsvc.Image != psvc.Image:
		sc.Result = ResultOutOfSync
		sc.Reasons = append(sc.Reasons, fmt.Sprintf(
			"service %q: deployed image %q != desired image %q",
			name, psvc.Image, dsvc.Image))
		return sc
	}

	// From here desired and deployed agree (both present with a matching
	// image and commit). Drift detection applies only when the deployed
	// commit equals the desired commit and the service is desired.
	if !desiredDeployed || !dHas {
		// Absent desired+deployed and absent at runtime is converged (the
		// orphan case above already handled a present runtime).
		return sc
	}

	// DRIFTED: desired == deployed but runtime diverges.
	switch {
	case !rHas:
		sc.Result = ResultDrifted
		sc.Reasons = append(sc.Reasons, fmt.Sprintf(
			"service %q: expected running but absent at runtime", name))
	case rsvc.Image != dsvc.Image:
		sc.Result = ResultDrifted
		sc.Reasons = append(sc.Reasons, fmt.Sprintf(
			"service %q: runtime image %q != desired image %q",
			name, rsvc.Image, dsvc.Image))
	}

	return sc
}
