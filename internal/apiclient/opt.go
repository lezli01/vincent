package apiclient

import "encoding/json"

// Opt is one field of a PATCH body with the three states §13.2 gives them:
// absent (leave it alone), null (clear it), and set (assign it). The daemon's
// projectPatchRequest distinguishes all three, and two project fields
// genuinely need it — an unset default_workflow means "fall back to adhoc"
// and an unset max_parallel_tasks means "no project cap", neither of which
// any in-band value can express.
//
// The zero Opt is absent, so a patch struct tags every Opt field `omitzero`
// and a caller only fills in what it is changing. Sending the whole object
// instead would silently stomp a field somebody else just edited.
type Opt[T any] struct {
	set  bool
	null bool
	val  T
}

// SetOpt marks the field as assigned to v.
func SetOpt[T any](v T) Opt[T] { return Opt[T]{set: true, val: v} }

// NullOpt marks the field as explicitly cleared.
func NullOpt[T any]() Opt[T] { return Opt[T]{set: true, null: true} }

// IsZero reports an absent field, which is what `omitzero` keys on. It is
// spelled out rather than left to reflection so a future non-comparable T
// cannot quietly turn every field into "present".
func (o Opt[T]) IsZero() bool { return !o.set }

// Value reports the assigned value and whether the field is set to a value
// (as opposed to absent or null).
func (o Opt[T]) Value() (T, bool) {
	if !o.set || o.null {
		var zero T
		return zero, false
	}
	return o.val, true
}

// IsNull reports an explicit clear.
func (o Opt[T]) IsNull() bool { return o.set && o.null }

// MarshalJSON writes null for a cleared field and the value otherwise; an
// absent field never reaches here, because omitzero drops it first.
func (o Opt[T]) MarshalJSON() ([]byte, error) {
	if o.null {
		return []byte("null"), nil
	}
	return json.Marshal(o.val)
}
