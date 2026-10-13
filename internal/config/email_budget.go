package config

import (
	"fmt"
	"time"
)

// PIN email budget settings.
//
// These bound how many verification PIN emails the deployment may issue, per
// address and in total, over a rolling window.
//
// They are configuration rather than constants for a reason that is the same one
// puts the password-work capacity in configuration: the right numbers are a
// property of a deployment, not of this code. The global figure exists to keep
// the application under a delivery provider's plan quota, and that quota is a
// fact about an account someone chose. A number written into a package would
// silently become wrong the day the plan changed, and would be wrong in whichever
// direction nobody noticed.
//
// The per-address figure is here for the same reason even though it is a policy
// number rather than a provider one: it is a tuning point an operator will want
// to move once real traffic exists, and making that a deployment change rather
// than a code change is the difference between a decision and a release.
const (
	// DefaultPINEmailBudgetPerEmailMax is how many PIN issuance attempts one
	// address may make in the rolling window.
	//
	// Ten a day is generous for a person — a first code, a resend after a
	// misfiled mail, and room for a few mistakes — and tight enough that an
	// attacker who knows an address and can obtain a verification id for it
	// cannot turn it into an unlimited mail cannon. The daily figure is itself
	// load-bearing rather than incidental: over a shorter window a single address
	// could hold enough of the deployment-wide budget to make that budget
	// meaningless, which is the one thing it exists to prevent.
	DefaultPINEmailBudgetPerEmailMax = 10

	// MinPINEmailBudgetPerEmailMax is the smallest value that still lets somebody
	// register or recover. One is the floor: it must be possible to receive the
	// first code.
	MinPINEmailBudgetPerEmailMax = 1

	// MaxPINEmailBudgetPerEmailMax is a typo guard rather than a tuning value. It
	// is bounded well below the deployment-wide default so a stray digit cannot
	// quietly raise one budget past the other and make the smaller one inert.
	MaxPINEmailBudgetPerEmailMax = 100

	// DefaultPINEmailBudgetGlobalMax is how many PIN emails the whole deployment
	// may admit in the rolling window.
	//
	// It sits below the candidate provider plan's daily quota rather than equal
	// to it, and the gap is deliberate: it absorbs operator and test sends, the
	// difference between what this application admits and what the provider
	// processes, and any other consumer of the same account. This is an admission
	// limit — it says nothing about whether the provider delivers, and a provider
	// that defers or retries messages can deliver more in a period than were
	// admitted in it.
	DefaultPINEmailBudgetGlobalMax = 250

	// MinPINEmailBudgetGlobalMax is one: a deployment that may send no messages at
	// all has no reason to run this.
	MinPINEmailBudgetGlobalMax = 1

	// MaxPINEmailBudgetGlobalMax is a typo guard. It is generous rather than
	// policy-shaped on purpose, so it is not mistaken for a recommended figure.
	MaxPINEmailBudgetGlobalMax = 1_000_000

	// DefaultPINEmailBudgetWindowSeconds is the rolling window both budgets use,
	// in seconds. A day matches how a provider's own daily quota is expressed, so
	// the two are being asked the same question.
	DefaultPINEmailBudgetWindowSeconds = 86_400

	// MinPINEmailBudgetWindowSeconds is one second, which is the shortest window
	// that is a window at all rather than a momentary gate.
	MinPINEmailBudgetWindowSeconds = 1

	// MaxPINEmailBudgetWindowSeconds is a typo guard. A rolling window long
	// enough to outlive the deployment is not a budget anyone can reason about.
	MaxPINEmailBudgetWindowSeconds = 30 * 86_400
)

// PINEmailBudget is the resolved budget configuration.
type PINEmailBudget struct {
	// PerEmailMax is how many PIN issuance attempts one address may make inside
	// PerEmailWindow.
	PerEmailMax int64

	// PerEmailWindow is the rolling window the per-address budget is measured
	// over.
	PerEmailWindow time.Duration

	// GlobalMax is how many PIN emails the deployment may admit inside
	// GlobalWindow.
	GlobalMax int64

	// GlobalWindow is the rolling window the deployment-wide budget is measured
	// over.
	GlobalWindow time.Duration
}

// DefaultPINEmailBudget returns the budget a deployment gets when the environment
// says nothing.
//
// It is exported so test wiring can build the same budget production builds,
// rather than leaving a zero-valued configuration to be discovered as a budget
// that refuses everything.
func DefaultPINEmailBudget() PINEmailBudget {
	return PINEmailBudget{
		PerEmailMax:    DefaultPINEmailBudgetPerEmailMax,
		PerEmailWindow: DefaultPINEmailBudgetWindowSeconds * time.Second,
		GlobalMax:      DefaultPINEmailBudgetGlobalMax,
		GlobalWindow:   DefaultPINEmailBudgetWindowSeconds * time.Second,
	}
}

// Validate reports whether the budget can be enforced as written.
//
// It is checked here rather than at the point of use so a deployment with an
// unusable budget refuses to start. A budget of zero would refuse every PIN
// without an error anywhere, which is indistinguishable from the feature being
// broken.
func (b PINEmailBudget) Validate() error {
	if b.PerEmailMax < MinPINEmailBudgetPerEmailMax ||
		b.PerEmailMax > MaxPINEmailBudgetPerEmailMax {
		return fmt.Errorf(
			"PIN_EMAIL_BUDGET_PER_EMAIL_MAX must be between %d and %d, got %d",
			MinPINEmailBudgetPerEmailMax,
			MaxPINEmailBudgetPerEmailMax,
			b.PerEmailMax,
		)
	}

	if b.GlobalMax < MinPINEmailBudgetGlobalMax ||
		b.GlobalMax > MaxPINEmailBudgetGlobalMax {
		return fmt.Errorf(
			"PIN_EMAIL_BUDGET_GLOBAL_MAX must be between %d and %d, got %d",
			MinPINEmailBudgetGlobalMax,
			MaxPINEmailBudgetGlobalMax,
			b.GlobalMax,
		)
	}

	if err := validateBudgetWindow(b.PerEmailWindow); err != nil {
		return fmt.Errorf("PIN_EMAIL_BUDGET_WINDOW_SECONDS: %w", err)
	}

	if err := validateBudgetWindow(b.GlobalWindow); err != nil {
		return fmt.Errorf(
			"PIN_EMAIL_BUDGET_WINDOW_SECONDS (global budget): %w",
			err,
		)
	}

	if b.PerEmailMax > b.GlobalMax {
		return fmt.Errorf(
			"PIN_EMAIL_BUDGET_PER_EMAIL_MAX (%d) must not exceed "+
				"PIN_EMAIL_BUDGET_GLOBAL_MAX (%d): an address could then hold "+
				"more of the deployment's budget than the deployment allows",
			b.PerEmailMax,
			b.GlobalMax,
		)
	}

	return nil
}

func validateBudgetWindow(window time.Duration) error {
	seconds := window.Seconds()

	if seconds < MinPINEmailBudgetWindowSeconds ||
		seconds > MaxPINEmailBudgetWindowSeconds {
		return fmt.Errorf(
			"must be between %d and %d seconds, got %d",
			MinPINEmailBudgetWindowSeconds,
			MaxPINEmailBudgetWindowSeconds,
			int64(seconds),
		)
	}

	return nil
}

// loadPINEmailBudget reads and validates the PIN email budget settings.
//
// The distinction it protects is the one loadPasswordWork protects: absent is an
// ordinary deployment and resolves to the default, while present-and-wrong is a
// mistake and is reported at startup rather than discovered as a budget that
// quietly refuses every PIN or admits every request.
func loadPINEmailBudget() (PINEmailBudget, error) {
	perEmailMax, err := loadBoundedInt(
		"PIN_EMAIL_BUDGET_PER_EMAIL_MAX",
		DefaultPINEmailBudgetPerEmailMax,
		MinPINEmailBudgetPerEmailMax,
		MaxPINEmailBudgetPerEmailMax,
	)
	if err != nil {
		return PINEmailBudget{}, err
	}

	globalMax, err := loadBoundedInt(
		"PIN_EMAIL_BUDGET_GLOBAL_MAX",
		DefaultPINEmailBudgetGlobalMax,
		MinPINEmailBudgetGlobalMax,
		MaxPINEmailBudgetGlobalMax,
	)
	if err != nil {
		return PINEmailBudget{}, err
	}

	// One window value is read and used for both budgets. They are two halves of
	// one question — how much mail may this deployment send — and letting them
	// drift apart would produce a configuration where the per-address budget
	// expires long before the deployment-wide one and the smaller figure silently
	// stops meaning anything.
	windowSeconds, err := loadBoundedInt(
		"PIN_EMAIL_BUDGET_WINDOW_SECONDS",
		DefaultPINEmailBudgetWindowSeconds,
		MinPINEmailBudgetWindowSeconds,
		MaxPINEmailBudgetWindowSeconds,
	)
	if err != nil {
		return PINEmailBudget{}, err
	}

	budget := PINEmailBudget{
		PerEmailMax:    int64(perEmailMax),
		PerEmailWindow: time.Duration(windowSeconds) * time.Second,
		GlobalMax:      int64(globalMax),
		GlobalWindow:   time.Duration(windowSeconds) * time.Second,
	}

	if err := budget.Validate(); err != nil {
		return PINEmailBudget{}, err
	}

	return budget, nil
}
