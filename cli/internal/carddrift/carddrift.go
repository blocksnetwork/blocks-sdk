// Package carddrift compares a local agent card with the version held by the
// registry and names the fields that differ.
package carddrift

import (
	"reflect"
	"sort"
)

// Diff returns the sorted dotted paths that differ between two decoded JSON objects.
// Arrays are compared whole and reported at their own path.
func Diff(local, registered map[string]any) []string {
	var paths []string
	diffObjects("", local, registered, &paths)
	sort.Strings(paths)
	return paths
}

func diffObjects(prefix string, a, b map[string]any, paths *[]string) {
	for key, av := range a {
		bv, ok := b[key]
		if !ok {
			*paths = append(*paths, join(prefix, key))
			continue
		}
		diffValues(join(prefix, key), av, bv, paths)
	}
	for key := range b {
		if _, ok := a[key]; !ok {
			*paths = append(*paths, join(prefix, key))
		}
	}
}

func diffValues(path string, a, b any, paths *[]string) {
	am, aIsObject := a.(map[string]any)
	bm, bIsObject := b.(map[string]any)
	if aIsObject && bIsObject {
		diffObjects(path, am, bm, paths)
		return
	}
	if !reflect.DeepEqual(a, b) {
		*paths = append(*paths, path)
	}
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
