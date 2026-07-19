package ear

import "time"

// DefaultOrgID is the org id a stack that declares no tenant.md gets.
const DefaultOrgID = "default"

// Tenant is the org a stack belongs to, stacked in tenant.md. It is a
// boundary, not authentication: it carries only which org's data this
// Runtime instance's activity belongs to, never who a caller is. tenant.md
// is optional -- a stack that declares none gets the default tenant.
type Tenant struct {
	OrgID           string
	Name            string
	FiscalYearStart *time.Time
	FiscalYearEnd   *time.Time
	Timezone        string
	SecretEnvVar    string
}

// NewTenant builds the default tenant.
func NewTenant() Tenant { return Tenant{OrgID: DefaultOrgID} }

// FiscalYearBounds is the fiscal year window used for workday-notation
// resolution. A declared start/end is used as-is; undeclared falls back to
// the calendar year containing today.
func (t Tenant) FiscalYearBounds(today time.Time) (time.Time, time.Time) {
	if t.FiscalYearStart != nil && t.FiscalYearEnd != nil {
		return *t.FiscalYearStart, *t.FiscalYearEnd
	}
	if today.IsZero() {
		today = time.Now()
	}
	start := time.Date(today.Year(), 1, 1, 0, 0, 0, 0, today.Location())
	end := time.Date(today.Year(), 12, 31, 0, 0, 0, 0, today.Location())
	return start, end
}
