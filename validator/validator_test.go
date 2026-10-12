package validator

import "testing"

func TestValidator(t *testing.T) {
	v := NewValidator()
	v.Check(NotBlank("  "), "name", "required")
	v.Check(MinLength("ab", 3), "name", "too short")
	v.Check(IsValidEmail("a@b.co"), "email", "invalid")
	if v.ValidData() || len(v.Errors) != 1 || v.Errors["name"] != "required" {
		t.Errorf("errors = %v", v.Errors)
	}
}
