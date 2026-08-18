package plan

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"accorda/internal/core/state"
)

// ActionKind enumerates the concrete actions a plan can contain. Keeping the
// set small and target-agnostic lets the reconcile loop reason about a plan
// without knowing whether the target is Compose or Kubernetes.
type ActionKind string

const (
	// ActionPull means a service image needs to be pulled before it can run.
	ActionPull ActionKind = "pull"
	// ActionCreate means a service should be created that is not currently
	// running.
	ActionCreate ActionKind = "create"
	// ActionRecreate means a running service should be replaced because its
	// desired definition changed (image, environment, etc.).
	ActionRecreate ActionKind = "recreate"
	// ActionStart means a stopped service should be started.
	ActionStart ActionKind = "start"
	// ActionStop means a running service should be stopped.
	ActionStop ActionKind = "stop"
	// ActionRemove means an orphaned service (present at runtime but not in
	// the desired state) should be removed.
	ActionRemove ActionKind = "remove"
	// ActionNoop means the service is already converged and no change is
	// needed. Plans include Noop actions so that a full plan reflects every
	// service, which makes hashing and auditing deterministic.
	ActionNoop ActionKind = "noop"
)

// Plan is the deployment plan that reconciles a desired state with a target's
// current state (docs/ACCORDA.md §6, §31). It is a value type: callers must
// be able to copy and compare it without aliasing mutable state.
//
// A Plan is intended to be deterministic enough to eventually be hashed and
// signed, so the same plan can be shared and audited across the OSS agent and
// Accorda Cloud. The Hash field is populated by Hash once the plan is final.
type Plan struct {
	// DeploymentID is the identifier Accorda assigned to this deployment.
	DeploymentID string
	// Environment is the target environment the plan applies to.
	Environment string
	// Commit is the Git commit the plan reconciles toward.
	Commit string
	// CreatedAt is when the plan was generated.
	CreatedAt time.Time
	// Actions is the ordered set of actions to apply. Order is significant
	// for dependencies (for example pulling before recreating).
	Actions []Action
	// Security captures vulnerability scan results associated with the
	// plan (docs/ACCORDA.md §31). It is optional.
	Security *Security
	// Policy captures the authorization policy decision for the plan
	// (docs/ACCORDA.md §31, §32). It is optional.
	Policy *Policy
	// Hash is the deterministic hash of the plan, set by Hash. It is empty
	// until Hash is called.
	Hash string
}

// Action describes a single intended change to a single service.
type Action struct {
	// Kind is the type of action.
	Kind ActionKind
	// Service is the name of the service the action applies to.
	Service string
	// Image is the image reference involved in the action, when relevant
	// (for example the image to pull or recreate with).
	Image string
	// From is the previous value being changed from, when applicable.
	From string
	// To is the new value being changed to, when applicable.
	To string
}

// Security summarizes vulnerability scan results for a plan
// (docs/ACCORDA.md §31).
type Security struct {
	Vulnerabilities VulnerabilityCounts
}

// VulnerabilityCounts tallies vulnerabilities by severity.
type VulnerabilityCounts struct {
	Critical int
	High     int
	Medium   int
	Low      int
}

// Policy captures the authorization decision for a plan
// (docs/ACCORDA.md §31, §32).
type Policy struct {
	// Status is the policy decision, e.g. "approved", "approval_required",
	// or "rejected".
	Status string
	// ApprovalsRequired is the number of approvals needed.
	ApprovalsRequired int
	// ApprovalsReceived is the number of approvals received so far.
	ApprovalsReceived int
}

// New returns a Plan populated with the identifying fields and an empty
// action slice. Callers add actions with AddAction and finalize with Hash.
func New(deploymentID, environment, commit string, createdAt time.Time) *Plan {
	return &Plan{
		DeploymentID: deploymentID,
		Environment:  environment,
		Commit:       commit,
		CreatedAt:    createdAt,
		Actions:      make([]Action, 0),
	}
}

// AddAction appends an action to the plan. It returns the plan so actions
// can be chained: p.AddAction(a1).AddAction(a2).
func (p *Plan) AddAction(a Action) *Plan {
	p.Actions = append(p.Actions, a)
	return p
}

// NoopFor returns a Noop action for the named service, used when the service
// is already converged.
func NoopFor(service string) Action {
	return Action{Kind: ActionNoop, Service: service}
}

// Validate reports whether p is internally consistent. It checks the
// identifying fields and every action. It does not require a Hash.
func (p *Plan) Validate() error {
	if p.DeploymentID == "" {
		return errors.New("plan: deployment id is required")
	}
	if p.Commit == "" {
		return errors.New("plan: commit is required")
	}
	for i, a := range p.Actions {
		if a.Service == "" {
			return fmt.Errorf("plan: action[%d] has no service", i)
		}
		switch a.Kind {
		case ActionPull, ActionCreate, ActionRecreate, ActionStart, ActionStop, ActionRemove, ActionNoop:
		default:
			return fmt.Errorf("plan: action[%d] %q has unknown kind %q", i, a.Service, a.Kind)
		}
	}
	return nil
}

// Clone returns a deep copy of p. The Security and Policy pointers are copied
// to fresh allocations so the clone does not alias the original.
func (p *Plan) Clone() *Plan {
	if p == nil {
		return nil
	}
	out := new(Plan)
	*out = *p
	out.Actions = append([]Action(nil), p.Actions...)
	if p.Security != nil {
		sec := *p.Security
		out.Security = &sec
	}
	if p.Policy != nil {
		pol := *p.Policy
		out.Policy = &pol
	}
	return out
}

// Changed reports whether the plan contains any action that changes the
// target. A plan with only Noop actions (or no actions at all) is unchanged.
func (p *Plan) Changed() bool {
	if p == nil {
		return false
	}
	for _, a := range p.Actions {
		if a.Kind != ActionNoop {
			return true
		}
	}
	return false
}

// String returns a human-readable summary of the plan suitable for CLI
// output (docs/ACCORDA.md §9, §11). It lists each service with its action
// kind, using the CHANGED/UNCHANGED vocabulary from §9 so a plan can be
// shown as a per-service diff. The output is deterministic because Actions
// are produced in sorted service order.
func (p *Plan) String() string {
	if p == nil {
		return "plan: <nil>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "deployment=%s environment=%s commit=%s\n", p.DeploymentID, p.Environment, p.Commit)
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionNoop:
			fmt.Fprintf(&b, "%-12s %s\n", a.Service, "UNCHANGED")
		default:
			fmt.Fprintf(&b, "%-12s %s\n", a.Service, "CHANGED")
		}
	}
	return b.String()
}

// DriftActions computes the actions needed to reconcile desired against
// runtime and deployed. It is the pure, target-agnostic diffing logic shared
// by all target drivers: a target's Plan method produces the desired and
// current states and delegates the diff to this function.
//
// The comparison uses desired as the source of truth:
//   - A service in desired but not running is created (or started if it is
//     already deployed but stopped).
//   - A service present at runtime but with a Status other than
//     state.RunningStatus is started (it was stopped, e.g. "docker compose
//     stop api", the canonical §5.3 drift example).
//   - A service whose desired image differs from the running image is
//     recreated.
//   - A service running but not in desired is removed (orphan).
//   - A service that matches desired is a noop.
func DriftActions(desired *state.DesiredState, deployed *state.DeployedState, runtime *state.RuntimeState) []Action {
	if desired == nil {
		desired = &state.DesiredState{}
	}
	if runtime == nil {
		runtime = &state.RuntimeState{}
	}
	deployedExists := make(map[string]bool)
	if deployed != nil {
		for name := range deployed.Services {
			deployedExists[name] = true
		}
	}

	// Iterate service names in sorted order so the returned action slice is
	// deterministic regardless of Go's randomized map iteration order. A
	// plan is intended to be hashed and signed (docs/ACCORDA.md §31), so its
	// action order must be stable (docs/DECISIONS.md #12).
	var actions []Action
	for _, name := range sortedKeys(desired.Services) {
		dsvc := desired.Services[name]
		rsvc, present := runtime.Services[name]
		switch {
		case !present:
			if deployedExists[name] {
				actions = append(actions, Action{Kind: ActionStart, Service: name, Image: dsvc.Image})
			} else {
				actions = append(actions, Action{Kind: ActionCreate, Service: name, Image: dsvc.Image})
			}
		case rsvc.Status != state.RunningStatus:
			// Present but stopped/exited: drift, not convergence. Mirror
			// compareService's status check so a manually stopped service
			// surfaces as a Start action rather than a silent Noop.
			actions = append(actions, Action{Kind: ActionStart, Service: name, Image: dsvc.Image})
		case rsvc.Image != dsvc.Image:
			actions = append(actions, Action{
				Kind:    ActionRecreate,
				Service: name,
				Image:   dsvc.Image,
				From:    rsvc.Image,
				To:      dsvc.Image,
			})
		default:
			actions = append(actions, NoopFor(name))
		}
	}
	for _, name := range sortedKeys(runtime.Services) {
		if _, ok := desired.Services[name]; !ok {
			actions = append(actions, Action{Kind: ActionRemove, Service: name, Image: runtime.Services[name].Image})
		}
	}
	return actions
}

// sortedKeys returns the keys of m in sorted order. It is used to make plan
// action ordering deterministic (docs/DECISIONS.md #12).
func sortedKeys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
