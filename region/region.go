// Package region — federated multi-region scaffolding.
//
// Each Sentiae deployment runs in one home region. The Region type +
// helpers below let every service call a single resolver to pick the
// right backend for an org. Today the resolver is a local map; future
// work can swap in a DNS-based or registry-based locator.
//
// Two rules services should honor:
//
//  1. Identity is global. Auth tokens + user rows live in the home
//     region of the *platform*, so a login in EU validates against
//     the same JWKS as a login in US.
//  2. Org data is regional. Every org is pinned to a `region` on
//     creation; subsequent reads for that org route to that region's
//     service URL via `Resolver.Route(orgID, serviceName)`.
package region

import (
	"context"
	"errors"
	"sync"
)

// Region is a short identifier for a deployment region. Match the
// convention "us-east-1", "eu-west-1", "ap-southeast-2".
type Region string

// Well-known regions — not exhaustive; callers can use any string.
const (
	RegionGlobal        Region = ""
	RegionUSEast1       Region = "us-east-1"
	RegionUSWest2       Region = "us-west-2"
	RegionEUWest1       Region = "eu-west-1"
	RegionEUCentral1    Region = "eu-central-1"
	RegionAPSoutheast2  Region = "ap-southeast-2"
)

// Locator maps (region, serviceName) → base URL. A static implementation
// backed by env vars is plenty for deployments that still run a single
// region; multi-region deployments inject a richer implementation
// (e.g. a DNS-backed locator) via DI.
type Locator interface {
	Resolve(ctx context.Context, region Region, service string) (string, error)
}

// ErrRegionNotConfigured means the caller asked for a region/service
// pair that the locator doesn't know about. Wrap or return directly.
var ErrRegionNotConfigured = errors.New("region: not configured")

// StaticLocator is a simple in-memory Locator. Safe for concurrent use.
type StaticLocator struct {
	mu  sync.RWMutex
	tbl map[Region]map[string]string
}

// NewStaticLocator builds an empty locator. Populate via Add.
func NewStaticLocator() *StaticLocator {
	return &StaticLocator{tbl: map[Region]map[string]string{}}
}

// Add registers a region → service → URL mapping. Overwrites on conflict.
func (l *StaticLocator) Add(region Region, service, url string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tbl[region] == nil {
		l.tbl[region] = map[string]string{}
	}
	l.tbl[region][service] = url
}

// Resolve looks up the base URL for a (region, service) pair. Returns
// ErrRegionNotConfigured when the pair isn't registered.
func (l *StaticLocator) Resolve(_ context.Context, region Region, service string) (string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	svcMap, ok := l.tbl[region]
	if !ok {
		return "", ErrRegionNotConfigured
	}
	url, ok := svcMap[service]
	if !ok {
		return "", ErrRegionNotConfigured
	}
	return url, nil
}
