package layer

import (
	"reflect"
	"testing"
)

func uniqNotNull() []dbtTest {
	return []dbtTest{{Name: "unique"}, {Name: "not_null"}}
}

// TestPkFromModelSingleUniqueKey covers the common single-surrogate-key dim: one
// unique+not_null column is the primary key.
func TestPkFromModelSingleUniqueKey(t *testing.T) {
	md := dbtModel{Name: "dim_x", Columns: []dbtColumn{
		{Name: "x_sk", DataTests: uniqNotNull()},
		{Name: "label"},
	}}
	if got := pkFromModel(md); !reflect.DeepEqual(got, []string{"x_sk"}) {
		t.Errorf("pkFromModel = %v, want [x_sk]", got)
	}
}

// TestPkFromModelMultipleUniqueKeysPicksSurrogate is the dim_campaign case: a
// surrogate key AND a natural key are both unique+not_null. They are independent
// candidate keys, not a composite PK — infer the single _sk surrogate so a
// relationship referencing it stays valid for Snowflake.
func TestPkFromModelMultipleUniqueKeysPicksSurrogate(t *testing.T) {
	md := dbtModel{Name: "dim_campaign", Columns: []dbtColumn{
		{Name: "campaign_sk", DataTests: uniqNotNull()},
		{Name: "campaign_nk", DataTests: uniqNotNull()},
		{Name: "platform"},
	}}
	if got := pkFromModel(md); !reflect.DeepEqual(got, []string{"campaign_sk"}) {
		t.Errorf("pkFromModel = %v, want [campaign_sk] (single surrogate key, not composite)", got)
	}
}

// TestPkFromModelMultipleUniqueNoSurrogatePicksFirst falls back to the first
// candidate when no _sk column exists.
func TestPkFromModelMultipleUniqueNoSurrogatePicksFirst(t *testing.T) {
	md := dbtModel{Name: "t", Columns: []dbtColumn{
		{Name: "natural_id", DataTests: uniqNotNull()},
		{Name: "alt_id", DataTests: uniqNotNull()},
	}}
	if got := pkFromModel(md); !reflect.DeepEqual(got, []string{"natural_id"}) {
		t.Errorf("pkFromModel = %v, want [natural_id] (first candidate)", got)
	}
}

// TestPkFromModelExplicitCompositeConstraintWins verifies a real composite PK,
// declared via an explicit model-level primary_key constraint, is preserved (the
// single-column idiom applies only as a fallback).
func TestPkFromModelExplicitCompositeConstraintWins(t *testing.T) {
	md := dbtModel{
		Name:        "bridge",
		Constraints: []dbtConstraint{{Type: "primary_key", Columns: []string{"a_id", "b_id"}}},
		Columns: []dbtColumn{
			{Name: "a_id", DataTests: uniqNotNull()},
			{Name: "b_id"},
		},
	}
	if got := pkFromModel(md); !reflect.DeepEqual(got, []string{"a_id", "b_id"}) {
		t.Errorf("pkFromModel = %v, want [a_id b_id] (explicit composite constraint)", got)
	}
}
