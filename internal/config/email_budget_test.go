package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The PIN email budget settings: the default, the accepted range, and every way a
// supplied value can be wrong.
//
// The distinction protected is the one loadPasswordWork protects. An absent value
// is an ordinary deployment and resolves to the default. A value that is present
// and wrong is a mistake, and is reported at startup rather than discovered as a
// budget that quietly refuses every PIN or admits every request.
//
// These go through loadPINEmailBudget rather than the exported loader because the
// rest of Load() needs a whole set of unrelated required values, and a case about
// mail budgets is not a case about those.

func loadPINEmailBudgetWith(
	t *testing.T,
	perEmailMax string,
	globalMax string,
	windowSeconds string,
) (PINEmailBudget, error) {
	t.Helper()

	t.Setenv("PIN_EMAIL_BUDGET_PER_EMAIL_MAX", perEmailMax)
	t.Setenv("PIN_EMAIL_BUDGET_GLOBAL_MAX", globalMax)
	t.Setenv("PIN_EMAIL_BUDGET_WINDOW_SECONDS", windowSeconds)

	return loadPINEmailBudget()
}

// The baseline is ten PIN issuance admissions per address a day and 250 for the
// deployment, which is the configuration a deployment gets before anyone has
// chosen a delivery provider or measured a single request.
//
// The global figure is an admission ceiling chosen to sit below a candidate
// provider plan's daily quota rather than at it, so that the gap absorbs operator
// sends, tests and anything else using the same account.
func TestLoadPINEmailBudget_DefaultsToTheAgreedBaseline(t *testing.T) {
	budget, err := loadPINEmailBudget()
	require.NoError(t, err)

	require.Equal(t, int64(10), budget.PerEmailMax)
	require.Equal(t, 24*time.Hour, budget.PerEmailWindow)
	require.Equal(t, int64(250), budget.GlobalMax)
	require.Equal(t, 24*time.Hour, budget.GlobalWindow)

	require.Equal(t, DefaultPINEmailBudget(), budget)
	require.NoError(t, budget.Validate())
}

// A value that is set and valid is used, at both ends of each range.
func TestLoadPINEmailBudget_AcceptsExplicitValuesAcrossTheRange(t *testing.T) {
	cases := map[string]struct {
		perEmailMax string
		globalMax   string
		window      string
		wantPer     int64
		wantGlobal  int64
		wantWindow  time.Duration
	}{
		"the smallest budget that still lets somebody register": {
			perEmailMax: "1", globalMax: "1",
			window:     "60",
			wantPer:    1, wantGlobal: 1, wantWindow: time.Minute,
		},
		"a tighter deployment": {
			perEmailMax: "3", globalMax: "50",
			window:     "3600",
			wantPer:    3, wantGlobal: 50, wantWindow: time.Hour,
		},
		"a larger daily allowance": {
			perEmailMax: "25", globalMax: "1000",
			window:     "86400",
			wantPer:    25, wantGlobal: 1000, wantWindow: 24 * time.Hour,
		},
		"the per-address typo guard": {
			perEmailMax: "100", globalMax: "100",
			window:     "86400",
			wantPer:    100, wantGlobal: 100, wantWindow: 24 * time.Hour,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			budget, err := loadPINEmailBudgetWith(
				t,
				tc.perEmailMax,
				tc.globalMax,
				tc.window,
			)

			require.NoError(t, err)
			require.Equal(t, tc.wantPer, budget.PerEmailMax)
			require.Equal(t, tc.wantGlobal, budget.GlobalMax)

			// One window value is read for both budgets, so they cannot drift
			// apart into a configuration where the smaller figure quietly stops
			// meaning anything.
			require.Equal(t, tc.wantWindow, budget.PerEmailWindow)
			require.Equal(t, tc.wantWindow, budget.GlobalWindow)
		})
	}
}

// Every way a supplied value can be wrong is reported. None is corrected,
// because a deployment that believes it asked for something it did not get is
// worse than one that refuses to start.
func TestLoadPINEmailBudget_RejectsAnUnusableValue(t *testing.T) {
	cases := map[string]struct {
		perEmailMax string
		globalMax   string
		window      string
		errContains string
	}{
		"a per-address budget of zero would refuse every PIN": {
			perEmailMax: "0", globalMax: "250", window: "86400",
			errContains: "PIN_EMAIL_BUDGET_PER_EMAIL_MAX must be between 1 and 100",
		},
		"a negative per-address budget": {
			perEmailMax: "-1", globalMax: "250", window: "86400",
			errContains: "PIN_EMAIL_BUDGET_PER_EMAIL_MAX must be between 1 and 100",
		},
		"a per-address budget past the typo guard": {
			perEmailMax: "500", globalMax: "1000", window: "86400",
			errContains: "PIN_EMAIL_BUDGET_PER_EMAIL_MAX must be between 1 and 100",
		},
		"a per-address budget that is not a number": {
			perEmailMax: "ten", globalMax: "250", window: "86400",
			errContains: "PIN_EMAIL_BUDGET_PER_EMAIL_MAX must be a whole number",
		},
		"a deployment budget of zero": {
			perEmailMax: "10", globalMax: "0", window: "86400",
			errContains: "PIN_EMAIL_BUDGET_GLOBAL_MAX must be between 1 and 1000000",
		},
		"a deployment budget that is not a number": {
			perEmailMax: "10", globalMax: "many", window: "86400",
			errContains: "PIN_EMAIL_BUDGET_GLOBAL_MAX must be a whole number",
		},
		"a window of zero is not a window": {
			perEmailMax: "10", globalMax: "250", window: "0",
			errContains: "PIN_EMAIL_BUDGET_WINDOW_SECONDS must be between 1",
		},
		"a negative window": {
			perEmailMax: "10", globalMax: "250", window: "-60",
			errContains: "PIN_EMAIL_BUDGET_WINDOW_SECONDS must be between 1",
		},
		"a window past the typo guard": {
			perEmailMax: "10", globalMax: "250", window: "31536000",
			errContains: "PIN_EMAIL_BUDGET_WINDOW_SECONDS must be between 1",
		},
		"a window that is not a number": {
			perEmailMax: "10", globalMax: "250", window: "a day",
			errContains: "PIN_EMAIL_BUDGET_WINDOW_SECONDS must be a whole number",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadPINEmailBudgetWith(
				t,
				tc.perEmailMax,
				tc.globalMax,
				tc.window,
			)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// One address may not be allowed more than the deployment is. That configuration
// is not merely odd: it would let a single mailbox hold enough of the
// deployment-wide budget to make that budget meaningless, which is the one thing
// it exists to prevent.
func TestLoadPINEmailBudget_RejectsAPerAddressBudgetAboveTheDeploymentOne(
	t *testing.T,
) {
	_, err := loadPINEmailBudgetWith(t, "100", "10", "86400")

	require.Error(t, err)
	require.Contains(t, err.Error(), "must not exceed")
}

// The per-address budget is checked before the deployment-wide one, so a
// deployment with both wrong is told about the one that would lock every address
// out first.
func TestLoadPINEmailBudget_ReportsPerAddressBeforeGlobal(t *testing.T) {
	_, err := loadPINEmailBudgetWith(t, "0", "also-wrong", "86400")

	require.Error(t, err)
	require.Contains(t, err.Error(), "PIN_EMAIL_BUDGET_PER_EMAIL_MAX")
	require.NotContains(t, err.Error(), "PIN_EMAIL_BUDGET_GLOBAL_MAX")
}

// Validate is exported because a wiring caller can construct a budget by hand and
// needs the same rules the loader applies. A zero-valued one validates as nothing
// at all, which is why the test app fills in the defaults rather than leaving a
// literal's zero fields in place.
func TestPINEmailBudget_ValidateAcceptsOnlyAnEnforceableBudget(t *testing.T) {
	require.NoError(t, DefaultPINEmailBudget().Validate())
	require.Error(t, PINEmailBudget{}.Validate())
}