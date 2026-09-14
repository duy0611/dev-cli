// Package secret turns a workspace setting's spec into a value.
//
// A spec says where a value comes from; it is never the value. That is what
// keeps tokens out of the database, out of `workspace show`, and out of any
// backup of either.
package secret

import (
	"context"
	"fmt"
	"strings"
)

// Backend reads values of one scheme.
type Backend interface {
	// Scheme is the spec prefix this backend answers to, without the colon.
	Scheme() string
	// Get resolves the reference part of a spec — everything after the
	// scheme — to a value.
	Get(ctx context.Context, ref string) (string, error)
	// Available reports whether this backend can be used on this host right
	// now, so a missing tool is reported as a missing tool rather than as a
	// failed lookup.
	Available(ctx context.Context) bool
}

// Resolver dispatches specs to backends.
type Resolver struct {
	backends map[string]Backend
}

// NewResolver returns a resolver with the backends this host can use.
func NewResolver() *Resolver {
	r := &Resolver{backends: map[string]Backend{}}
	for _, b := range []Backend{literalBackend{}, keychainBackend{}, onePasswordBackend{}} {
		r.backends[b.Scheme()] = b
	}
	return r
}

// Resolve turns one spec into a value.
//
// The op:// form is the 1Password CLI's own URI, kept verbatim so a spec can be
// pasted straight out of the 1Password app. Everything else is scheme:ref.
func (r *Resolver) Resolve(ctx context.Context, spec string) (string, error) {
	scheme, ref, err := splitSpec(spec)
	if err != nil {
		return "", err
	}

	b, ok := r.backends[scheme]
	if !ok {
		return "", fmt.Errorf("unknown spec scheme %q in %q", scheme, spec)
	}
	if !b.Available(ctx) {
		return "", fmt.Errorf("cannot read %q: the %s backend is not available on this host", spec, scheme)
	}

	v, err := b.Get(ctx, ref)
	if err != nil {
		return "", err
	}
	if v == "" {
		// An empty value is almost always a wrong reference rather than an
		// intentionally empty secret, and it fails far away from here: the tool
		// inside the container sends an empty Bearer token and reports a
		// confusing authentication error.
		return "", fmt.Errorf("%q resolved to an empty value", spec)
	}
	return v, nil
}

// splitSpec separates a spec's scheme from its reference.
func splitSpec(spec string) (scheme, ref string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", fmt.Errorf("empty spec")
	}
	// op:// is a URI, so the whole thing is the reference.
	if strings.HasPrefix(spec, schemeOnePassword+"://") {
		return schemeOnePassword, spec, nil
	}

	scheme, ref, found := strings.Cut(spec, ":")
	if !found {
		return "", "", fmt.Errorf(
			"spec %q has no scheme; use literal:VALUE, keychain:SERVICE or op://vault/item/field", spec)
	}
	if ref == "" {
		return "", "", fmt.Errorf("spec %q has an empty reference", spec)
	}
	return scheme, ref, nil
}
