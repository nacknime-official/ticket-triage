package classifier

import (
	"errors"
	"fmt"
	"slices"
)

// Category names of the default taxonomy (README §3.3).
const (
	CategoryBilling        = "billing"
	CategoryTechnical      = "technical"
	CategoryAccount        = "account"
	CategoryFeatureRequest = "feature_request"
	// CategoryOther is "none of the above". Every taxonomy must contain it.
	CategoryOther = "other"
)

// Category is one routing target and the description a classifier sees.
type Category struct {
	Name        string
	Description string
}

// Taxonomy is the ordered list of categories a classifier chooses from.
type Taxonomy []Category

// DefaultTaxonomy returns the categories of README §3.3.
func DefaultTaxonomy() Taxonomy {
	return Taxonomy{
		{CategoryBilling, "Payments, invoices, refunds, double charges, pricing, subscriptions"},
		{CategoryTechnical, "Bugs, errors, outages, performance, integration or API problems"},
		{CategoryAccount, "Login, password, access, profile, account deletion or security of the account"},
		{CategoryFeatureRequest, "Suggestions or requests for new functionality"},
		{CategoryOther, "Anything that fits none of the categories above"},
	}
}

// Names returns the category names in order.
func (t Taxonomy) Names() []string {
	names := make([]string, len(t))
	for i, c := range t {
		names[i] = c.Name
	}
	return names
}

// Has reports whether name is a category of t.
func (t Taxonomy) Has(name string) bool {
	return slices.ContainsFunc(t, func(c Category) bool { return c.Name == name })
}

// Validate reports whether t has non-empty, unique names, descriptions, and
// contains CategoryOther.
func (t Taxonomy) Validate() error {
	for i, c := range t {
		switch {
		case c.Name == "":
			return fmt.Errorf("classifier: category %d has no name", i)
		case c.Description == "":
			return fmt.Errorf("classifier: category %q has no description", c.Name)
		case t[:i].Has(c.Name):
			return fmt.Errorf("classifier: category %q is repeated", c.Name)
		}
	}
	if !t.Has(CategoryOther) {
		return errors.New("classifier: taxonomy has no " + CategoryOther + " category")
	}
	return nil
}
