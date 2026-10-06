package cmd

import (
	"os"
	"testing"
)

// bakedInstanceDomainEnv carries the value a release build injects as defaultInstanceDomain.
const bakedInstanceDomainEnv = "BLOCKS_BAKED_INSTANCE_DOMAIN"

// The release workflows run this against the domain they are about to bake in, so a
// malformed value fails the release instead of shipping a CLI whose short-name logins break.
func TestBakedInstanceDomainIsDNSSuffix(t *testing.T) {
	domain, ok := os.LookupEnv(bakedInstanceDomainEnv)
	if !ok {
		t.Skipf("set %s to the instance domain a build injects", bakedInstanceDomainEnv)
	}
	if !isDNSSuffix(domain) {
		t.Fatalf("%s=%q is not a DNS suffix: 'blocks login acme' would not expand to a host under it", instanceDomainEnv, domain)
	}
}
