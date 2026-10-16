package host

// Source yields the current host adapters. A Registry is a fixed Source;
// Live rebuilds from the secrets on every call, so a token set at runtime
// is used by the next fetch, push or token check.
type Source interface {
	Hosts() Registry
}

// Hosts makes a Registry its own Source.
func (r Registry) Hosts() Registry { return r }

// Live is a Source built from whatever values returns at the time.
type Live func() map[string]string

// Hosts implements Source.
func (l Live) Hosts() Registry { return FromSecrets(l()) }
