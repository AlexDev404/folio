// Package validator collects per-field form validation errors.
package validator

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Validator type is just a map
type Validator struct {
	Errors map[string]string
}

// We create a new Validator using a factory function
// We have used factory functions before
func NewValidator() *Validator {
	return &Validator{
		Errors: make(map[string]string),
	}
}

// Is the data good?
func (v *Validator) ValidData() bool {
	return len(v.Errors) == 0
}

// Add an error entry to the error map.
// the field and the failed validation message to be sent to the client
func (v *Validator) AddError(field string, message string) {
	_, exists := v.Errors[field]
	if !exists {
		v.Errors[field] = message
	}
}

// CheckField adds an error if the validation check fails
func (v *Validator) Check(ok bool, field string, message string) {
	if !ok {
		v.AddError(field, message)
	}
}

// returns true if data is present in the input box
func NotBlank(value string) bool {
	return strings.TrimSpace(value) != ""
}

// MaxLength returns true if the value contains no more than n characters
func MaxLength(value string, n int) bool {
	return utf8.RuneCountInString(value) <= n
}

// MinLength returns true if the value contains at least n characters
func MinLength(value string, n int) bool {
	return utf8.RuneCountInString(value) >= n
}

// IsEmail returns true if the value is a valid email address
// this is the recommended Regular Expression for valid emails
var EmailRX = regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")

func IsValidEmail(email string) bool {
	return EmailRX.MatchString(email)
}
